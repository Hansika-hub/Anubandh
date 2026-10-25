package fabric

import (
	"encoding/json"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Hansika-hub/anubandh/gateway/internal/apperr"
	"github.com/Hansika-hub/anubandh/gateway/internal/model"
)

// Mock chaincode rules. They follow BUILD.md §5 and the MSMED Act, 2006:
//   - §2(b): the day of acceptance / deemed acceptance is the day of actual delivery, unless the
//     buyer objects in writing within 15 days, in which case it is the day the objection is removed;
//   - §15: payment is due within the agreed term (max 45 days) or, absent agreement, 15 days.
//
// The real rules live in chaincode/deal (M1); this mock exists so the gateway, UI and demo can run
// before (or without) a Drunix network.
const (
	deemedAcceptanceAfter = 15 * 24 * time.Hour
	mseWarnWindow         = 5 * 24 * time.Hour
	disputeTimeout        = 30 * 24 * time.Hour
	day                   = 24 * time.Hour
)

var mockFns map[string]mockFn

func init() {
	mockFns = map[string]mockFn{
		"CreateDeal":             {1, true, ccCreateDeal},
		"ConfirmLien":            {3, true, ccConfirmLien},
		"VerifyInvoice":          {3, true, ccVerifyInvoice},
		"ExpireDeal":             {1, true, ccExpireDeal},
		"PostGoodsReceipt":       {4, true, ccPostGoodsReceipt},
		"AcknowledgeDelivery":    {2, true, ccAcknowledgeDelivery},
		"RecordQualityCheck":     {4, true, ccRecordQualityCheck},
		"ApproveRelease":         {2, true, ccApproveRelease},
		"ReleaseTranche":         {3, true, ccReleaseTranche},
		"ConfirmSettlement":      {4, true, ccConfirmSettlement},
		"ShipReplacement":        {4, true, ccShipReplacement},
		"ProposeCreditNote":      {4, true, ccProposeCreditNote},
		"SignCreditNote":         {3, true, ccSignCreditNote},
		"ProposePriceAdjustment": {4, true, ccProposePriceAdjustment},
		"SignPriceAdjustment":    {3, true, ccSignPriceAdjustment},
		"RaiseObjection":         {3, true, ccRaiseObjection},
		"ResolveObjection":       {2, true, ccResolveObjection},
		"RaiseDispute":           {4, true, ccRaiseDispute},
		"ResolveDispute":         {4, true, ccResolveDispute},
		"ApplyDisputeTimeout":    {3, true, ccApplyDisputeTimeout},
		"TickDeadlines":          {1, true, ccTickDeadlines},

		"GetDeal":                   {1, false, ccGetDeal},
		"GetTranche":                {2, false, ccGetTranche},
		"ListTranches":              {1, false, ccListTranches},
		"GetHeldPortions":           {1, false, ccGetHeldPortions},
		"GetDealHistory":            {1, false, ccGetDealHistory},
		"QueryDealsByStatus":        {1, false, ccQueryDealsByStatus},
		"QueryMseDealsNearDeadline": {1, false, ccQueryMseDealsNearDeadline},
		"GetMseClock":               {2, false, ccGetMseClock},
	}
}

var dealIDRe = regexp.MustCompile(`^DL-\d{8}-\d{4}$`)

func mul(a, b int64) (int64, error) {
	if a < 0 || b < 0 {
		return 0, apperr.New(apperr.ErrBadRequest, "negative quantity or amount")
	}
	if a != 0 && b > math.MaxInt64/a {
		return 0, apperr.New(apperr.ErrBadRequest, "amount overflows int64 paise")
	}
	return a * b, nil
}

func add(a, b int64) (int64, error) {
	if b > 0 && a > math.MaxInt64-b {
		return 0, apperr.New(apperr.ErrBadRequest, "amount overflows int64 paise")
	}
	return a + b, nil
}

func ptr[T any](v T) *T { return &v }

// ---- deal lifecycle ----

func ccCreateDeal(t *mtx, a []string) ([]byte, error) {
	if err := t.requireOrg(model.OrgBuyer); err != nil {
		return nil, err
	}
	var d model.Deal
	if err := json.Unmarshal([]byte(a[0]), &d); err != nil {
		return nil, apperr.Wrap(apperr.ErrBadRequest, err, "dealJSON is not valid JSON")
	}
	var extra map[string]json.RawMessage
	_ = json.Unmarshal([]byte(a[0]), &extra)
	if !dealIDRe.MatchString(d.DealID) {
		return nil, apperr.New(apperr.ErrBadRequest, "dealId must match DL-YYYYMMDD-NNNN")
	}
	if _, exists := t.st().Deals[d.DealID]; exists {
		return nil, apperr.Newf(apperr.ErrDuplicate, "deal %s already exists", d.DealID)
	}
	if err := t.requireParty(&d); err != nil {
		return nil, err
	}
	raw, ok := t.transient["lines"]
	if !ok || len(raw) == 0 {
		return nil, apperr.New(apperr.ErrBadRequest, `transient map key "lines" is required`)
	}
	var lines []*model.Line
	if err := json.Unmarshal(raw, &lines); err != nil || len(lines) == 0 {
		return nil, apperr.New(apperr.ErrBadRequest, "lines must be a non-empty JSON array")
	}
	var sum int64
	for i, l := range lines {
		if l.LineNo != int64(i+1) || l.OrderedQty <= 0 || l.UnitPricePaise <= 0 || l.SKU == "" {
			return nil, apperr.Newf(apperr.ErrBadRequest, "line %d: lineNo must be sequential from 1, sku set, qty and price > 0", i+1)
		}
		v, err := mul(l.OrderedQty, l.UnitPricePaise)
		if err != nil {
			return nil, err
		}
		if sum, err = add(sum, v); err != nil {
			return nil, err
		}
		l.DealID = d.DealID
		l.DeliveredQty, l.AcceptedQty, l.RejectedQty, l.HeldQty = 0, 0, 0, 0
	}
	if d.LienAmountPaise == 0 {
		d.LienAmountPaise = sum
	}
	if d.LienAmountPaise != sum {
		return nil, apperr.Newf(apperr.ErrBadRequest, "lienAmountPaise %d must equal the PO value %d", d.LienAmountPaise, sum)
	}
	if len(d.ApprovalPolicy) == 0 {
		return nil, apperr.New(apperr.ErrBadRequest, "approvalPolicy is required")
	}
	if d.MSE.Applies {
		if d.MSE.Category != model.MseMicro && d.MSE.Category != model.MseSmall {
			return nil, apperr.New(apperr.ErrMseNotApplicable, "MSMED Act payment terms apply only to micro and small suppliers")
		}
		if d.MSE.TermDays != 15 && d.MSE.TermDays != 45 {
			return nil, apperr.New(apperr.ErrBadRequest, "mse.termDays must be 15 or 45")
		}
		if d.MSE.TermDays == 45 && !d.MSE.WrittenAgreement {
			return nil, apperr.New(apperr.ErrBadRequest, "45-day term requires mse.writtenAgreement=true (MSMED Act s.15)")
		}
		if strings.TrimSpace(d.MSE.UdyamNo) == "" {
			return nil, apperr.New(apperr.ErrBadRequest, "mse.udyamNo required when mse.applies")
		}
	}
	if d.DeliveryDeadline == nil {
		return nil, apperr.New(apperr.ErrBadRequest, "deliveryDeadline is required")
	}
	if d.Currency == "" {
		d.Currency = "INR"
	}
	now := t.now
	d.Status = model.DealCreated
	d.LienRef = nil
	d.TotalReleasedPaise, d.TotalHeldPaise = 0, 0
	d.Invoice = model.Invoice{}
	d.CreatedAt, d.UpdatedAt = &now, &now
	t.st().Deals[d.DealID] = &d
	t.st().Lines[d.DealID] = lines
	known := map[string]bool{}
	for _, k := range []string{"dealId", "poNumber", "buyerId", "supplierId", "status", "currency", "lienAmountPaise", "lienRef",
		"totalReleasedPaise", "totalHeldPaise", "mse", "approvalPolicy", "toleranceBps", "deliveryDeadline", "invoice", "createdAt", "updatedAt"} {
		known[k] = true
	}
	for k := range extra {
		if known[k] {
			delete(extra, k)
		}
	}
	if len(extra) > 0 {
		t.st().Extra[d.DealID] = extra
	}
	t.touch(d.DealID)
	t.emit(model.EvDealCreated, d.DealID, map[string]any{"buyerId": d.BuyerID, "supplierId": d.SupplierID, "lienAmountPaise": d.LienAmountPaise})
	return nil, nil
}

func ccConfirmLien(t *mtx, a []string) ([]byte, error) {
	if err := t.requireOrg(model.OrgBank); err != nil {
		return nil, err
	}
	d, err := t.deal(a[0])
	if err != nil {
		return nil, err
	}
	amount, err := argInt(a[2], "amountPaise", 1)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(a[1]) == "" {
		return nil, apperr.New(apperr.ErrBadRequest, "lienRef is required")
	}
	if d.Status != model.DealCreated {
		return nil, apperr.Newf(apperr.ErrInvalidTransition, "deal %s is %s, lien can only be confirmed on CREATED", d.DealID, d.Status)
	}
	if amount != d.LienAmountPaise {
		return nil, apperr.Newf(apperr.ErrLienExceeded, "lien amount %d must equal deal lien %d", amount, d.LienAmountPaise)
	}
	d.Status = model.DealFunded
	d.LienRef = ptr(a[1])
	t.touch(d.DealID)
	t.emit(model.EvLienConfirmed, d.DealID, map[string]any{"lienRef": a[1], "amountPaise": amount})
	return nil, nil
}

func ccVerifyInvoice(t *mtx, a []string) ([]byte, error) {
	if err := t.requireOrg(model.OrgBuyer); err != nil {
		return nil, err
	}
	if err := t.useIdem(a[2]); err != nil {
		return nil, err
	}
	d, err := t.deal(a[0])
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(a[1]) == "" {
		return nil, apperr.New(apperr.ErrBadRequest, "irn is required")
	}
	if d.Status == model.DealExpired || d.Status == model.DealClosed {
		return nil, apperr.Newf(apperr.ErrInvalidTransition, "deal %s is %s", d.DealID, d.Status)
	}
	d.Invoice = model.Invoice{IRN: ptr(a[1]), Verified: true}
	t.touch(d.DealID)
	t.emit(model.EvInvoiceVerified, d.DealID, map[string]any{"irn": a[1]})
	return nil, nil
}

func ccExpireDeal(t *mtx, a []string) ([]byte, error) {
	d, err := t.deal(a[0])
	if err != nil {
		return nil, err
	}
	if !expirable(t, d) {
		return nil, apperr.Newf(apperr.ErrInvalidTransition, "deal %s is not past its delivery deadline undelivered", d.DealID)
	}
	expire(t, d)
	return nil, nil
}

func expirable(t *mtx, d *model.Deal) bool {
	return d.Status == model.DealFunded && len(t.st().Tranches[d.DealID]) == 0 &&
		d.DeliveryDeadline != nil && t.now.After(*d.DeliveryDeadline)
}

func expire(t *mtx, d *model.Deal) {
	d.Status = model.DealExpired
	t.touch(d.DealID)
	t.emit(model.EvDealExpired, d.DealID, map[string]any{"reason": "DELIVERY_DEADLINE_PASSED"})
}

// ---- delivery & QC ----

func ccPostGoodsReceipt(t *mtx, a []string) ([]byte, error) {
	if err := t.requireOrg(model.OrgBuyer); err != nil {
		return nil, err
	}
	if t.id.Role != model.RoleWarehouse {
		return nil, apperr.New(apperr.ErrForbiddenRole, "goods receipt requires role WAREHOUSE")
	}
	if err := t.useIdem(a[3]); err != nil {
		return nil, err
	}
	d, err := t.deal(a[0])
	if err != nil {
		return nil, err
	}
	n, err := argInt(a[1], "trancheNo", 1)
	if err != nil {
		return nil, err
	}
	switch d.Status {
	case model.DealCreated, model.DealExpired, model.DealClosed:
		return nil, apperr.Newf(apperr.ErrInvalidTransition, "deal %s is %s; goods can be received only on a funded deal", d.DealID, d.Status)
	}
	var in []model.TrancheLine
	if err := json.Unmarshal([]byte(a[2]), &in); err != nil || len(in) == 0 {
		return nil, apperr.New(apperr.ErrBadRequest, `linesJSON must be [{"lineNo":1,"deliveredQty":100}]`)
	}
	seen := map[int64]bool{}
	for _, l := range in {
		if l.DeliveredQty <= 0 || seen[l.LineNo] {
			return nil, apperr.New(apperr.ErrBadRequest, "each line needs a unique lineNo and deliveredQty > 0")
		}
		seen[l.LineNo] = true
		if _, err := t.line(d.DealID, l.LineNo); err != nil {
			return nil, err
		}
	}
	tranches := t.st().Tranches[d.DealID]
	var tr *mockTranche
	if existing, err := t.tranche(d.DealID, n); err == nil {
		if existing.Status != model.TranchePending {
			return nil, apperr.Newf(apperr.ErrInvalidTransition, "tranche %d already received", n)
		}
		if len(in) != len(existing.Expected) {
			return nil, apperr.Newf(apperr.ErrQtyMismatch, "replacement tranche expects %d line(s)", len(existing.Expected))
		}
		for _, l := range in {
			if l.DeliveredQty != existing.Expected[l.LineNo] {
				return nil, apperr.Newf(apperr.ErrQtyMismatch, "replacement line %d must deliver exactly %d", l.LineNo, existing.Expected[l.LineNo])
			}
		}
		tr = existing
	} else {
		if n != int64(len(tranches))+1 {
			return nil, apperr.Newf(apperr.ErrInvalidTransition, "next tranche for %s is %d, got %d", d.DealID, len(tranches)+1, n)
		}
		for _, l := range in {
			line, _ := t.line(d.DealID, l.LineNo)
			var prior int64
			for _, x := range tranches {
				if x.Expected != nil || x.Prices != nil {
					continue
				}
				for _, xl := range x.Lines {
					if xl.LineNo == l.LineNo {
						prior += xl.DeliveredQty
					}
				}
			}
			if prior+l.DeliveredQty > line.OrderedQty {
				return nil, apperr.Newf(apperr.ErrQtyMismatch, "line %d: %d delivered so far + %d exceeds ordered %d", l.LineNo, prior, l.DeliveredQty, line.OrderedQty)
			}
		}
		tr = &mockTranche{Tranche: model.Tranche{DealID: d.DealID, TrancheNo: n}}
		t.st().Tranches[d.DealID] = append(tranches, tr)
	}
	tr.Lines = tr.Lines[:0]
	sort.Slice(in, func(i, j int) bool { return in[i].LineNo < in[j].LineNo })
	for _, l := range in {
		tr.Lines = append(tr.Lines, model.TrancheLine{LineNo: l.LineNo, DeliveredQty: l.DeliveredQty})
		line, _ := t.line(d.DealID, l.LineNo)
		line.DeliveredQty += l.DeliveredQty
	}
	now := t.now
	tr.Status = model.TrancheDelivered
	tr.DeliveredAt = &now
	recomputeDealStatus(t, d)
	t.touch(d.DealID)
	t.emit(model.EvGoodsReceived, d.DealID, map[string]any{"trancheNo": n, "lines": tr.Lines})
	return nil, nil
}

func ccAcknowledgeDelivery(t *mtx, a []string) ([]byte, error) {
	if err := t.requireOrg(model.OrgSupplier); err != nil {
		return nil, err
	}
	d, err := t.deal(a[0])
	if err != nil {
		return nil, err
	}
	if err := t.requireParty(d); err != nil {
		return nil, err
	}
	n, err := argInt(a[1], "trancheNo", 1)
	if err != nil {
		return nil, err
	}
	tr, err := t.tranche(d.DealID, n)
	if err != nil {
		return nil, err
	}
	if tr.Status == model.TranchePending {
		return nil, apperr.Newf(apperr.ErrInvalidTransition, "tranche %d has not been delivered", n)
	}
	if tr.Acked {
		return nil, apperr.Newf(apperr.ErrDuplicate, "tranche %d already acknowledged", n)
	}
	tr.Acked = true
	t.touch(d.DealID)
	t.emit(model.EvDeliveryAcknowledged, d.DealID, map[string]any{"trancheNo": n})
	return nil, nil
}

// unitPrice returns the price a tranche's line is valued at (adjusted price if agreed).
func unitPrice(t *mtx, dealID string, tr *mockTranche, lineNo int64) (int64, error) {
	if p, ok := tr.Prices[lineNo]; ok {
		return p, nil
	}
	l, err := t.line(dealID, lineNo)
	if err != nil {
		return 0, err
	}
	return l.UnitPricePaise, nil
}

// startMseClock applies MSMED Act §15: due date = day of acceptance + agreed term (15 or 45 days).
func startMseClock(d *model.Deal, tr *mockTranche, start time.Time) {
	if !d.MSE.Applies || d.MSE.TermDays <= 0 {
		return
	}
	deadline := start.Add(time.Duration(d.MSE.TermDays) * day)
	tr.MseClockStart, tr.MseDeadline = &start, &deadline
	tr.Warned = false
}

func ccRecordQualityCheck(t *mtx, a []string) ([]byte, error) {
	if err := t.requireOrg(model.OrgBuyer); err != nil {
		return nil, err
	}
	if err := t.useIdem(a[3]); err != nil {
		return nil, err
	}
	d, err := t.deal(a[0])
	if err != nil {
		return nil, err
	}
	n, err := argInt(a[1], "trancheNo", 1)
	if err != nil {
		return nil, err
	}
	tr, err := t.tranche(d.DealID, n)
	if err != nil || tr.Status == model.TranchePending {
		return nil, apperr.Newf(apperr.ErrInvalidTransition, "tranche %d has no goods receipt yet; QC must follow receipt", n)
	}
	if tr.Status != model.TrancheDelivered {
		return nil, apperr.Newf(apperr.ErrInvalidTransition, "tranche %d is %s; QC already recorded", n, tr.Status)
	}
	var res []model.TrancheLine
	if err := json.Unmarshal([]byte(a[2]), &res); err != nil || len(res) != len(tr.Lines) {
		return nil, apperr.New(apperr.ErrQtyMismatch, "resultsJSON must hold one result per delivered line")
	}
	byLine := map[int64]model.TrancheLine{}
	for _, r := range res {
		byLine[r.LineNo] = r
	}
	var accVal, heldVal int64
	var held []*mockHeld
	for i, l := range tr.Lines {
		r, ok := byLine[l.LineNo]
		if !ok || r.AcceptedQty < 0 || r.RejectedQty < 0 || r.AcceptedQty+r.RejectedQty != l.DeliveredQty {
			return nil, apperr.Newf(apperr.ErrQtyMismatch, "line %d: accepted + rejected must equal delivered %d", l.LineNo, l.DeliveredQty)
		}
		acc, rej := r.AcceptedQty, r.RejectedQty
		if rej > 0 && d.ToleranceBps > 0 && rej*10000 <= l.DeliveredQty*d.ToleranceBps {
			acc, rej = l.DeliveredQty, 0 // variance within the agreed QC tolerance
		}
		price, err := unitPrice(t, d.DealID, tr, l.LineNo)
		if err != nil {
			return nil, err
		}
		av, err := mul(acc, price)
		if err != nil {
			return nil, err
		}
		hv, err := mul(rej, price)
		if err != nil {
			return nil, err
		}
		accVal += av
		heldVal += hv
		tr.Lines[i].AcceptedQty, tr.Lines[i].RejectedQty = acc, rej
		line, _ := t.line(d.DealID, l.LineNo)
		line.AcceptedQty += acc
		line.RejectedQty += rej
		line.HeldQty += rej
		if rej > 0 {
			held = append(held, &mockHeld{HeldPortion: model.HeldPortion{DealID: d.DealID, TrancheNo: n, LineNo: l.LineNo, Qty: rej, ValuePaise: hv, State: model.TrancheHeld}})
		}
	}
	now := t.now
	tr.AcceptedValuePaise, tr.HeldValuePaise = accVal, heldVal
	tr.AcceptedAt = &now
	if accVal > 0 {
		tr.Status = model.TrancheAccepted
		// §2(b)(i): the day of acceptance is the day of actual delivery, unless a written objection
		// was raised and later removed (then the clock restarts at removal, see ResolveObjection).
		startMseClock(d, tr, acceptanceDay(tr))
	} else {
		tr.Status = model.TrancheHeld
	}
	t.st().Held[d.DealID] = append(t.st().Held[d.DealID], held...)
	d.TotalHeldPaise += heldVal
	recomputeDealStatus(t, d)
	t.touch(d.DealID)
	t.emit(model.EvQualityRecorded, d.DealID, map[string]any{"trancheNo": n, "acceptedValuePaise": accVal, "heldValuePaise": heldVal})
	for _, h := range held {
		t.emit(model.EvHeldPortionCreated, d.DealID, map[string]any{"trancheNo": n, "lineNo": h.LineNo, "qty": h.Qty, "valuePaise": h.ValuePaise})
	}
	if accVal > 0 {
		emitAcceptance(t, d, tr, model.EvTrancheAccepted)
	}
	return nil, nil
}

// emitAcceptance announces an acceptance (TrancheAccepted or DeemedAcceptance) followed, on MSE
// deals, by the payment clock it starts, in the same order as the real chaincode.
func emitAcceptance(t *mtx, d *model.Deal, tr *mockTranche, name string) {
	p := map[string]any{"trancheNo": tr.TrancheNo}
	if tr.MseClockStart != nil {
		p["mseClockStart"], p["mseDeadline"] = tr.MseClockStart, tr.MseDeadline
	}
	if name == model.EvTrancheAccepted {
		p["deemed"] = false
	}
	t.emit(name, d.DealID, p)
	if d.MSE.Applies && tr.MseClockStart != nil {
		t.emit(model.EvMseClockStarted, d.DealID, map[string]any{"trancheNo": tr.TrancheNo, "termDays": d.MSE.TermDays, "mseDeadline": tr.MseDeadline})
	}
}

// ---- approval & release ----

func bandFor(policy []model.PolicyBand, value int64) (model.PolicyBand, bool) {
	best := -1
	for i, b := range policy {
		if b.MinPaise <= value && (best < 0 || b.MinPaise > policy[best].MinPaise) {
			best = i
		}
	}
	if best < 0 {
		return model.PolicyBand{}, false
	}
	return policy[best], true
}

func policySatisfied(d *model.Deal, tr *mockTranche) bool {
	band, ok := bandFor(d.ApprovalPolicy, tr.AcceptedValuePaise)
	if !ok || len(band.RequiredRoles) == 0 {
		return false
	}
	have := map[model.Role]bool{}
	for _, ap := range tr.Approvals {
		have[ap.Role] = true
	}
	for _, r := range band.RequiredRoles {
		if !have[r] {
			return false
		}
	}
	return true
}

func ccApproveRelease(t *mtx, a []string) ([]byte, error) {
	if err := t.requireOrg(model.OrgBuyer); err != nil {
		return nil, err
	}
	d, err := t.deal(a[0])
	if err != nil {
		return nil, err
	}
	if err := t.requireParty(d); err != nil {
		return nil, err
	}
	n, err := argInt(a[1], "trancheNo", 1)
	if err != nil {
		return nil, err
	}
	tr, err := t.tranche(d.DealID, n)
	if err != nil {
		return nil, err
	}
	for _, ap := range tr.Approvals {
		if ap.Role == t.id.Role {
			return nil, apperr.Newf(apperr.ErrDuplicate, "%s already approved tranche %d", t.id.Role, n)
		}
	}
	switch tr.Status {
	case model.TrancheAccepted, model.TrancheDeemedAccepted:
	case model.TrancheReleased, model.TrancheSettled:
		return nil, apperr.Newf(apperr.ErrAlreadyReleased, "tranche %d already released", n)
	default:
		return nil, apperr.Newf(apperr.ErrInvalidTransition, "tranche %d is %s; only accepted tranches can be approved", n, tr.Status)
	}
	if tr.Obj != nil && tr.Obj.Open {
		return nil, apperr.Newf(apperr.ErrInvalidTransition, "tranche %d has an open objection", n)
	}
	band, ok := bandFor(d.ApprovalPolicy, tr.AcceptedValuePaise)
	if !ok {
		return nil, apperr.New(apperr.ErrPolicyNotSatisfied, "no approval band covers this amount")
	}
	allowed := false
	for _, r := range band.RequiredRoles {
		if r == t.id.Role {
			allowed = true
		}
	}
	if !allowed {
		return nil, apperr.Newf(apperr.ErrForbiddenRole, "role %s is not an approver for %d paise (needs %v)", t.id.Role, tr.AcceptedValuePaise, band.RequiredRoles)
	}
	now := t.now
	tr.Approvals = append(tr.Approvals, model.Approval{Role: t.id.Role, UserID: t.id.UserID, SignedAt: &now})
	satisfied := policySatisfied(d, tr)
	if satisfied {
		tr.Status = model.TrancheApproved
	}
	t.touch(d.DealID)
	t.emit(model.EvApprovalRecorded, d.DealID, map[string]any{"trancheNo": n, "role": t.id.Role, "userId": t.id.UserID, "satisfied": satisfied})
	return nil, nil
}

func ccReleaseTranche(t *mtx, a []string) ([]byte, error) {
	if err := t.requireOrg(model.OrgBuyer); err != nil {
		return nil, err
	}
	if err := t.useIdem(a[2]); err != nil {
		return nil, err
	}
	d, err := t.deal(a[0])
	if err != nil {
		return nil, err
	}
	n, err := argInt(a[1], "trancheNo", 1)
	if err != nil {
		return nil, err
	}
	tr, err := t.tranche(d.DealID, n)
	if err != nil {
		return nil, err
	}
	switch tr.Status {
	case model.TrancheApproved:
	case model.TrancheReleased, model.TrancheSettled:
		return nil, apperr.Newf(apperr.ErrAlreadyReleased, "tranche %d already released", n)
	case model.TrancheAccepted, model.TrancheDeemedAccepted:
		return nil, apperr.Newf(apperr.ErrPolicyNotSatisfied, "tranche %d still needs approvals", n)
	default:
		return nil, apperr.Newf(apperr.ErrInvalidTransition, "tranche %d is %s", n, tr.Status)
	}
	if !policySatisfied(d, tr) {
		return nil, apperr.Newf(apperr.ErrPolicyNotSatisfied, "tranche %d approvals do not satisfy the policy", n)
	}
	// Amount is computed on-chain from accepted quantities and private unit prices, never taken from the caller.
	var amount int64
	for _, l := range tr.Lines {
		price, err := unitPrice(t, d.DealID, tr, l.LineNo)
		if err != nil {
			return nil, err
		}
		v, err := mul(l.AcceptedQty, price)
		if err != nil {
			return nil, err
		}
		amount += v
	}
	if amount <= 0 {
		return nil, apperr.Newf(apperr.ErrInvalidTransition, "tranche %d has nothing to release", n)
	}
	if d.TotalReleasedPaise+amount > d.LienAmountPaise {
		return nil, apperr.Newf(apperr.ErrLienExceeded, "release %d + released %d exceeds lien %d", amount, d.TotalReleasedPaise, d.LienAmountPaise)
	}
	tr.Status = model.TrancheReleased
	tr.ReleaseIdemKey = ptr(a[2])
	d.TotalReleasedPaise += amount
	recomputeDealStatus(t, d)
	t.touch(d.DealID)
	t.emit(model.EvTrancheReleased, d.DealID, map[string]any{
		"trancheNo": n, "amountPaise": amount, "supplierId": d.SupplierID, "supplierAccountRef": "ACCT-" + d.SupplierID,
	})
	return nil, nil
}

func ccConfirmSettlement(t *mtx, a []string) ([]byte, error) {
	if err := t.requireOrg(model.OrgBank); err != nil {
		return nil, err
	}
	d, err := t.deal(a[0])
	if err != nil {
		return nil, err
	}
	n, err := argInt(a[1], "trancheNo", 1)
	if err != nil {
		return nil, err
	}
	bankRef, rail := a[2], model.Rail(a[3])
	if bankRef == "" || !rail.Valid() {
		return nil, apperr.New(apperr.ErrBadRequest, "bankRef and a valid rail are required")
	}
	tr, err := t.tranche(d.DealID, n)
	if err != nil {
		return nil, err
	}
	if tr.Status == model.TrancheSettled {
		if tr.BankRef != nil && *tr.BankRef == bankRef {
			return nil, apperr.Newf(apperr.ErrDuplicate, "tranche %d already settled with %s", n, bankRef)
		}
		return nil, apperr.Newf(apperr.ErrInvalidTransition, "tranche %d already settled", n)
	}
	if tr.Status != model.TrancheReleased {
		return nil, apperr.Newf(apperr.ErrInvalidTransition, "tranche %d is %s, not RELEASED", n, tr.Status)
	}
	tr.Status = model.TrancheSettled
	tr.BankRef, tr.Rail = ptr(bankRef), ptr(string(rail))
	recomputeDealStatus(t, d)
	t.touch(d.DealID)
	t.emit(model.EvSettlementConfirmed, d.DealID, map[string]any{"trancheNo": n, "bankRef": bankRef, "rail": rail})
	return nil, nil
}

// ---- held portion exits ----

func heldArgs(t *mtx, a []string, orgs ...model.Org) (*model.Deal, *mockHeld, error) {
	if err := t.requireOrg(orgs...); err != nil {
		return nil, nil, err
	}
	d, err := t.deal(a[0])
	if err != nil {
		return nil, nil, err
	}
	if err := t.requireParty(d); err != nil {
		return nil, nil, err
	}
	n, err := argInt(a[1], "trancheNo", 1)
	if err != nil {
		return nil, nil, err
	}
	ln, err := argInt(a[2], "lineNo", 1)
	if err != nil {
		return nil, nil, err
	}
	h, err := t.held(d.DealID, n, ln)
	if err != nil {
		return nil, nil, err
	}
	return d, h, nil
}

func requireHeld(h *mockHeld) error {
	if h.State != model.TrancheHeld {
		return apperr.Newf(apperr.ErrInvalidTransition, "held portion %d/%d is %s", h.TrancheNo, h.LineNo, h.State)
	}
	return nil
}

func closeHeld(t *mtx, d *model.Deal, h *mockHeld, exit string) {
	h.Exit, h.State = ptr(exit), model.TrancheClosed
	d.TotalHeldPaise -= h.ValuePaise
	if line, err := t.line(d.DealID, h.LineNo); err == nil {
		line.HeldQty -= h.Qty
	}
	if tr, err := t.tranche(d.DealID, h.TrancheNo); err == nil {
		tr.HeldExit = ptr(exit)
	}
}

// newAcceptedTranche makes a releasable tranche out of a held portion (price adjustment or a
// dispute resolved for the supplier). Its MSE clock starts on the day the portion is accepted.
func newAcceptedTranche(t *mtx, d *model.Deal, h *mockHeld, price int64) (*mockTranche, error) {
	v, err := mul(h.Qty, price)
	if err != nil {
		return nil, err
	}
	now := t.now
	src, err := t.tranche(d.DealID, h.TrancheNo)
	if err != nil {
		return nil, err
	}
	nt := &mockTranche{
		Tranche: model.Tranche{
			DealID: d.DealID, TrancheNo: int64(len(t.st().Tranches[d.DealID])) + 1, Status: model.TrancheAccepted,
			// No new goods arrive: the tranche accepts units already delivered (and rejected) earlier.
			Lines:              []model.TrancheLine{{LineNo: h.LineNo, DeliveredQty: 0, AcceptedQty: h.Qty}},
			AcceptedValuePaise: v, DeliveredAt: src.DeliveredAt, AcceptedAt: &now,
		},
		Prices: map[int64]int64{h.LineNo: price},
	}
	startMseClock(d, nt, now)
	t.st().Tranches[d.DealID] = append(t.st().Tranches[d.DealID], nt)
	if line, err := t.line(d.DealID, h.LineNo); err == nil {
		line.AcceptedQty += h.Qty
		line.RejectedQty -= h.Qty
	}
	return nt, nil
}

func ccShipReplacement(t *mtx, a []string) ([]byte, error) {
	d, h, err := heldArgs(t, a, model.OrgSupplier)
	if err != nil {
		return nil, err
	}
	if err := requireHeld(h); err != nil {
		return nil, err
	}
	qty, err := argInt(a[3], "qty", 1)
	if err != nil {
		return nil, err
	}
	if qty > h.Qty {
		return nil, apperr.Newf(apperr.ErrQtyMismatch, "replacement qty %d exceeds held qty %d", qty, h.Qty)
	}
	src, err := t.tranche(d.DealID, h.TrancheNo)
	if err != nil {
		return nil, err
	}
	price, err := unitPrice(t, d.DealID, src, h.LineNo)
	if err != nil {
		return nil, err
	}
	moved, err := mul(qty, price)
	if err != nil {
		return nil, err
	}
	newNo := int64(len(t.st().Tranches[d.DealID])) + 1
	t.st().Tranches[d.DealID] = append(t.st().Tranches[d.DealID], &mockTranche{
		Tranche:  model.Tranche{DealID: d.DealID, TrancheNo: newNo, Status: model.TranchePending, Lines: []model.TrancheLine{{LineNo: h.LineNo}}},
		Expected: map[int64]int64{h.LineNo: qty},
	})
	if qty == h.Qty {
		closeHeld(t, d, h, model.ExitReplacement)
	} else {
		h.Qty -= qty
		h.ValuePaise -= moved
		d.TotalHeldPaise -= moved
		if line, err := t.line(d.DealID, h.LineNo); err == nil {
			line.HeldQty -= qty
		}
	}
	recomputeDealStatus(t, d)
	t.touch(d.DealID)
	t.emit(model.EvReplacementShipped, d.DealID, map[string]any{"trancheNo": h.TrancheNo, "newTrancheNo": newNo, "lineNo": h.LineNo, "qty": qty})
	return nil, nil
}

func ccProposeCreditNote(t *mtx, a []string) ([]byte, error) {
	d, h, err := heldArgs(t, a, model.OrgBuyer, model.OrgSupplier)
	if err != nil {
		return nil, err
	}
	if err := requireHeld(h); err != nil {
		return nil, err
	}
	v, err := argInt(a[3], "valuePaise", 1)
	if err != nil {
		return nil, err
	}
	if v != h.ValuePaise {
		return nil, apperr.Newf(apperr.ErrBadRequest, "credit note must cover the full held value %d; use a price adjustment for partial value", h.ValuePaise)
	}
	h.CreditNote = &mockProposal{Amount: v, ProposedBy: t.id.Org, ProposedAt: t.now}
	t.touch(d.DealID)
	t.emit(model.EvCreditNoteProposed, d.DealID, map[string]any{"trancheNo": h.TrancheNo, "lineNo": h.LineNo, "valuePaise": v})
	return nil, nil
}

func ccSignCreditNote(t *mtx, a []string) ([]byte, error) {
	d, h, err := heldArgs(t, a, model.OrgBuyer, model.OrgSupplier)
	if err != nil {
		return nil, err
	}
	if err := requireHeld(h); err != nil {
		return nil, err
	}
	if h.CreditNote == nil {
		return nil, apperr.New(apperr.ErrInvalidTransition, "no credit note has been proposed")
	}
	if h.CreditNote.ProposedBy == t.id.Org {
		return nil, apperr.New(apperr.ErrForbiddenRole, "the counter-party must sign the credit note")
	}
	closeHeld(t, d, h, model.ExitCreditNote)
	recomputeDealStatus(t, d)
	t.touch(d.DealID)
	t.emit(model.EvCreditNoteSigned, d.DealID, map[string]any{"trancheNo": h.TrancheNo, "lineNo": h.LineNo, "valuePaise": h.CreditNote.Amount})
	return nil, nil
}

func ccProposePriceAdjustment(t *mtx, a []string) ([]byte, error) {
	d, h, err := heldArgs(t, a, model.OrgBuyer, model.OrgSupplier)
	if err != nil {
		return nil, err
	}
	if err := requireHeld(h); err != nil {
		return nil, err
	}
	p, err := argInt(a[3], "adjustedUnitPricePaise", 1)
	if err != nil {
		return nil, err
	}
	src, err := t.tranche(d.DealID, h.TrancheNo)
	if err != nil {
		return nil, err
	}
	orig, err := unitPrice(t, d.DealID, src, h.LineNo)
	if err != nil {
		return nil, err
	}
	if p >= orig {
		return nil, apperr.Newf(apperr.ErrBadRequest, "adjusted price must be below the original %d", orig)
	}
	h.PriceAdj = &mockProposal{Amount: p, ProposedBy: t.id.Org, ProposedAt: t.now}
	t.touch(d.DealID)
	t.emit(model.EvPriceAdjustmentProposed, d.DealID, map[string]any{"trancheNo": h.TrancheNo, "lineNo": h.LineNo, "adjustedUnitPricePaise": p})
	return nil, nil
}

func ccSignPriceAdjustment(t *mtx, a []string) ([]byte, error) {
	d, h, err := heldArgs(t, a, model.OrgBuyer, model.OrgSupplier)
	if err != nil {
		return nil, err
	}
	if err := requireHeld(h); err != nil {
		return nil, err
	}
	if h.PriceAdj == nil {
		return nil, apperr.New(apperr.ErrInvalidTransition, "no price adjustment has been proposed")
	}
	if h.PriceAdj.ProposedBy == t.id.Org {
		return nil, apperr.New(apperr.ErrForbiddenRole, "the counter-party must sign the price adjustment")
	}
	nt, err := newAcceptedTranche(t, d, h, h.PriceAdj.Amount)
	if err != nil {
		return nil, err
	}
	closeHeld(t, d, h, model.ExitPriceAdjustment)
	recomputeDealStatus(t, d)
	t.touch(d.DealID)
	t.emit(model.EvPriceAdjustmentSigned, d.DealID, map[string]any{"trancheNo": h.TrancheNo, "lineNo": h.LineNo, "adjustedUnitPricePaise": h.PriceAdj.Amount, "newTrancheNo": nt.TrancheNo})
	return nil, nil
}

// ---- objections & disputes ----

func ccRaiseObjection(t *mtx, a []string) ([]byte, error) {
	if err := t.requireOrg(model.OrgBuyer); err != nil {
		return nil, err
	}
	d, err := t.deal(a[0])
	if err != nil {
		return nil, err
	}
	if err := t.requireParty(d); err != nil {
		return nil, err
	}
	n, err := argInt(a[1], "trancheNo", 1)
	if err != nil {
		return nil, err
	}
	reason := strings.TrimSpace(a[2])
	if reason == "" {
		return nil, apperr.New(apperr.ErrBadRequest, "reason is required (MSMED Act §2(b): objection must be in writing)")
	}
	tr, err := t.tranche(d.DealID, n)
	if err != nil {
		return nil, err
	}
	if tr.Status != model.TrancheDelivered && tr.Status != model.TrancheAccepted {
		return nil, apperr.Newf(apperr.ErrInvalidTransition, "tranche %d is %s; objections apply to delivered goods", n, tr.Status)
	}
	if tr.DeliveredAt == nil || t.now.After(tr.DeliveredAt.Add(deemedAcceptanceAfter)) {
		return nil, apperr.Newf(apperr.ErrObjectionWindowClosed, "objection window (15 days from delivery) closed for tranche %d", n)
	}
	if tr.Obj != nil && tr.Obj.Open {
		return nil, apperr.Newf(apperr.ErrDuplicate, "tranche %d already has an open objection", n)
	}
	tr.Obj = &mockObjection{Reason: reason, RaisedAt: t.now, Open: true}
	t.touch(d.DealID)
	t.emit(model.EvObjectionRaised, d.DealID, map[string]any{"trancheNo": n, "reason": reason})
	return nil, nil
}

func ccResolveObjection(t *mtx, a []string) ([]byte, error) {
	if err := t.requireOrg(model.OrgBuyer, model.OrgSupplier); err != nil {
		return nil, err
	}
	d, err := t.deal(a[0])
	if err != nil {
		return nil, err
	}
	if err := t.requireParty(d); err != nil {
		return nil, err
	}
	n, err := argInt(a[1], "trancheNo", 1)
	if err != nil {
		return nil, err
	}
	tr, err := t.tranche(d.DealID, n)
	if err != nil {
		return nil, err
	}
	if tr.Obj == nil || !tr.Obj.Open {
		return nil, apperr.Newf(apperr.ErrInvalidTransition, "tranche %d has no open objection", n)
	}
	if (t.id.Org == model.OrgBuyer && tr.Obj.BuyerSigned) || (t.id.Org == model.OrgSupplier && tr.Obj.SupplierSigned) {
		return nil, apperr.Newf(apperr.ErrDuplicate, "%s already signed the resolution", t.id.Org)
	}
	if t.id.Org == model.OrgBuyer {
		tr.Obj.BuyerSigned = true
	} else {
		tr.Obj.SupplierSigned = true
	}
	t.touch(d.DealID)
	if !tr.Obj.BuyerSigned || !tr.Obj.SupplierSigned {
		return nil, nil // first co-signature recorded; resolution completes on the second
	}
	now := t.now
	tr.Obj.Open, tr.Obj.ResolvedAt = false, &now
	if tr.Status == model.TrancheAccepted {
		// §2(b)(i)(b): acceptance day becomes the day the objection was removed.
		startMseClock(d, tr, now)
	}
	t.emit(model.EvObjectionResolved, d.DealID, map[string]any{"trancheNo": n})
	if d.MSE.Applies && tr.MseClockStart != nil {
		t.emit(model.EvMseClockRestarted, d.DealID, map[string]any{"trancheNo": n, "mseClockStart": tr.MseClockStart, "mseDeadline": tr.MseDeadline})
	}
	return nil, nil
}

func ccRaiseDispute(t *mtx, a []string) ([]byte, error) {
	d, h, err := heldArgs(t, a, model.OrgBuyer, model.OrgSupplier)
	if err != nil {
		return nil, err
	}
	if err := requireHeld(h); err != nil {
		return nil, err
	}
	reason := strings.TrimSpace(a[3])
	if reason == "" {
		return nil, apperr.New(apperr.ErrBadRequest, "reason is required")
	}
	h.Dispute = &mockDispute{Reason: reason, RaisedBy: t.id.Org, RaisedAt: t.now}
	h.Exit, h.State = ptr(model.ExitDispute), model.DealDisputed
	recomputeDealStatus(t, d)
	t.touch(d.DealID)
	t.emit(model.EvDisputeRaised, d.DealID, map[string]any{"trancheNo": h.TrancheNo, "lineNo": h.LineNo, "reason": reason})
	return nil, nil
}

func resolveDispute(t *mtx, d *model.Deal, h *mockHeld, resolution string) error {
	if h.State != model.DealDisputed {
		return apperr.Newf(apperr.ErrInvalidTransition, "held portion %d/%d is not in dispute", h.TrancheNo, h.LineNo)
	}
	switch resolution {
	case model.ResolutionReleaseToSupplier:
		src, err := t.tranche(d.DealID, h.TrancheNo)
		if err != nil {
			return err
		}
		price, err := unitPrice(t, d.DealID, src, h.LineNo)
		if err != nil {
			return err
		}
		if _, err := newAcceptedTranche(t, d, h, price); err != nil {
			return err
		}
	case model.ResolutionRefundToBuyer:
	default:
		return apperr.New(apperr.ErrBadRequest, "resolution must be RELEASE_TO_SUPPLIER or REFUND_TO_BUYER")
	}
	closeHeld(t, d, h, model.ExitDispute)
	recomputeDealStatus(t, d)
	t.touch(d.DealID)
	t.emit(model.EvDisputeResolved, d.DealID, map[string]any{"trancheNo": h.TrancheNo, "lineNo": h.LineNo, "resolution": resolution})
	return nil
}

func ccResolveDispute(t *mtx, a []string) ([]byte, error) {
	d, h, err := heldArgs(t, a, model.OrgBank)
	if err != nil {
		return nil, err
	}
	if t.id.Role != model.RoleArbiter {
		return nil, apperr.New(apperr.ErrForbiddenRole, "dispute resolution requires role ARBITER")
	}
	return nil, resolveDispute(t, d, h, a[3])
}

func ccApplyDisputeTimeout(t *mtx, a []string) ([]byte, error) {
	d, h, err := heldArgs(t, a, model.OrgBuyer, model.OrgSupplier, model.OrgBank)
	if err != nil {
		return nil, err
	}
	if h.Dispute == nil || t.now.Before(h.Dispute.RaisedAt.Add(disputeTimeout)) {
		return nil, apperr.New(apperr.ErrInvalidTransition, "dispute timeout (30 days) has not elapsed")
	}
	// Default resolution: the recorded QC rejection stands.
	return nil, resolveDispute(t, d, h, model.ResolutionRefundToBuyer)
}

// ---- time rules ----

func ccTickDeadlines(t *mtx, a []string) ([]byte, error) {
	// The transaction timestamp is proposed by the client, so only the orgs that endorse the
	// deal may drive its clock (the real chaincode enforces the same rule).
	if err := t.requireOrg(model.OrgBank, model.OrgBuyer); err != nil {
		return nil, err
	}
	d, err := t.deal(a[0])
	if err != nil {
		return nil, err
	}
	if expirable(t, d) {
		expire(t, d)
		return nil, nil
	}
	deemed := map[int64]bool{}
	for _, tr := range t.st().Tranches[d.DealID] {
		if d.MSE.Applies && tr.Status == model.TrancheDelivered && tr.DeliveredAt != nil && (tr.Obj == nil || !tr.Obj.Open) &&
			!t.now.Before(acceptanceDay(tr).Add(deemedAcceptanceAfter)) {
			deemAccept(t, d, tr)
			emitAcceptance(t, d, tr, model.EvDeemedAcceptance)
			deemed[tr.TrancheNo] = true
		}
	}
	for _, tr := range t.st().Tranches[d.DealID] {
		if !deemed[tr.TrancheNo] && tr.MseDeadline != nil && !tr.Warned && unpaid(tr.Status) && tr.MseDeadline.Sub(t.now) <= mseWarnWindow {
			tr.Warned = true
			t.emit(model.EvMseDeadlineApproaching, d.DealID, map[string]any{"trancheNo": tr.TrancheNo, "mseDeadline": tr.MseDeadline, "daysLeft": daysLeft(*tr.MseDeadline, t.now)})
		}
	}
	if len(t.events) > 0 {
		recomputeDealStatus(t, d)
		t.touch(d.DealID)
	}
	return nil, nil
}

// deemAccept applies MSMED Act §2(b)(ii): no written objection within 15 days of delivery means the
// goods are deemed accepted on the day of actual delivery, and the payment clock runs from then.
func deemAccept(t *mtx, d *model.Deal, tr *mockTranche) {
	var accVal int64
	for i, l := range tr.Lines {
		price, _ := unitPrice(t, d.DealID, tr, l.LineNo)
		tr.Lines[i].AcceptedQty = l.DeliveredQty
		accVal += l.DeliveredQty * price
		if line, err := t.line(d.DealID, l.LineNo); err == nil {
			line.AcceptedQty += l.DeliveredQty
		}
	}
	start := acceptanceDay(tr)
	tr.Status = model.TrancheDeemedAccepted
	tr.DeemedAcceptance = true
	tr.AcceptedValuePaise = accVal
	tr.AcceptedAt = &start
	startMseClock(d, tr, start)
}

// acceptanceDay is the statutory day of (deemed) acceptance: the delivery day, or the day a written
// objection was removed (MSMED Act §2(b)).
func acceptanceDay(tr *mockTranche) time.Time {
	if tr.Obj != nil && tr.Obj.ResolvedAt != nil {
		return *tr.Obj.ResolvedAt
	}
	return *tr.DeliveredAt
}

func unpaid(status string) bool {
	return status != model.TrancheReleased && status != model.TrancheSettled && status != model.TrancheClosed
}

// daysLeft rounds up: 4 days 1 hour left is shown as 5 days.
func daysLeft(deadline, now time.Time) int64 {
	return int64(math.Ceil(deadline.Sub(now).Hours() / 24))
}

// recomputeDealStatus derives the deal status from its tranches and held portions.
func recomputeDealStatus(t *mtx, d *model.Deal) {
	if d.Status == model.DealExpired || d.Status == model.DealClosed {
		return
	}
	tranches := t.st().Tranches[d.DealID]
	if len(tranches) == 0 {
		return
	}
	openHeld, disputed := false, false
	for _, h := range t.st().Held[d.DealID] {
		switch h.State {
		case model.TrancheHeld:
			openHeld = true
		case model.DealDisputed:
			disputed = true
		}
	}
	has := map[string]bool{}
	for _, tr := range tranches {
		has[tr.Status] = true
	}
	switch {
	case disputed:
		d.Status = model.DealDisputed
	case has[model.TrancheDelivered]:
		d.Status = model.DealDelivered
	case openHeld || has[model.TranchePending]:
		d.Status = model.DealPartiallyAccepted
	case has[model.TrancheAccepted] || has[model.TrancheDeemedAccepted] || has[model.TrancheApproved]:
		d.Status = model.DealAccepted
	case has[model.TrancheReleased]:
		d.Status = model.DealReleased
	case has[model.TrancheSettled]:
		d.Status = model.DealSettled
	}
}

// ---- queries ----

func (tr *mockTranche) view() *model.Tranche {
	out := tr.Tranche
	out.Lines = append([]model.TrancheLine{}, tr.Lines...)
	out.Approvals = append([]model.Approval{}, tr.Approvals...)
	if tr.Obj != nil {
		out.Objection, _ = json.Marshal(tr.Obj)
	} else {
		out.Objection = json.RawMessage("null")
	}
	return &out
}

func ccGetDeal(t *mtx, a []string) ([]byte, error) {
	d, err := t.deal(a[0])
	if err != nil {
		return nil, err
	}
	extra := t.st().Extra[d.DealID]
	if len(extra) == 0 {
		return jsonOut(d)
	}
	base, err := json.Marshal(d)
	if err != nil {
		return nil, apperr.Wrap(apperr.ErrInternal, err, "marshal deal")
	}
	merged := map[string]json.RawMessage{}
	_ = json.Unmarshal(base, &merged)
	for k, v := range extra {
		merged[k] = v
	}
	return jsonOut(merged)
}

func ccGetTranche(t *mtx, a []string) ([]byte, error) {
	if _, err := t.deal(a[0]); err != nil {
		return nil, err
	}
	n, err := argInt(a[1], "trancheNo", 1)
	if err != nil {
		return nil, err
	}
	tr, err := t.tranche(a[0], n)
	if err != nil {
		return nil, err
	}
	return jsonOut(tr.view())
}

func ccListTranches(t *mtx, a []string) ([]byte, error) {
	if _, err := t.deal(a[0]); err != nil {
		return nil, err
	}
	out := []*model.Tranche{}
	for _, tr := range t.st().Tranches[a[0]] {
		out = append(out, tr.view())
	}
	return jsonOut(out)
}

func ccGetHeldPortions(t *mtx, a []string) ([]byte, error) {
	if _, err := t.deal(a[0]); err != nil {
		return nil, err
	}
	out := []model.HeldPortion{}
	for _, h := range t.st().Held[a[0]] {
		out = append(out, h.HeldPortion)
	}
	return jsonOut(out)
}

func ccGetDealHistory(t *mtx, a []string) ([]byte, error) {
	if _, err := t.deal(a[0]); err != nil {
		return nil, err
	}
	out := append([]model.HistoryEntry{}, t.st().History[a[0]]...)
	return jsonOut(out)
}

func ccQueryDealsByStatus(t *mtx, a []string) ([]byte, error) {
	out := []*model.Deal{}
	for _, d := range t.st().Deals {
		if d.Status == a[0] {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DealID < out[j].DealID })
	return jsonOut(out)
}

func ccQueryMseDealsNearDeadline(t *mtx, a []string) ([]byte, error) {
	within, err := argInt(a[0], "withinDays", 0)
	if err != nil {
		return nil, err
	}
	limit := t.now.Add(time.Duration(within) * day)
	out := []*model.Tranche{}
	for _, d := range t.st().Deals {
		for _, tr := range t.st().Tranches[d.DealID] {
			if tr.MseDeadline != nil && unpaid(tr.Status) && !tr.MseDeadline.After(limit) {
				out = append(out, tr.view())
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MseDeadline.Before(*out[j].MseDeadline) })
	return jsonOut(out)
}

func ccGetMseClock(t *mtx, a []string) ([]byte, error) {
	d, err := t.deal(a[0])
	if err != nil {
		return nil, err
	}
	if !d.MSE.Applies {
		return nil, apperr.Newf(apperr.ErrMseNotApplicable, "deal %s is not covered by the MSMED Act", d.DealID)
	}
	n, err := argInt(a[1], "trancheNo", 1)
	if err != nil {
		return nil, err
	}
	tr, err := t.tranche(d.DealID, n)
	if err != nil {
		return nil, err
	}
	c := model.MseClock{DealID: d.DealID, TrancheNo: n, TermDays: d.MSE.TermDays, MseClockStart: tr.MseClockStart, MseDeadline: tr.MseDeadline,
		Paused: tr.Obj != nil && tr.Obj.Open}
	if tr.MseDeadline != nil {
		c.DaysLeft = daysLeft(*tr.MseDeadline, t.now)
	}
	return jsonOut(c)
}
