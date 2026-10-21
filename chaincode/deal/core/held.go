package core

import "time"

// DisputeTimeout and DisputeDefault are DECISION D-2 (BUILD has no field for the "deal's default
// rule"): after 30 days an unresolved dispute is refunded to the buyer.
const DisputeTimeout = 30 * day
const DisputeDefault = ResolutionRefund

func (e *Engine) heldCtx(dealID string, n, l int64, orgs ...Org) (Caller, *Deal, *HeldPortion, error) {
	c, err := e.requireOrg(orgs...)
	if err != nil {
		return c, nil, nil, err
	}
	d, err := e.loadDeal(dealID)
	if err != nil {
		return c, nil, nil, err
	}
	if err := requireParty(c, d); err != nil {
		return c, nil, nil, err
	}
	h, err := e.loadHeld(dealID, n, l)
	return c, d, h, err
}

// newAcceptedTranche turns a closed held portion into a fresh, releasable tranche.
func (e *Engine) newAcceptedTranche(d *Deal, h *HeldPortion, adjPrice int64) (*Tranche, error) {
	n, err := e.nextTrancheNo(d.DealID)
	if err != nil {
		return nil, err
	}
	src, err := e.loadTranche(d.DealID, h.TrancheNo)
	if err != nil {
		return nil, err
	}
	ln, err := e.loadLine(d.DealID, h.LineNo)
	if err != nil {
		return nil, err
	}
	price := ln.UnitPricePaise
	tl := TrancheLine{LineNo: h.LineNo, DeliveredQty: h.Qty, AcceptedQty: h.Qty}
	if adjPrice > 0 {
		price, tl.AdjustedUnitPricePaise = adjPrice, adjPrice
	}
	v, err := mul(h.Qty, price)
	if err != nil {
		return nil, err
	}
	now := e.now()
	tr := &Tranche{DealID: d.DealID, TrancheNo: n, Status: TrAccepted, Lines: []TrancheLine{tl}, AcceptedValuePaise: v,
		DeliveredAt: src.DeliveredAt, AcceptedAt: tp(now), Approvals: []Approval{}, Replaces: &Ref{h.TrancheNo, h.LineNo}}
	e.startClock(d, tr, now)
	ln.AcceptedQty += h.Qty
	ln.HeldQty -= h.Qty
	if err := e.saveLine(ln); err != nil {
		return nil, err
	}
	d.TotalHeldPaise -= h.ValuePaise
	return tr, e.saveTranche(tr)
}

func (e *Engine) closeHeld(d *Deal, h *HeldPortion, exit HeldExit) error {
	ln, err := e.loadLine(d.DealID, h.LineNo)
	if err != nil {
		return err
	}
	ln.HeldQty -= h.Qty
	if err := e.saveLine(ln); err != nil {
		return err
	}
	d.TotalHeldPaise -= h.ValuePaise
	x := exit
	h.Exit, h.State, h.Proposal = &x, HeldStateClosed, nil
	return e.saveHeld(h)
}

// ShipReplacement — Org SUPPLIER. Whole held quantity only (partial replacement: raise a
// price adjustment or credit note instead). Creates the next tranche as PENDING.
func (e *Engine) ShipReplacement(dealID string, trancheNo, lineNo, qty int64) error {
	_, d, h, err := e.heldCtx(dealID, trancheNo, lineNo, OrgSupplier)
	if err != nil {
		return err
	}
	if h.State != HeldStateHeld {
		return fail(ErrInvalidTransition, "held portion is %s", h.State)
	}
	if qty != h.Qty {
		return fail(ErrQtyMismatch, "replacement must cover the full held quantity %d", h.Qty)
	}
	n, err := e.nextTrancheNo(dealID)
	if err != nil {
		return err
	}
	tr := &Tranche{DealID: dealID, TrancheNo: n, Status: TrPending, Lines: []TrancheLine{{LineNo: lineNo, DeliveredQty: qty}},
		Approvals: []Approval{}, Replaces: &Ref{trancheNo, lineNo}}
	if err := e.saveTranche(tr); err != nil {
		return err
	}
	x := ExitReplacement
	h.Exit, h.State, h.Proposal, h.ReplacementNo = &x, HeldStateReplacing, nil, n
	if err := e.saveHeld(h); err != nil {
		return err
	}
	if err := e.refreshStatus(d); err != nil {
		return err
	}
	e.emit("ReplacementShipped", dealID, map[string]any{"trancheNo": trancheNo, "newTrancheNo": n, "lineNo": lineNo, "qty": qty})
	return nil
}

func (e *Engine) propose(dealID string, n, l int64, p Proposal) (*Deal, *HeldPortion, error) {
	c, d, h, err := e.heldCtx(dealID, n, l, OrgBuyer, OrgSupplier)
	if err != nil {
		return nil, nil, err
	}
	if h.State != HeldStateHeld {
		return nil, nil, fail(ErrInvalidTransition, "held portion is %s", h.State)
	}
	p.ProposedBy = c.Org
	x := p.Kind
	h.Exit, h.Proposal = &x, &p
	return d, h, e.saveHeld(h)
}

// countersign checks a pending proposal of `kind` exists and the caller is the OTHER party.
func (e *Engine) countersign(dealID string, n, l int64, kind HeldExit) (*Deal, *HeldPortion, error) {
	c, d, h, err := e.heldCtx(dealID, n, l, OrgBuyer, OrgSupplier)
	if err != nil {
		return nil, nil, err
	}
	if h.State != HeldStateHeld || h.Proposal == nil || h.Proposal.Kind != kind {
		return nil, nil, fail(ErrInvalidTransition, "no pending %s proposal", kind)
	}
	if h.Proposal.ProposedBy == c.Org {
		return nil, nil, fail(ErrForbiddenRole, "the other party must sign")
	}
	return d, h, nil
}

func (e *Engine) ProposeCreditNote(dealID string, trancheNo, lineNo, valuePaise int64) error {
	if _, err := e.requireOrg(OrgBuyer, OrgSupplier); err != nil {
		return err
	}
	_, h, err := e.loadHeldOnly(dealID, trancheNo, lineNo)
	if err != nil {
		return err
	}
	if valuePaise != h.ValuePaise {
		return fail(ErrQtyMismatch, "credit note must equal the held value %d (partial credit = price adjustment)", h.ValuePaise)
	}
	if _, _, err := e.propose(dealID, trancheNo, lineNo, Proposal{Kind: ExitCreditNote, ValuePaise: valuePaise}); err != nil {
		return err
	}
	e.emit("CreditNoteProposed", dealID, map[string]any{"trancheNo": trancheNo, "lineNo": lineNo, "valuePaise": valuePaise})
	return nil
}

func (e *Engine) loadHeldOnly(dealID string, n, l int64) (*Deal, *HeldPortion, error) {
	d, err := e.loadDeal(dealID)
	if err != nil {
		return nil, nil, err
	}
	h, err := e.loadHeld(dealID, n, l)
	return d, h, err
}

func (e *Engine) SignCreditNote(dealID string, trancheNo, lineNo int64) error {
	d, h, err := e.countersign(dealID, trancheNo, lineNo, ExitCreditNote)
	if err != nil {
		return err
	}
	v := h.Proposal.ValuePaise
	if err := e.closeHeld(d, h, ExitCreditNote); err != nil {
		return err
	}
	if err := e.refreshStatus(d); err != nil {
		return err
	}
	e.emit("CreditNoteSigned", dealID, map[string]any{"trancheNo": trancheNo, "lineNo": lineNo, "valuePaise": v})
	return nil
}

func (e *Engine) ProposePriceAdjustment(dealID string, trancheNo, lineNo, adjustedUnitPricePaise int64) error {
	if _, err := e.requireOrg(OrgBuyer, OrgSupplier); err != nil {
		return err
	}
	_, h, err := e.loadHeldOnly(dealID, trancheNo, lineNo)
	if err != nil {
		return err
	}
	ln, err := e.loadLine(dealID, h.LineNo)
	if err != nil {
		return err
	}
	if adjustedUnitPricePaise < 1 || adjustedUnitPricePaise >= ln.UnitPricePaise {
		return fail(ErrBadRequest, "adjusted price must be >0 and below the agreed price")
	}
	if _, _, err := e.propose(dealID, trancheNo, lineNo, Proposal{Kind: ExitPriceAdjustment, AdjustedUnitPricePaise: adjustedUnitPricePaise}); err != nil {
		return err
	}
	e.emit("PriceAdjustmentProposed", dealID, map[string]any{"trancheNo": trancheNo, "lineNo": lineNo, "adjustedUnitPricePaise": adjustedUnitPricePaise})
	return nil
}

func (e *Engine) SignPriceAdjustment(dealID string, trancheNo, lineNo int64) error {
	d, h, err := e.countersign(dealID, trancheNo, lineNo, ExitPriceAdjustment)
	if err != nil {
		return err
	}
	adj := h.Proposal.AdjustedUnitPricePaise
	tr, err := e.newAcceptedTranche(d, h, adj) // adjusts line + totalHeld
	if err != nil {
		return err
	}
	x := ExitPriceAdjustment
	h.Exit, h.State, h.Proposal = &x, HeldStateClosed, nil
	if err := e.saveHeld(h); err != nil {
		return err
	}
	if err := e.refreshStatus(d); err != nil {
		return err
	}
	e.emit("PriceAdjustmentSigned", dealID, map[string]any{"trancheNo": trancheNo, "lineNo": lineNo, "adjustedUnitPricePaise": adj})
	e.emitAcceptance(d, tr, false)
	return nil
}

// RaiseDispute — BUYER or SUPPLIER, on a HELD portion.
func (e *Engine) RaiseDispute(dealID string, trancheNo, lineNo int64, reason string) error {
	_, d, h, err := e.heldCtx(dealID, trancheNo, lineNo, OrgBuyer, OrgSupplier)
	if err != nil {
		return err
	}
	if h.State != HeldStateHeld {
		return fail(ErrInvalidTransition, "only a HELD portion can be disputed, it is %s", h.State)
	}
	if trim(reason) == "" {
		return fail(ErrBadRequest, "reason required")
	}
	x := ExitDispute
	h.Exit, h.State, h.Proposal, h.DisputeRaisedAt = &x, HeldStateDisputed, nil, tp(e.now())
	if err := e.saveHeld(h); err != nil {
		return err
	}
	if err := e.refreshStatus(d); err != nil {
		return err
	}
	e.emit("DisputeRaised", dealID, map[string]any{"trancheNo": trancheNo, "lineNo": lineNo})
	return nil
}

func (e *Engine) applyResolution(d *Deal, h *HeldPortion, resolution string) error {
	switch resolution {
	case ResolutionRelease:
		tr, err := e.newAcceptedTranche(d, h, 0)
		if err != nil {
			return err
		}
		x := ExitDispute
		h.Exit, h.State = &x, HeldStateClosed
		if err := e.saveHeld(h); err != nil {
			return err
		}
		defer e.emitAcceptance(d, tr, false)
	case ResolutionRefund:
		if err := e.closeHeld(d, h, ExitDispute); err != nil {
			return err
		}
	default:
		return fail(ErrBadRequest, "resolution must be %s or %s", ResolutionRelease, ResolutionRefund)
	}
	if err := e.refreshStatus(d); err != nil {
		return err
	}
	e.emit("DisputeResolved", d.DealID, map[string]any{"trancheNo": h.TrancheNo, "lineNo": h.LineNo, "resolution": resolution})
	return nil
}

// ResolveDispute — role ARBITER on the BANK org (prototype). Records a decision only; it does
// not replace the MSEFC or the courts (proposal Appendix E).
func (e *Engine) ResolveDispute(dealID string, trancheNo, lineNo int64, resolution string) error {
	c, err := e.requireOrg(OrgBank)
	if err != nil {
		return err
	}
	if err := requireRole(c, RoleArbiter); err != nil { // authorise BEFORE touching state
		return err
	}
	_, d, h, err := e.heldCtx(dealID, trancheNo, lineNo, OrgBank)
	if err != nil {
		return err
	}
	if h.State != HeldStateDisputed {
		return fail(ErrInvalidTransition, "held portion is %s, not DISPUTED", h.State)
	}
	return e.applyResolution(d, h, resolution)
}

// ApplyDisputeTimeout — time rule, BANK or BUYER may trigger (idempotent: fails once resolved).
func (e *Engine) ApplyDisputeTimeout(dealID string, trancheNo, lineNo int64) error {
	_, d, h, err := e.heldCtx(dealID, trancheNo, lineNo, OrgBank, OrgBuyer)
	if err != nil {
		return err
	}
	if h.State != HeldStateDisputed || h.DisputeRaisedAt == nil {
		return fail(ErrInvalidTransition, "held portion is %s, not DISPUTED", h.State)
	}
	if e.now().Before(h.DisputeRaisedAt.Add(DisputeTimeout)) {
		return fail(ErrInvalidTransition, "dispute timeout not reached (%s)", h.DisputeRaisedAt.Add(DisputeTimeout).Format(time.RFC3339))
	}
	return e.applyResolution(d, h, DisputeDefault)
}
