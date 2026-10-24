package core

// Deal-level table (BUILD §5.4). A deal can hold several tranches, so after FUNDED the
// deal status is DERIVED (deriveStatus) and moves between the "live" states. The strict,
// per-step guard is the tranche table below.
var dealLive = map[string]bool{
	"DELIVERED": true, "ACCEPTED": true, "PARTIALLY_ACCEPTED": true,
	"RELEASED": true, "SETTLED": true, "DISPUTED": true,
}

// CanTransition reports whether a deal may move from -> to.
func CanTransition(from, to string) bool {
	switch from {
	case "CREATED":
		return to == "FUNDED"
	case "FUNDED":
		return to == "EXPIRED" || to == "DELIVERED"
	case "EXPIRED", "CLOSED":
		return false
	}
	if dealLive[from] {
		return dealLive[to] || to == "CLOSED"
	}
	return false
}

var trancheNext = map[TrancheStatus][]TrancheStatus{
	TrPending:        {TrDelivered},
	TrDelivered:      {TrAccepted, TrDeemedAccepted, TrHeld},
	TrAccepted:       {TrApproved},
	TrDeemedAccepted: {TrApproved},
	TrApproved:       {TrReleased},
	TrReleased:       {TrSettled},
	TrHeld:           {TrClosed},
}

// CanTransitionTranche is the strict tranche state machine.
func CanTransitionTranche(from, to TrancheStatus) bool {
	for _, n := range trancheNext[from] {
		if n == to {
			return true
		}
	}
	return false
}

func moveTranche(t *Tranche, to TrancheStatus) error {
	if !CanTransitionTranche(t.Status, to) {
		return fail(ErrInvalidTransition, "tranche %d: %s -> %s not allowed", t.TrancheNo, t.Status, to)
	}
	t.Status = to
	return nil
}

// deriveStatus computes the deal status from its tranches and held portions.
func deriveStatus(cur DealStatus, trs []*Tranche, held []*HeldPortion) DealStatus {
	if cur == DealCreated || isTerminal(cur) || len(trs) == 0 {
		return cur
	}
	openHeld, disputed := false, false
	for _, h := range held {
		if h.State != HeldStateClosed {
			openHeld = true
		}
		if h.State == HeldStateDisputed {
			disputed = true
		}
	}
	if disputed {
		return DealDisputed
	}
	var pending, delivered, accepted, released, anySettled bool
	for _, t := range trs {
		switch t.Status {
		case TrPending:
			pending = true
		case TrDelivered:
			delivered = true
		case TrAccepted, TrDeemedAccepted, TrApproved:
			accepted = true
		case TrReleased:
			released = true
		case TrSettled:
			anySettled = true
		}
	}
	switch {
	case delivered:
		return DealDelivered
	case pending:
		return DealPartiallyAccepted
	case accepted:
		if openHeld {
			return DealPartiallyAccepted
		}
		return DealAccepted
	case released:
		return DealReleased
	case openHeld:
		return DealPartiallyAccepted
	case anySettled:
		return DealSettled
	}
	return DealClosed
}

// refreshStatus re-derives and persists the deal status (with the table check in saveDeal).
func (e *Engine) refreshStatus(d *Deal) error {
	trs, err := e.listTranches(d.DealID)
	if err != nil {
		return err
	}
	held, err := e.listHeld(d.DealID)
	if err != nil {
		return err
	}
	old := d.Status
	d.Status = deriveStatus(old, trs, held)
	return e.saveDeal(d, old)
}
