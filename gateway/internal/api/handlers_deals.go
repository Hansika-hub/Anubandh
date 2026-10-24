package api

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/Hansika-hub/anubandh/gateway/internal/apperr"
	"github.com/Hansika-hub/anubandh/gateway/internal/fabric"
	"github.com/Hansika-hub/anubandh/gateway/internal/model"
	"github.com/Hansika-hub/anubandh/gateway/internal/store"
)

var (
	dealIDRe  = regexp.MustCompile(`^DL-(\d{8})-\d{4}$`)
	partyIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,63}$`)
	udyamRe   = regexp.MustCompile(`^UDYAM-[A-Z]{2}-\d{2}-\d{7}$`)
)

// defaultApprovalPolicy is BUILD.md §4.2's example: Plant Head always, plus CFO from ₹50 lakh.
var defaultApprovalPolicy = []model.PolicyBand{
	{MinPaise: 0, RequiredRoles: []model.Role{model.RolePlantHead}},
	{MinPaise: 500000000, RequiredRoles: []model.Role{model.RolePlantHead, model.RoleCFO}},
}

func errBadRequest(msg string) error { return apperr.New(apperr.ErrBadRequest, msg) }
func errNotFound(msg string) error   { return apperr.New(apperr.ErrNotFound, msg) }

func pathDealID(r *http.Request) (string, error) {
	id := chi.URLParam(r, "dealId")
	if !dealIDRe.MatchString(id) {
		return "", errBadRequest("dealId must match DL-YYYYMMDD-NNNN")
	}
	return id, nil
}

func pathInt(r *http.Request, name string) (int64, error) {
	v, err := strconv.ParseInt(chi.URLParam(r, name), 10, 64)
	if err != nil || v < 1 || v > 100000 {
		return 0, apperr.Newf(apperr.ErrBadRequest, "%s must be a positive integer", name)
	}
	return v, nil
}

// chainCtx detaches a chain write from the HTTP request: if the client disconnects mid-commit
// the transaction still completes and is reconciled, instead of ending in an unknown state.
func chainCtx(r *http.Request, id fabric.Identity) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 60*time.Second)
	return fabric.WithIdentity(ctx, id), cancel
}

// checkParty stops a buyer/supplier user from acting on another party's deal.
func (h *Handlers) checkParty(ctx context.Context, id fabric.Identity, dealID string) error {
	buyer, supplier, err := store.DealParties(ctx, h.store.Pool, dealID)
	if errors.Is(err, store.ErrNotFound) {
		b, cerr := h.chain.Evaluate(fabric.WithIdentity(ctx, h.reader), "GetDeal", dealID)
		if cerr != nil {
			return cerr
		}
		var d model.Deal
		if err := json.Unmarshal(b, &d); err != nil {
			return apperr.Wrap(apperr.ErrInternal, err, "decode deal")
		}
		buyer, supplier, err = d.BuyerID, d.SupplierID, nil
	}
	if err != nil {
		return err
	}
	if id.PartyID == "" {
		return nil
	}
	switch id.Org {
	case model.OrgBuyer:
		if buyer != id.PartyID {
			return apperr.Newf(apperr.ErrForbiddenRole, "%s is not the buyer on %s", id.PartyID, dealID)
		}
	case model.OrgSupplier:
		if supplier != id.PartyID {
			return apperr.Newf(apperr.ErrForbiddenRole, "%s is not the supplier on %s", id.PartyID, dealID)
		}
	}
	return nil
}

// reconcile refreshes the read model from the ledger so the caller reads its own write.
func (h *Handlers) reconcile(ctx context.Context, dealID string) {
	if h.proj == nil {
		return
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := h.proj.Reconcile(rctx, dealID); err != nil {
		h.log.Warn("post-write reconcile failed (listener will catch up)", "dealId", dealID, "err", err)
	}
}

// write runs one chain transaction on behalf of the caller and returns {txId, ...}.
func (h *Handlers) write(w http.ResponseWriter, r *http.Request, dealID string, orgs []model.Org, req fabric.SubmitRequest, extra map[string]any) {
	id, err := requireUser(principalFrom(r.Context()), orgs...)
	if err != nil {
		writeErr(w, h.log, err)
		return
	}
	if err := h.checkParty(r.Context(), id, dealID); err != nil {
		writeErr(w, h.log, err)
		return
	}
	ctx, cancel := chainCtx(r, id)
	defer cancel()
	res, err := h.chain.Submit(ctx, req)
	if err != nil {
		writeErr(w, h.log, err)
		return
	}
	h.log.Info("chain write", "fn", req.Fn, "dealId", dealID, "txId", res.TxID, "user", id.UserID, "attempts", res.Attempts)
	h.reconcile(ctx, dealID)
	out := map[string]any{"txId": res.TxID, "dealId": dealID}
	for k, v := range extra {
		out[k] = v
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- POST /deals ----

type createLine struct {
	LineNo         int64  `json:"lineNo"`
	SKU            string `json:"sku"`
	OrderedQty     int64  `json:"orderedQty"`
	UnitPricePaise int64  `json:"unitPricePaise"`
}

type createDealBody struct {
	DealID           string             `json:"dealId"`
	PONumber         string             `json:"poNumber"`
	BuyerID          string             `json:"buyerId"`
	SupplierID       string             `json:"supplierId"`
	Currency         string             `json:"currency"`
	LienAmountPaise  int64              `json:"lienAmountPaise"`
	MSE              *model.MSE         `json:"mse"`
	ApprovalPolicy   []model.PolicyBand `json:"approvalPolicy"`
	ToleranceBps     int64              `json:"toleranceBps"`
	DeliveryDeadline *time.Time         `json:"deliveryDeadline"`
	Lines            []createLine       `json:"lines"`
}

// validateDeal normalises and checks a create-deal body. It returns the PO value in paise.
func validateDeal(b *createDealBody, now time.Time) (int64, error) {
	m := dealIDRe.FindStringSubmatch(b.DealID)
	if m == nil {
		return 0, errBadRequest("dealId must match DL-YYYYMMDD-NNNN")
	}
	if _, err := time.Parse("20060102", m[1]); err != nil {
		return 0, errBadRequest("dealId date part is not a valid YYYYMMDD date")
	}
	b.PONumber = strings.TrimSpace(b.PONumber)
	if !partyIDRe.MatchString(b.PONumber) {
		return 0, errBadRequest("poNumber is required (letters, digits, . _ : / -; max 64)")
	}
	if !partyIDRe.MatchString(b.BuyerID) || !partyIDRe.MatchString(b.SupplierID) {
		return 0, errBadRequest("buyerId and supplierId are required (letters, digits, . _ : / -; max 64)")
	}
	if b.BuyerID == b.SupplierID {
		return 0, errBadRequest("buyer and supplier must differ")
	}
	switch b.Currency {
	case "":
		b.Currency = "INR"
	case "INR":
	default:
		return 0, errBadRequest("currency must be INR")
	}
	if len(b.Lines) == 0 || len(b.Lines) > 500 {
		return 0, errBadRequest("lines must hold 1 to 500 PO lines")
	}
	var sum int64
	for i, l := range b.Lines {
		switch {
		case l.LineNo != int64(i+1):
			return 0, apperr.Newf(apperr.ErrBadRequest, "lines[%d].lineNo must be %d (sequential from 1)", i, i+1)
		case strings.TrimSpace(l.SKU) == "" || len(l.SKU) > 64:
			return 0, apperr.Newf(apperr.ErrBadRequest, "lines[%d].sku is required (max 64)", i)
		case l.OrderedQty <= 0:
			return 0, apperr.Newf(apperr.ErrBadRequest, "lines[%d].orderedQty must be > 0", i)
		case l.UnitPricePaise <= 0:
			return 0, apperr.Newf(apperr.ErrBadRequest, "lines[%d].unitPricePaise must be > 0 (int64 paise)", i)
		case l.OrderedQty > math.MaxInt64/l.UnitPricePaise:
			return 0, apperr.Newf(apperr.ErrBadRequest, "lines[%d] value overflows int64 paise", i)
		}
		v := l.OrderedQty * l.UnitPricePaise
		if sum > math.MaxInt64-v {
			return 0, errBadRequest("PO value overflows int64 paise")
		}
		sum += v
	}
	if b.LienAmountPaise == 0 {
		b.LienAmountPaise = sum
	}
	if b.LienAmountPaise < sum {
		return 0, apperr.Newf(apperr.ErrBadRequest, "lienAmountPaise %d must cover the PO value %d", b.LienAmountPaise, sum)
	}
	if b.MSE == nil {
		b.MSE = &model.MSE{Category: model.MseNone}
	}
	switch b.MSE.Category {
	case "":
		b.MSE.Category = model.MseNone
	case model.MseMicro, model.MseSmall, model.MseMedium, model.MseNone:
	default:
		return 0, errBadRequest("mse.category must be MICRO, SMALL, MEDIUM or NONE")
	}
	if b.MSE.Applies {
		// MSMED Act 2006 §2(n): only micro and small enterprises are "suppliers" for §15-§16.
		if b.MSE.Category != model.MseMicro && b.MSE.Category != model.MseSmall {
			return 0, apperr.New(apperr.ErrMseNotApplicable, "MSMED Act payment terms apply only to MICRO and SMALL suppliers")
		}
		if b.MSE.TermDays != 15 && b.MSE.TermDays != 45 {
			return 0, errBadRequest("mse.termDays must be 15 (no written agreement) or 45 (written agreement, the statutory maximum)")
		}
		if !udyamRe.MatchString(b.MSE.UdyamNo) {
			return 0, errBadRequest("mse.udyamNo must look like UDYAM-XX-00-0000000")
		}
	} else if b.MSE.TermDays != 0 && b.MSE.TermDays != 15 && b.MSE.TermDays != 45 {
		return 0, errBadRequest("mse.termDays must be 15 or 45")
	}
	if len(b.ApprovalPolicy) == 0 {
		b.ApprovalPolicy = defaultApprovalPolicy
	}
	if len(b.ApprovalPolicy) > 10 {
		return 0, errBadRequest("approvalPolicy may have at most 10 bands")
	}
	seenMin := map[int64]bool{}
	for i, band := range b.ApprovalPolicy {
		if band.MinPaise < 0 || seenMin[band.MinPaise] {
			return 0, apperr.Newf(apperr.ErrBadRequest, "approvalPolicy[%d].minPaise must be >= 0 and unique", i)
		}
		seenMin[band.MinPaise] = true
		if len(band.RequiredRoles) == 0 {
			return 0, apperr.Newf(apperr.ErrBadRequest, "approvalPolicy[%d].requiredRoles must not be empty", i)
		}
		seenRole := map[model.Role]bool{}
		for _, role := range band.RequiredRoles {
			if !role.IsBuyerApprover() || seenRole[role] {
				return 0, apperr.Newf(apperr.ErrBadRequest, "approvalPolicy[%d]: %q is not a valid, unique buyer approver role", i, role)
			}
			seenRole[role] = true
		}
	}
	if !seenMin[0] {
		return 0, errBadRequest("approvalPolicy needs a band with minPaise 0 so every amount has approvers")
	}
	sort.Slice(b.ApprovalPolicy, func(i, j int) bool { return b.ApprovalPolicy[i].MinPaise < b.ApprovalPolicy[j].MinPaise })
	if b.ToleranceBps < 0 || b.ToleranceBps > 10000 {
		return 0, errBadRequest("toleranceBps must be within 0..10000")
	}
	if b.DeliveryDeadline == nil {
		return 0, errBadRequest("deliveryDeadline is required (RFC 3339)")
	}
	dd := b.DeliveryDeadline.UTC()
	b.DeliveryDeadline = &dd
	if !dd.After(now) {
		return 0, errBadRequest("deliveryDeadline must be in the future")
	}
	return sum, nil
}

// buildDealJSON is the chaincode's dealJSON: the caller's object with lines removed and every
// contract field normalised. Unknown fields (e.g. a future mse.writtenAgreement) pass through.
func buildDealJSON(raw map[string]json.RawMessage, b *createDealBody) ([]byte, error) {
	out := map[string]any{}
	for k, v := range raw {
		out[k] = v
	}
	delete(out, "lines")
	delete(out, "createdAt")
	delete(out, "updatedAt")
	mse := map[string]any{}
	if rawMSE, ok := raw["mse"]; ok {
		_ = json.Unmarshal(rawMSE, &mse)
	}
	mse["category"], mse["termDays"], mse["applies"] = b.MSE.Category, b.MSE.TermDays, b.MSE.Applies
	if b.MSE.UdyamNo != "" {
		mse["udyamNo"] = b.MSE.UdyamNo
	}
	out["dealId"], out["poNumber"], out["buyerId"], out["supplierId"] = b.DealID, b.PONumber, b.BuyerID, b.SupplierID
	out["status"], out["currency"] = model.DealCreated, b.Currency
	out["lienAmountPaise"], out["lienRef"] = b.LienAmountPaise, nil
	out["totalReleasedPaise"], out["totalHeldPaise"] = 0, 0
	out["mse"], out["approvalPolicy"], out["toleranceBps"] = mse, b.ApprovalPolicy, b.ToleranceBps
	out["deliveryDeadline"] = b.DeliveryDeadline.Format(time.RFC3339)
	out["invoice"] = model.Invoice{}
	return json.Marshal(out)
}

func (h *Handlers) createDeal(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	id, err := requireUser(p, model.OrgBuyer)
	if err != nil {
		writeErr(w, h.log, err)
		return
	}
	var raw map[string]json.RawMessage
	if err := decode(r, &raw); err != nil {
		writeErr(w, h.log, err)
		return
	}
	if raw == nil {
		writeErr(w, h.log, errBadRequest("request body must be a JSON object"))
		return
	}
	rawBytes, _ := json.Marshal(raw)
	var b createDealBody
	if err := json.Unmarshal(rawBytes, &b); err != nil {
		writeErr(w, h.log, apperr.Wrap(apperr.ErrBadRequest, err, "invalid deal body: "+err.Error()))
		return
	}
	if b.BuyerID == "" {
		b.BuyerID = id.PartyID
	}
	if id.PartyID != "" && b.BuyerID != id.PartyID {
		writeErr(w, h.log, apperr.Newf(apperr.ErrForbiddenRole, "%s cannot create deals for buyer %s", id.PartyID, b.BuyerID))
		return
	}
	if _, err := validateDeal(&b, h.clock.Now()); err != nil {
		writeErr(w, h.log, err)
		return
	}
	dealJSON, err := buildDealJSON(raw, &b)
	if err != nil {
		writeErr(w, h.log, apperr.Wrap(apperr.ErrInternal, err, "encode deal"))
		return
	}
	lines := make([]model.Line, len(b.Lines))
	for i, l := range b.Lines {
		lines[i] = model.Line{DealID: b.DealID, LineNo: l.LineNo, SKU: strings.TrimSpace(l.SKU), OrderedQty: l.OrderedQty, UnitPricePaise: l.UnitPricePaise}
	}
	linesJSON, _ := json.Marshal(lines)

	ctx, cancel := chainCtx(r, id)
	defer cancel()
	// Line prices travel in the transient map: they reach the private data collection, never the
	// channel ledger or the transaction payload.
	res, err := h.chain.Submit(ctx, fabric.SubmitRequest{Fn: "CreateDeal", Args: []string{string(dealJSON)}, Transient: map[string][]byte{"lines": linesJSON}})
	if err != nil {
		writeErr(w, h.log, err)
		return
	}
	now := h.clock.Now()
	err = h.store.InTx(ctx, func(tx pgx.Tx) error {
		if err := store.LockDeal(ctx, tx, b.DealID); err != nil {
			return err
		}
		if err := store.EnsureDeal(ctx, tx, model.Deal{DealID: b.DealID, PONumber: b.PONumber, BuyerID: b.BuyerID, SupplierID: b.SupplierID,
			Status: model.DealCreated, LienAmountPaise: b.LienAmountPaise, MSE: *b.MSE, DeliveryDeadline: b.DeliveryDeadline, CreatedAt: &now}); err != nil {
			return err
		}
		return store.UpsertLines(ctx, tx, lines)
	})
	if err != nil {
		h.log.Error("deal created on chain but read-model write failed", "dealId", b.DealID, "txId", res.TxID, "err", err)
	}
	h.log.Info("deal created", "dealId", b.DealID, "txId", res.TxID, "user", id.UserID, "lienAmountPaise", b.LienAmountPaise)
	h.reconcile(ctx, b.DealID)
	writeJSON(w, http.StatusCreated, map[string]any{"dealId": b.DealID, "txId": res.TxID, "status": model.DealCreated, "lienAmountPaise": b.LienAmountPaise})
}

// ---- GET /deals ----

func (h *Handlers) listDeals(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.DealFilter{Status: strings.ToUpper(q.Get("status")), BuyerID: q.Get("buyerId"), SupplierID: q.Get("supplierId")}
	var err error
	if f.MSE, err = queryBool(r, "mse"); err != nil {
		writeErr(w, h.log, err)
		return
	}
	if f.Limit, err = queryInt(r, "limit", 50, 1, 500); err != nil {
		writeErr(w, h.log, err)
		return
	}
	if f.Offset, err = queryInt(r, "offset", 0, 0, math.MaxInt32); err != nil {
		writeErr(w, h.log, err)
		return
	}
	items, total, err := store.ListDeals(r.Context(), h.store.Pool, f)
	if err != nil {
		writeErr(w, h.log, err)
		return
	}
	writeList(w, items, total)
}

// ---- GET /deals/{dealId} ----

// DealDetail is GET /deals/{dealId}.
type DealDetail struct {
	store.DealView
	ApprovalPolicy []model.PolicyBand  `json:"approvalPolicy,omitempty"`
	ToleranceBps   *int64              `json:"toleranceBps,omitempty"`
	Lines          []store.LineView    `json:"lines"`
	Tranches       []store.TrancheView `json:"tranches"`
	HeldPortions   []store.HeldView    `json:"heldPortions"`
}

func (h *Handlers) getDeal(w http.ResponseWriter, r *http.Request) {
	dealID, err := pathDealID(r)
	if err != nil {
		writeErr(w, h.log, err)
		return
	}
	ctx := r.Context()
	d, err := store.GetDeal(ctx, h.store.Pool, dealID)
	if errors.Is(err, store.ErrNotFound) {
		// Not projected yet (or read model rebuilt): pull it from the ledger once.
		h.reconcile(ctx, dealID)
		d, err = store.GetDeal(ctx, h.store.Pool, dealID)
	}
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, h.log, apperr.Newf(apperr.ErrNotFound, "deal %s not found", dealID))
		return
	}
	if err != nil {
		writeErr(w, h.log, err)
		return
	}
	out := DealDetail{DealView: d}
	if out.Lines, err = store.GetLines(ctx, h.store.Pool, dealID); err != nil {
		writeErr(w, h.log, err)
		return
	}
	if out.Tranches, err = store.GetTranches(ctx, h.store.Pool, dealID); err != nil {
		writeErr(w, h.log, err)
		return
	}
	if out.HeldPortions, err = store.GetHeld(ctx, h.store.Pool, dealID); err != nil {
		writeErr(w, h.log, err)
		return
	}
	if principalFrom(ctx).Org == model.OrgAuditor {
		for i := range out.Lines {
			out.Lines[i].UnitPricePaise = nil // line prices are private to buyer, supplier and bank
		}
	}
	h.enrich(ctx, &out)
	writeJSON(w, http.StatusOK, out)
}

// enrich adds ledger-only fields (approval policy, tolerance, per-tranche lines, objection,
// held exit) when the chain answers within 3 s; the read-model response stands on its own.
func (h *Handlers) enrich(ctx context.Context, out *DealDetail) {
	cctx, cancel := context.WithTimeout(fabric.WithIdentity(ctx, h.reader), 3*time.Second)
	defer cancel()
	if b, err := h.chain.Evaluate(cctx, "GetDeal", out.DealID); err == nil {
		var d model.Deal
		if json.Unmarshal(b, &d) == nil {
			out.ApprovalPolicy = d.ApprovalPolicy
			tb := d.ToleranceBps
			out.ToleranceBps = &tb
		}
	}
	b, err := h.chain.Evaluate(cctx, "ListTranches", out.DealID)
	if err != nil {
		return
	}
	var ts []model.Tranche
	if json.Unmarshal(b, &ts) != nil {
		return
	}
	byNo := map[int64]model.Tranche{}
	for _, t := range ts {
		byNo[t.TrancheNo] = t
	}
	for i := range out.Tranches {
		if t, ok := byNo[out.Tranches[i].TrancheNo]; ok {
			out.Tranches[i].Lines = t.Lines
			out.Tranches[i].HeldExit = t.HeldExit
			out.Tranches[i].ReleaseIdemKey = t.ReleaseIdemKey
			if len(t.Objection) > 0 && string(t.Objection) != "null" {
				out.Tranches[i].Objection = t.Objection
			}
		}
	}
}

// ---- GET /deals/{dealId}/history ----

func (h *Handlers) dealHistory(w http.ResponseWriter, r *http.Request) {
	dealID, err := pathDealID(r)
	if err != nil {
		writeErr(w, h.log, err)
		return
	}
	ctx, cancel := context.WithTimeout(fabric.WithIdentity(r.Context(), h.reader), 10*time.Second)
	defer cancel()
	b, err := h.chain.Evaluate(ctx, "GetDealHistory", dealID)
	if err != nil {
		writeErr(w, h.log, err)
		return
	}
	var items []json.RawMessage
	if err := json.Unmarshal(b, &items); err != nil {
		writeErr(w, h.log, apperr.Wrap(apperr.ErrInternal, err, "decode ledger history"))
		return
	}
	writeList(w, items, len(items))
}

// ---- POST /deals/{dealId}/lien ----

func (h *Handlers) confirmLien(w http.ResponseWriter, r *http.Request) {
	dealID, err := pathDealID(r)
	if err != nil {
		writeErr(w, h.log, err)
		return
	}
	var body struct {
		LienRef     string `json:"lienRef"`
		AmountPaise int64  `json:"amountPaise"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, h.log, err)
		return
	}
	body.LienRef = strings.TrimSpace(body.LienRef)
	if !partyIDRe.MatchString(body.LienRef) || body.AmountPaise <= 0 {
		writeErr(w, h.log, errBadRequest("lienRef (max 64) and amountPaise > 0 are required"))
		return
	}
	h.write(w, r, dealID, []model.Org{model.OrgBank},
		fabric.SubmitRequest{Fn: "ConfirmLien", Args: []string{dealID, body.LienRef, strconv.FormatInt(body.AmountPaise, 10)}},
		map[string]any{"lienRef": body.LienRef})
}
