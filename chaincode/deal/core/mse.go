package core

import (
	"encoding/json"
	"math"
	"sort"
	"time"
)

const (
	// ObjectionWindow is the MSMED Act s.15 window: a buyer must object within 15 days of
	// delivery, otherwise the goods are deemed accepted on day 15.
	ObjectionWindow = 15 * day
	// AlertWindow is when MseDeadlineApproaching fires (once per tranche).
	AlertWindow = 5 * day
)

func daysLeft(deadline, now time.Time) int64 {
	return int64(math.Ceil(deadline.Sub(now).Hours() / 24))
}

// RaiseObjection — Org BUYER, within 15 days of delivery, before deemed acceptance.
func (e *Engine) RaiseObjection(dealID string, trancheNo int64, reason string) error {
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
	tr, err := e.loadTranche(dealID, trancheNo)
	if err != nil {
		return err
	}
	if trim(reason) == "" {
		return fail(ErrBadRequest, "reason required")
	}
	if tr.Status == TrDeemedAccepted || tr.DeliveredAt == nil || e.now().After(tr.DeliveredAt.Add(ObjectionWindow)) {
		return fail(ErrObjectionWindowShut, "objection window for tranche %d is closed", trancheNo)
	}
	if tr.Status != TrDelivered && tr.Status != TrAccepted {
		return fail(ErrInvalidTransition, "tranche %d is %s", trancheNo, tr.Status)
	}
	if tr.Objection != nil && tr.Objection.ResolvedAt == nil {
		return fail(ErrInvalidTransition, "tranche %d already has an open objection", trancheNo)
	}
	tr.Objection = &Objection{Reason: reason, RaisedAt: e.now()}
	if err := e.saveTranche(tr); err != nil {
		return err
	}
	e.emit("ObjectionRaised", dealID, map[string]any{"trancheNo": trancheNo, "reason": reason})
	return nil
}

// ResolveObjection — BUYER and SUPPLIER each call once (co-sign). On the second signature the
// objection is resolved and the MSE clock for the tranche restarts at that moment.
func (e *Engine) ResolveObjection(dealID string, trancheNo int64) error {
	c, err := e.requireOrg(OrgBuyer, OrgSupplier)
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
	o := tr.Objection
	if o == nil || o.ResolvedAt != nil {
		return fail(ErrInvalidTransition, "tranche %d has no open objection", trancheNo)
	}
	now := e.now()
	if c.Org == OrgBuyer {
		if o.BuyerSignedAt != nil {
			return fail(ErrDuplicate, "buyer already signed")
		}
		o.BuyerSignedAt = tp(now)
	} else {
		if o.SupplierSignedAt != nil {
			return fail(ErrDuplicate, "supplier already signed")
		}
		o.SupplierSignedAt = tp(now)
	}
	resolved := o.BuyerSignedAt != nil && o.SupplierSignedAt != nil
	if resolved {
		o.ResolvedAt = tp(now)
		if tr.AcceptedAt != nil { // already accepted: acceptance date moves to resolution
			tr.AcceptedAt = tp(now)
			e.startClock(d, tr, now)
		}
	}
	if err := e.saveTranche(tr); err != nil {
		return err
	}
	if resolved {
		e.emit("ObjectionResolved", dealID, map[string]any{"trancheNo": trancheNo})
		if d.MSE.Applies && tr.MseClockStart != nil {
			e.emit("MseClockRestarted", dealID, map[string]any{"trancheNo": trancheNo, "mseClockStart": tr.MseClockStart.Format(time.RFC3339), "mseDeadline": tr.MseDeadline.Format(time.RFC3339)})
		}
	}
	return nil
}

// deemedAt returns when a DELIVERED tranche becomes deemed-accepted (nil if paused/not applicable).
func deemedAt(tr *Tranche) *time.Time {
	if tr.Status != TrDelivered || tr.DeliveredAt == nil || openObjection(tr) {
		return nil
	}
	base := *tr.DeliveredAt
	if tr.Objection != nil && tr.Objection.ResolvedAt != nil && tr.Objection.ResolvedAt.After(base) {
		base = *tr.Objection.ResolvedAt
	}
	return tp(base.Add(ObjectionWindow))
}

// TickDeadlines — time rules for one deal. Idempotent: does nothing when nothing is due.
// Restricted to BANK/BUYER because Fabric's tx timestamp is client-proposed: only the
// parties whose peers endorse may drive the clock (see HANDOFF.md, "timekeeper").
func (e *Engine) TickDeadlines(dealID string) error {
	if _, err := e.requireOrg(OrgBank, OrgBuyer); err != nil {
		return err
	}
	d, err := e.loadDeal(dealID)
	if err != nil {
		return err
	}
	if isTerminal(d.Status) {
		return nil
	}
	if done, err := e.expireIfDue(d); err != nil || done {
		return err
	}
	trs, err := e.listTranches(dealID)
	if err != nil {
		return err
	}
	now := e.now()
	changed := false
	for _, tr := range trs {
		// 1) deemed acceptance (MSE deals only)
		if d.MSE.Applies {
			if at := deemedAt(tr); at != nil && !now.Before(*at) {
				if err := e.deem(d, tr, *at); err != nil {
					return err
				}
				changed = true
				continue
			}
		}
		// 3) deadline approaching (once per tranche)
		live := tr.Status == TrAccepted || tr.Status == TrDeemedAccepted || tr.Status == TrApproved || tr.Status == TrReleased
		if d.MSE.Applies && live && tr.MseDeadline != nil && !tr.MseAlerted && tr.MseDeadline.Sub(now) <= AlertWindow {
			tr.MseAlerted = true
			if err := e.saveTranche(tr); err != nil {
				return err
			}
			e.emit("MseDeadlineApproaching", dealID, map[string]any{"trancheNo": tr.TrancheNo, "mseDeadline": tr.MseDeadline.Format(time.RFC3339), "daysLeft": daysLeft(*tr.MseDeadline, now)})
			changed = true
		}
	}
	if changed {
		return e.refreshStatus(d)
	}
	return nil
}

// deem accepts a whole delivered tranche. The acceptance date is the DEEMED date (delivery +15d,
// or objection resolution +15d), not the moment the tick happens to run.
func (e *Engine) deem(d *Deal, tr *Tranche, at time.Time) error {
	var v int64
	for i := range tr.Lines {
		tl := &tr.Lines[i]
		ln, err := e.loadLine(d.DealID, tl.LineNo)
		if err != nil {
			return err
		}
		tl.AcceptedQty, tl.RejectedQty = tl.DeliveredQty, 0
		ln.AcceptedQty += tl.DeliveredQty
		lv, err := mul(tl.DeliveredQty, ln.UnitPricePaise)
		if err != nil {
			return err
		}
		if v, err = add(v, lv); err != nil {
			return err
		}
		if err := e.saveLine(ln); err != nil {
			return err
		}
	}
	if err := moveTranche(tr, TrDeemedAccepted); err != nil {
		return err
	}
	tr.AcceptedValuePaise, tr.DeemedAcceptance, tr.AcceptedAt = v, true, tp(at)
	e.startClock(d, tr, at)
	if err := e.closeReplaced(d, tr); err != nil {
		return err
	}
	if err := e.saveTranche(tr); err != nil {
		return err
	}
	e.emitAcceptance(d, tr, true)
	return nil
}

func (e *Engine) clockFor(d *Deal, tr *Tranche) *MseClock {
	now := e.now()
	c := &MseClock{DealID: d.DealID, TrancheNo: tr.TrancheNo, Applies: d.MSE.Applies, TermDays: d.MSE.TermDays,
		ClockStart: tr.MseClockStart, Deadline: tr.MseDeadline, DeemedAcceptanceAt: deemedAt(tr)}
	switch {
	case !d.MSE.Applies:
		c.State, c.TermDays = "NOT_APPLICABLE", 0
	case tr.Status == TrSettled:
		c.State = "PAID"
	case tr.MseDeadline == nil:
		c.State = "NOT_STARTED"
	case now.After(*tr.MseDeadline):
		c.State = "OVERDUE"
	default:
		c.State = "RUNNING"
	}
	if tr.MseDeadline != nil {
		c.DaysLeft = daysLeft(*tr.MseDeadline, now)
	}
	return c
}

func (e *Engine) GetMseClock(dealID string, trancheNo int64) (*MseClock, error) {
	d, err := e.loadDeal(dealID)
	if err != nil {
		return nil, err
	}
	tr, err := e.loadTranche(dealID, trancheNo)
	if err != nil {
		return nil, err
	}
	return e.clockFor(d, tr), nil
}

// QueryMseDealsNearDeadline returns unsettled MSE tranches whose deadline is within withinDays
// (overdue ones included), soonest first. Uses a key-range scan, not a rich query.
func (e *Engine) QueryMseDealsNearDeadline(withinDays int64) ([]*Tranche, error) {
	if withinDays < 0 || withinDays > 3650 {
		return nil, fail(ErrBadRequest, "withinDays out of range")
	}
	kvs, err := e.rng("tranche:")
	if err != nil {
		return nil, fail(ErrInternal, "range: %v", err)
	}
	now := e.now()
	out := []*Tranche{}
	for _, kv := range kvs {
		var t Tranche
		if err := json.Unmarshal(kv.Value, &t); err != nil {
			return nil, fail(ErrInternal, "decode: %v", err)
		}
		if t.MseDeadline == nil || t.Status == TrSettled {
			continue
		}
		if t.MseDeadline.Sub(now) <= time.Duration(withinDays)*day {
			out = append(out, &t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MseDeadline.Before(*out[j].MseDeadline) })
	return out, nil
}
