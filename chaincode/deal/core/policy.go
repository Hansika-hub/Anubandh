package core

import "sort"

// RequireVerifiedInvoice: when true, ReleaseTranche needs deal.invoice.verified.
// OFF by default because the scripted demo (BUILD §15) has no invoice step. Team decision D-1.
var RequireVerifiedInvoice = false

var validRoles = map[Role]bool{RoleWarehouse: true, RolePlantHead: true, RoleCFO: true, RoleProcurement: true,
	RoleTreasurer: true, RoleSupplierUser: true, RoleBankOfficer: true, RoleArbiter: true, RoleAuditor: true}

// validatePolicy: must cover amount 0, roles valid, bands sorted ascending.
func validatePolicy(p []PolicyBand) ([]PolicyBand, error) {
	if len(p) == 0 {
		return nil, fail(ErrBadRequest, "approvalPolicy is empty")
	}
	out := append([]PolicyBand(nil), p...)
	sort.Slice(out, func(i, j int) bool { return out[i].MinPaise < out[j].MinPaise })
	if out[0].MinPaise != 0 {
		return nil, fail(ErrBadRequest, "approvalPolicy needs a band starting at minPaise 0")
	}
	for i, b := range out {
		if b.MinPaise < 0 || len(b.RequiredRoles) == 0 || (i > 0 && b.MinPaise == out[i-1].MinPaise) {
			return nil, fail(ErrBadRequest, "approvalPolicy band %d invalid", i)
		}
		for _, r := range b.RequiredRoles {
			if !validRoles[r] {
				return nil, fail(ErrBadRequest, "unknown role %q in approvalPolicy", r)
			}
		}
	}
	return out, nil
}

// requiredRoles returns the roles of the HIGHEST band whose minPaise <= amount.
func requiredRoles(p []PolicyBand, amount int64) []Role {
	var best *PolicyBand
	for i := range p {
		if p[i].MinPaise <= amount && (best == nil || p[i].MinPaise > best.MinPaise) {
			best = &p[i]
		}
	}
	if best == nil {
		return nil
	}
	return best.RequiredRoles
}

func policySatisfied(req []Role, ap []Approval) bool {
	have := map[Role]bool{}
	for _, a := range ap {
		have[a.Role] = true
	}
	for _, r := range req {
		if !have[r] {
			return false
		}
	}
	return len(req) > 0
}

func roleIn(r Role, set []Role) bool {
	for _, x := range set {
		if x == r {
			return true
		}
	}
	return false
}
