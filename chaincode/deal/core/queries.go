package core

var validDealStatus = map[DealStatus]bool{DealCreated: true, DealFunded: true, DealDelivered: true, DealAccepted: true,
	DealPartiallyAccepted: true, DealReleased: true, DealSettled: true, DealDisputed: true, DealExpired: true, DealClosed: true}

func (e *Engine) GetDeal(dealID string) (*Deal, error) { return e.loadDeal(dealID) }

func (e *Engine) GetTranche(dealID string, trancheNo int64) (*Tranche, error) {
	return e.loadTranche(dealID, trancheNo)
}

func (e *Engine) ListTranches(dealID string) ([]*Tranche, error) {
	if _, err := e.loadDeal(dealID); err != nil {
		return nil, err
	}
	return e.listTranches(dealID)
}

func (e *Engine) GetHeldPortions(dealID string) ([]*HeldPortion, error) {
	if _, err := e.loadDeal(dealID); err != nil {
		return nil, err
	}
	return e.listHeld(dealID)
}

func (e *Engine) GetDealHistory(dealID string) ([]*HistoryEntry, error) {
	if _, err := e.loadDeal(dealID); err != nil {
		return nil, err
	}
	h, err := e.L.History(dealKey(dealID))
	if err != nil {
		return nil, fail(ErrInternal, "history: %v", err)
	}
	out := make([]*HistoryEntry, len(h))
	for i := range h {
		out[i] = &h[i]
	}
	return out, nil
}

// QueryDealsByStatus uses the status index maintained by saveDeal (works on any state DB).
func (e *Engine) QueryDealsByStatus(status string) ([]*Deal, error) {
	if !validDealStatus[DealStatus(status)] {
		return nil, fail(ErrBadRequest, "unknown status %q", status)
	}
	prefix := "idx:status:" + status + ":"
	kvs, err := e.rng(prefix)
	if err != nil {
		return nil, fail(ErrInternal, "range: %v", err)
	}
	out := []*Deal{}
	for _, kv := range kvs {
		var d Deal
		ok, err := e.getJSON(dealKey(kv.Key[len(prefix):]), &d)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, &d)
		}
	}
	return out, nil
}
