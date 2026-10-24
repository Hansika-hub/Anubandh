package core

// trancheValue recomputes what a tranche is worth from ON-CHAIN data only
// (accepted qty x unit price, or the co-signed adjusted price). The caller never supplies money.
func (e *Engine) trancheValue(tr *Tranche) (int64, error) {
	var total int64
	for _, tl := range tr.Lines {
		price := tl.AdjustedUnitPricePaise
		if price == 0 {
			ln, err := e.loadLine(tr.DealID, tl.LineNo)
			if err != nil {
				return 0, err
			}
			price = ln.UnitPricePaise
		}
		v, err := mul(tl.AcceptedQty, price)
		if err != nil {
			return 0, err
		}
		if total, err = add(total, v); err != nil {
			return 0, err
		}
	}
	return total, nil
}

func openObjection(tr *Tranche) bool { return tr.Objection != nil && tr.Objection.ResolvedAt == nil }

// ApproveRelease — Org BUYER. The caller's cert role must belong to the band that matches
// this tranche's value ("highest matching band wins"). When every required role has signed
// the tranche becomes APPROVED.
func (e *Engine) ApproveRelease(dealID string, trancheNo int64) error {
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
	switch tr.Status {
	case TrAccepted, TrDeemedAccepted:
	case TrReleased, TrSettled:
		return fail(ErrAlreadyReleased, "tranche %d already %s", trancheNo, tr.Status)
	default:
		return fail(ErrInvalidTransition, "tranche %d is %s, approvals need an accepted tranche", trancheNo, tr.Status)
	}
	if openObjection(tr) {
		return fail(ErrInvalidTransition, "tranche %d has an open objection", trancheNo)
	}
	req := requiredRoles(d.ApprovalPolicy, tr.AcceptedValuePaise)
	if !roleIn(c.Role, req) {
		return fail(ErrForbiddenRole, "role %q is not an approver for a tranche of %d paise (needs %v)", c.Role, tr.AcceptedValuePaise, req)
	}
	for _, a := range tr.Approvals {
		if a.Role == c.Role {
			return fail(ErrDuplicate, "role %s has already approved tranche %d", c.Role, trancheNo)
		}
	}
	tr.Approvals = append(tr.Approvals, Approval{Role: c.Role, UserID: c.UserID, SignedAt: e.now()})
	satisfied := policySatisfied(req, tr.Approvals)
	if satisfied {
		if err := moveTranche(tr, TrApproved); err != nil {
			return err
		}
	}
	if err := e.saveTranche(tr); err != nil {
		return err
	}
	if err := e.refreshStatus(d); err != nil {
		return err
	}
	e.emit("ApprovalRecorded", dealID, map[string]any{"trancheNo": trancheNo, "role": string(c.Role), "userId": c.UserID, "satisfied": satisfied})
	return nil
}

// ReleaseTranche — Org BUYER. APPROVED -> RELEASED. Amount is computed here, once per tranche.
func (e *Engine) ReleaseTranche(dealID string, trancheNo int64, idemKey string) error {
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
		return err
	}
	switch tr.Status {
	case TrReleased, TrSettled:
		return fail(ErrAlreadyReleased, "tranche %d already %s", trancheNo, tr.Status)
	case TrAccepted, TrDeemedAccepted:
		return fail(ErrPolicyNotSatisfied, "tranche %d lacks the required approvals", trancheNo)
	case TrApproved:
	default:
		return fail(ErrInvalidTransition, "tranche %d is %s", trancheNo, tr.Status)
	}
	if openObjection(tr) {
		return fail(ErrInvalidTransition, "tranche %d has an open objection", trancheNo)
	}
	if RequireVerifiedInvoice && !d.Invoice.Verified {
		return fail(ErrInvalidTransition, "invoice not verified")
	}
	amt, err := e.trancheValue(tr)
	if err != nil {
		return err
	}
	if amt <= 0 || amt != tr.AcceptedValuePaise {
		return fail(ErrInternal, "tranche %d value mismatch (%d vs %d)", trancheNo, amt, tr.AcceptedValuePaise)
	}
	if d.TotalReleasedPaise+amt+d.TotalHeldPaise > d.LienAmountPaise {
		return fail(ErrLienExceeded, "release of %d would push released+held above the lien", amt)
	}
	if err := moveTranche(tr, TrReleased); err != nil {
		return err
	}
	key := idemKey
	tr.ReleaseIdemKey, tr.ReleasedAt = &key, tp(e.now())
	d.TotalReleasedPaise += amt
	if err := e.saveTranche(tr); err != nil {
		return err
	}
	if err := e.refreshStatus(d); err != nil {
		return err
	}
	e.emit("TrancheReleased", dealID, map[string]any{"trancheNo": trancheNo, "amountPaise": amt, "supplierId": d.SupplierID, "supplierAccountRef": d.SupplierAccountRef})
	return nil
}

var validRails = map[string]bool{"UPI": true, "IMPS": true, "NEFT": true, "RTGS": true}

// ConfirmSettlement — Org BANK. RELEASED -> SETTLED.
func (e *Engine) ConfirmSettlement(dealID string, trancheNo int64, bankRef, rail string) error {
	if _, err := e.requireOrg(OrgBank); err != nil {
		return err
	}
	d, err := e.loadDeal(dealID)
	if err != nil {
		return err
	}
	tr, err := e.loadTranche(dealID, trancheNo)
	if err != nil {
		return err
	}
	if trim(bankRef) == "" || !validRails[rail] {
		return fail(ErrBadRequest, "bankRef and a valid rail (UPI|IMPS|NEFT|RTGS) are required")
	}
	if err := moveTranche(tr, TrSettled); err != nil {
		return err
	}
	tr.BankRef, tr.Rail, tr.SettledAt = &bankRef, &rail, tp(e.now())
	if err := e.saveTranche(tr); err != nil {
		return err
	}
	if err := e.refreshStatus(d); err != nil {
		return err
	}
	e.emit("SettlementConfirmed", dealID, map[string]any{"trancheNo": trancheNo, "bankRef": bankRef, "rail": rail})
	return nil
}
