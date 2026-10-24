package core

import (
	"encoding/json"
	"time"
)

type grIn struct {
	LineNo       int64 `json:"lineNo"`
	DeliveredQty int64 `json:"deliveredQty"`
}

type qcIn struct {
	LineNo      int64 `json:"lineNo"`
	AcceptedQty int64 `json:"acceptedQty"`
	RejectedQty int64 `json:"rejectedQty"`
}

func tp(t time.Time) *time.Time { return &t }

// PostGoodsReceipt — Org BUYER, role WAREHOUSE. Creates tranche DELIVERED (or delivers a
// PENDING replacement tranche). Tranche numbers are sequential; an out-of-order number is
// ERR_INVALID_TRANSITION so the ingest worker requeues instead of failing.
func (e *Engine) PostGoodsReceipt(dealID string, trancheNo int64, linesJSON, idemKey string) error {
	c, err := e.requireOrg(OrgBuyer)
	if err != nil {
		return err
	}
	if err := requireRole(c, RoleWarehouse); err != nil {
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
		return fail(ErrInvalidTransition, "deal is %s: goods can only be received on a funded deal", d.Status)
	}
	var in []grIn
	if err := json.Unmarshal([]byte(linesJSON), &in); err != nil || len(in) == 0 {
		return fail(ErrBadRequest, "linesJSON must be a non-empty array")
	}
	seen := map[int64]bool{}
	for _, l := range in {
		if l.LineNo < 1 || l.DeliveredQty < 1 || seen[l.LineNo] {
			return fail(ErrBadRequest, "invalid line %d", l.LineNo)
		}
		seen[l.LineNo] = true
	}
	trs, err := e.listTranches(dealID)
	if err != nil {
		return err
	}
	var existing *Tranche
	var maxNo int64
	for _, t := range trs {
		if t.TrancheNo == trancheNo {
			existing = t
		}
		if t.TrancheNo > maxNo {
			maxNo = t.TrancheNo
		}
	}
	now := e.now()
	if existing != nil {
		if existing.Status != TrPending {
			return fail(ErrInvalidTransition, "tranche %d already %s", trancheNo, existing.Status)
		}
		if len(existing.Lines) != len(in) {
			return fail(ErrQtyMismatch, "replacement tranche expects %d line(s)", len(existing.Lines))
		}
		for _, el := range existing.Lines {
			ok := false
			for _, l := range in {
				ok = ok || (l.LineNo == el.LineNo && l.DeliveredQty == el.DeliveredQty)
			}
			if !ok {
				return fail(ErrQtyMismatch, "replacement line %d must deliver exactly %d", el.LineNo, el.DeliveredQty)
			}
		}
		for _, l := range in {
			ln, err := e.loadLine(dealID, l.LineNo)
			if err != nil {
				return err
			}
			ln.DeliveredQty += l.DeliveredQty
			if err := e.saveLine(ln); err != nil {
				return err
			}
		}
		if err := moveTranche(existing, TrDelivered); err != nil {
			return err
		}
		existing.DeliveredAt = tp(now)
		if err := e.saveTranche(existing); err != nil {
			return err
		}
	} else {
		if trancheNo != maxNo+1 {
			return fail(ErrInvalidTransition, "next tranche number is %d, got %d", maxNo+1, trancheNo)
		}
		tr := &Tranche{DealID: dealID, TrancheNo: trancheNo, Status: TrDelivered, DeliveredAt: tp(now), Approvals: []Approval{}}
		for _, l := range in {
			ln, err := e.loadLine(dealID, l.LineNo)
			if err != nil {
				return err
			}
			var inflight int64
			for _, t := range trs {
				if t.Status == TrDelivered || t.Status == TrPending {
					for _, tl := range t.Lines {
						if tl.LineNo == l.LineNo {
							inflight += tl.DeliveredQty
						}
					}
				}
			}
			if l.DeliveredQty > ln.OrderedQty-ln.AcceptedQty-ln.HeldQty-inflight {
				return fail(ErrQtyMismatch, "line %d: delivering %d exceeds what is still owed", l.LineNo, l.DeliveredQty)
			}
			ln.DeliveredQty += l.DeliveredQty
			if err := e.saveLine(ln); err != nil {
				return err
			}
			tr.Lines = append(tr.Lines, TrancheLine{LineNo: l.LineNo, DeliveredQty: l.DeliveredQty})
		}
		if err := e.saveTranche(tr); err != nil {
			return err
		}
		existing = tr
	}
	if err := e.refreshStatus(d); err != nil {
		return err
	}
	e.emit("GoodsReceived", dealID, map[string]any{"trancheNo": trancheNo, "lines": existing.Lines})
	return nil
}

// AcknowledgeDelivery — Org SUPPLIER co-signs the delivery (evidence for the MSE clock).
func (e *Engine) AcknowledgeDelivery(dealID string, trancheNo int64) error {
	c, err := e.requireOrg(OrgSupplier)
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
	tr, err := e.loadTranche(dealID, trancheNo)
	if err != nil {
		return err
	}
	if tr.Status == TrPending || tr.DeliveredAt == nil {
		return fail(ErrInvalidTransition, "tranche %d has no goods receipt yet", trancheNo)
	}
	if tr.AckedAt != nil {
		return fail(ErrInvalidTransition, "tranche %d already acknowledged", trancheNo)
	}
	tr.AckedAt = tp(e.now())
	if err := e.saveTranche(tr); err != nil {
		return err
	}
	e.emit("DeliveryAcknowledged", dealID, map[string]any{"trancheNo": trancheNo})
	return nil
}

func (e *Engine) startClock(d *Deal, tr *Tranche, at time.Time) {
	if !d.MSE.Applies {
		return
	}
	dl := at.Add(time.Duration(d.MSE.TermDays) * day)
	tr.MseClockStart, tr.MseDeadline = tp(at), tp(dl)
}

// emitAcceptance emits the acceptance events for a tranche (batched by the adapter).
func (e *Engine) emitAcceptance(d *Deal, tr *Tranche, deemed bool) {
	p := map[string]any{"trancheNo": tr.TrancheNo}
	if tr.MseClockStart != nil {
		p["mseClockStart"], p["mseDeadline"] = tr.MseClockStart.Format(time.RFC3339), tr.MseDeadline.Format(time.RFC3339)
	}
	if deemed {
		e.emit("DeemedAcceptance", d.DealID, p)
	} else {
		p["deemed"] = false
		e.emit("TrancheAccepted", d.DealID, p)
	}
	if d.MSE.Applies && tr.MseClockStart != nil {
		e.emit("MseClockStarted", d.DealID, map[string]any{"trancheNo": tr.TrancheNo, "termDays": d.MSE.TermDays, "mseDeadline": tr.MseDeadline.Format(time.RFC3339)})
	}
}

// closeReplaced closes the held portion a replacement tranche stands in for.
func (e *Engine) closeReplaced(d *Deal, tr *Tranche) error {
	if tr.Replaces == nil {
		return nil
	}
	h, err := e.loadHeld(d.DealID, tr.Replaces.TrancheNo, tr.Replaces.LineNo)
	if err != nil {
		return err
	}
	if h.State != HeldStateReplacing {
		return nil
	}
	h.State = HeldStateClosed
	d.TotalHeldPaise -= h.ValuePaise
	ln, err := e.loadLine(d.DealID, h.LineNo)
	if err != nil {
		return err
	}
	ln.HeldQty -= h.Qty
	if err := e.saveLine(ln); err != nil {
		return err
	}
	return e.saveHeld(h)
}

// RecordQualityCheck — Org BUYER. Splits a delivered tranche into accepted and held parts.
func (e *Engine) RecordQualityCheck(dealID string, trancheNo int64, resultsJSON, idemKey string) error {
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
	tr, err := e.loadTranche(dealID, trancheNo)
	if err != nil {
		if CodeOf(err) == ErrNotFound { // QC arrived before the goods receipt
			return fail(ErrInvalidTransition, "no goods receipt for tranche %d yet", trancheNo)
		}
		return err
	}
	if tr.Status != TrDelivered {
		return fail(ErrInvalidTransition, "tranche %d is %s, QC needs DELIVERED", trancheNo, tr.Status)
	}
	var in []qcIn
	if err := json.Unmarshal([]byte(resultsJSON), &in); err != nil || len(in) != len(tr.Lines) {
		return fail(ErrBadRequest, "resultsJSON needs exactly one result per delivered line")
	}
	res := map[int64]qcIn{}
	var totDel, totRej int64
	for _, r := range in {
		res[r.LineNo] = r
	}
	for _, tl := range tr.Lines {
		r, ok := res[tl.LineNo]
		if !ok || r.AcceptedQty < 0 || r.RejectedQty < 0 || r.AcceptedQty+r.RejectedQty != tl.DeliveredQty {
			return fail(ErrQtyMismatch, "line %d: accepted+rejected must equal delivered %d", tl.LineNo, tl.DeliveredQty)
		}
		totDel += tl.DeliveredQty
		totRej += r.RejectedQty
	}
	// Variance within the deal's tolerance: the whole lot is accepted (AQL-style).
	withinTol := totRej > 0 && totRej*10000 <= d.ToleranceBps*totDel
	now := e.now()
	var accV, heldV int64
	var heldEvents []map[string]any
	for i := range tr.Lines {
		tl := &tr.Lines[i]
		r := res[tl.LineNo]
		if withinTol {
			r = qcIn{LineNo: tl.LineNo, AcceptedQty: tl.DeliveredQty}
		}
		ln, err := e.loadLine(dealID, tl.LineNo)
		if err != nil {
			return err
		}
		av, err := mul(r.AcceptedQty, ln.UnitPricePaise)
		if err != nil {
			return err
		}
		hv, err := mul(r.RejectedQty, ln.UnitPricePaise)
		if err != nil {
			return err
		}
		tl.AcceptedQty, tl.RejectedQty = r.AcceptedQty, r.RejectedQty
		accV += av
		heldV += hv
		ln.AcceptedQty += r.AcceptedQty
		ln.RejectedQty += r.RejectedQty
		if r.RejectedQty > 0 {
			ln.HeldQty += r.RejectedQty
			h := &HeldPortion{DealID: dealID, TrancheNo: trancheNo, LineNo: tl.LineNo, Qty: r.RejectedQty, ValuePaise: hv, State: HeldStateHeld}
			if err := e.saveHeld(h); err != nil {
				return err
			}
			heldEvents = append(heldEvents, map[string]any{"trancheNo": trancheNo, "lineNo": tl.LineNo, "qty": r.RejectedQty, "valuePaise": hv})
		}
		if err := e.saveLine(ln); err != nil {
			return err
		}
	}
	tr.AcceptedValuePaise, tr.HeldValuePaise = accV, heldV
	if d.TotalHeldPaise, err = add(d.TotalHeldPaise, heldV); err != nil {
		return err
	}
	if d.TotalReleasedPaise+d.TotalHeldPaise > d.LienAmountPaise {
		return fail(ErrLienExceeded, "released+held would exceed the lien")
	}
	if err := e.closeReplaced(d, tr); err != nil {
		return err
	}
	accepted := accV > 0
	if accepted {
		if err := moveTranche(tr, TrAccepted); err != nil {
			return err
		}
		tr.AcceptedAt = tp(now)
		e.startClock(d, tr, now)
	} else if err := moveTranche(tr, TrHeld); err != nil {
		return err
	}
	if err := e.saveTranche(tr); err != nil {
		return err
	}
	if err := e.refreshStatus(d); err != nil {
		return err
	}
	e.emit("QualityRecorded", dealID, map[string]any{"trancheNo": trancheNo, "acceptedValuePaise": accV, "heldValuePaise": heldV})
	for _, h := range heldEvents {
		e.emit("HeldPortionCreated", dealID, h)
	}
	if accepted {
		e.emitAcceptance(d, tr, false)
	}
	return nil
}
