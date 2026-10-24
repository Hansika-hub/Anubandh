package core

import (
	"encoding/json"
	"regexp"
	"time"
)

var dealIDRe = regexp.MustCompile(`^DL-\d{8}-\d{4}$`)

const day = 24 * time.Hour

// CreateDeal — Org BUYER. Lines arrive in the transient map key "lines" (private data).
func (e *Engine) CreateDeal(dealJSON string) error {
	c, err := e.requireOrg(OrgBuyer)
	if err != nil {
		return err
	}
	var d Deal
	if err := json.Unmarshal([]byte(dealJSON), &d); err != nil {
		return fail(ErrBadRequest, "dealJSON: %v", err)
	}
	if !dealIDRe.MatchString(d.DealID) {
		return fail(ErrBadRequest, "dealId must match DL-YYYYMMDD-NNNN")
	}
	if trim(d.PONumber) == "" || trim(d.BuyerID) == "" || trim(d.SupplierID) == "" {
		return fail(ErrBadRequest, "poNumber, buyerId and supplierId are required")
	}
	if err := requireParty(c, &d); err != nil {
		return err
	}
	if d.Currency == "" {
		d.Currency = "INR"
	}
	if d.Currency != "INR" {
		return fail(ErrBadRequest, "only INR is supported")
	}
	if d.ToleranceBps < 0 || d.ToleranceBps > 10000 {
		return fail(ErrBadRequest, "toleranceBps must be 0..10000")
	}
	now := e.now()
	if !d.DeliveryDeadline.After(now) {
		return fail(ErrBadRequest, "deliveryDeadline must be in the future")
	}
	if err := validateMSE(&d.MSE); err != nil {
		return err
	}
	if d.ApprovalPolicy, err = validatePolicy(d.ApprovalPolicy); err != nil {
		return err
	}
	if b, _ := e.get(dealKey(d.DealID)); b != nil {
		return fail(ErrBadRequest, "deal %s already exists", d.DealID)
	}
	raw, ok := e.L.Transient("lines")
	if !ok {
		return fail(ErrBadRequest, "transient map key \"lines\" is required")
	}
	var lines []Line
	if err := json.Unmarshal(raw, &lines); err != nil || len(lines) == 0 {
		return fail(ErrBadRequest, "transient lines must be a non-empty JSON array")
	}
	var sum int64
	seen := map[int64]bool{}
	for i := range lines {
		l := &lines[i]
		if l.LineNo < 1 || seen[l.LineNo] || l.OrderedQty < 1 || l.UnitPricePaise < 1 || trim(l.SKU) == "" {
			return fail(ErrBadRequest, "line %d invalid (lineNo>=1 unique, qty>=1, price>=1, sku required)", l.LineNo)
		}
		seen[l.LineNo] = true
		v, err := mul(l.OrderedQty, l.UnitPricePaise)
		if err != nil {
			return err
		}
		if sum, err = add(sum, v); err != nil {
			return err
		}
		l.DealID, l.DeliveredQty, l.AcceptedQty, l.RejectedQty, l.HeldQty = d.DealID, 0, 0, 0, 0
	}
	if d.LienAmountPaise != sum {
		return fail(ErrBadRequest, "lienAmountPaise %d must equal sum of lines %d", d.LienAmountPaise, sum)
	}
	d.Status, d.LienRef, d.TotalReleasedPaise, d.TotalHeldPaise = DealCreated, "", 0, 0
	d.Invoice = Invoice{}
	d.CreatedAt, d.UpdatedAt = now, now
	for i := range lines {
		if err := e.saveLine(&lines[i]); err != nil {
			return err
		}
	}
	if err := e.saveDeal(&d, ""); err != nil {
		return err
	}
	// Per-deal key-level endorsement: writes to this deal need BUYER and BANK peer endorsement.
	if err := e.L.SetKeyEndorsement(dealKey(d.DealID), []Org{OrgBuyer, OrgBank}); err != nil {
		return fail(ErrInternal, "key endorsement: %v", err)
	}
	e.emit("DealCreated", d.DealID, map[string]any{"buyerId": d.BuyerID, "supplierId": d.SupplierID, "lienAmountPaise": d.LienAmountPaise})
	return nil
}

func validateMSE(m *MSE) error {
	if !m.Applies {
		return nil
	}
	if m.Category != MseMicro && m.Category != MseSmall {
		return fail(ErrMseNotApplicable, "payment guard applies to MICRO/SMALL only, got %q", m.Category)
	}
	if m.TermDays != 15 && m.TermDays != 45 {
		return fail(ErrBadRequest, "mse.termDays must be 15 or 45")
	}
	if m.TermDays == 45 && !m.WrittenAgreement {
		return fail(ErrBadRequest, "45-day term requires mse.writtenAgreement=true (MSMED Act s.15)")
	}
	if trim(m.UdyamNo) == "" {
		return fail(ErrBadRequest, "mse.udyamNo required when mse.applies")
	}
	return nil
}

// ConfirmLien — Org BANK. CREATED -> FUNDED.
func (e *Engine) ConfirmLien(dealID, lienRef string, amountPaise int64) error {
	if _, err := e.requireOrg(OrgBank); err != nil {
		return err
	}
	d, err := e.loadDeal(dealID)
	if err != nil {
		return err
	}
	if d.Status != DealCreated {
		return fail(ErrInvalidTransition, "deal is %s, lien can only be confirmed on CREATED", d.Status)
	}
	if trim(lienRef) == "" {
		return fail(ErrBadRequest, "lienRef required")
	}
	if amountPaise != d.LienAmountPaise {
		return fail(ErrLienExceeded, "lien amount %d must equal deal lien %d", amountPaise, d.LienAmountPaise)
	}
	d.LienRef = lienRef
	old := d.Status
	d.Status = DealFunded
	if err := e.saveDeal(d, old); err != nil {
		return err
	}
	e.emit("LienConfirmed", dealID, map[string]any{"lienRef": lienRef, "amountPaise": amountPaise})
	return nil
}

// VerifyInvoice — Org BUYER (after the GST check). Idempotent by idemKey.
func (e *Engine) VerifyInvoice(dealID, irn, idemKey string) error {
	c, err := e.requireOrg(OrgBuyer)
	if err != nil {
		return err
	}
	d, err := e.loadDeal(dealID)
	if err != nil {
		return err
	}
	if err := requireParty(c, d); err != nil {
		return err
	}
	if err := e.consumeIdem(idemKey); err != nil {
		return err
	}
	if d.Status == DealCreated || isTerminal(d.Status) {
		return fail(ErrInvalidTransition, "deal is %s", d.Status)
	}
	if trim(irn) == "" {
		return fail(ErrBadRequest, "irn required")
	}
	d.Invoice = Invoice{IRN: &irn, Verified: true}
	if err := e.saveDeal(d, d.Status); err != nil {
		return err
	}
	e.emit("InvoiceVerified", dealID, map[string]any{"irn": irn})
	return nil
}

// ExpireDeal — time rule. FUNDED, nothing delivered, deadline passed -> EXPIRED (lien released).
// Allowed for BANK or BUYER (see tickAllowed): timestamps are client-proposed in Fabric.
func (e *Engine) ExpireDeal(dealID string) error {
	if _, err := e.requireOrg(OrgBank, OrgBuyer); err != nil {
		return err
	}
	d, err := e.loadDeal(dealID)
	if err != nil {
		return err
	}
	ok, err := e.expireIfDue(d)
	if err != nil {
		return err
	}
	if !ok {
		return fail(ErrInvalidTransition, "deal %s is not eligible for expiry", dealID)
	}
	return nil
}

func (e *Engine) expireIfDue(d *Deal) (bool, error) {
	if d.Status != DealFunded || !e.now().After(d.DeliveryDeadline) {
		return false, nil
	}
	trs, err := e.listTranches(d.DealID)
	if err != nil {
		return false, err
	}
	if len(trs) > 0 {
		return false, nil
	}
	old := d.Status
	d.Status, d.TotalHeldPaise = DealExpired, 0
	if err := e.saveDeal(d, old); err != nil {
		return false, err
	}
	e.emit("DealExpired", d.DealID, map[string]any{"reason": "delivery deadline passed with nothing received"})
	return true, nil
}
