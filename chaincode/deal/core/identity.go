package core

import "fmt"

// MSPOrg maps MSP IDs to organisations. CONFIRM WITH M3 (network/README.md) — these names are
// an assumption, not something BUILD.md fixes.
var MSPOrg = map[string]Org{"BuyerMSP": OrgBuyer, "SupplierMSP": OrgSupplier, "BankMSP": OrgBank, "AuditorMSP": OrgAuditor}

// OrgMSPs is the reverse map used for key-level endorsement.
var OrgMSPs = map[Org][]string{OrgBuyer: {"BuyerMSP"}, OrgBank: {"BankMSP"}, OrgSupplier: {"SupplierMSP"}, OrgAuditor: {"AuditorMSP"}}

var roleOrg = map[Role]Org{RoleWarehouse: OrgBuyer, RolePlantHead: OrgBuyer, RoleCFO: OrgBuyer, RoleProcurement: OrgBuyer,
	RoleTreasurer: OrgBuyer, RoleSupplierUser: OrgSupplier, RoleBankOfficer: OrgBank, RoleArbiter: OrgBank, RoleAuditor: OrgAuditor}

// OrgFromIdentity derives the acting organisation from the cert's MSP and its "role" attribute.
// Proposal: suppliers run no nodes and get identities from their BANK's CA, so a SUPPLIER_USER
// legitimately arrives under the bank's MSP. Every other role must come from its own org's MSP.
func OrgFromIdentity(msp string, role Role) (Org, error) {
	mspOrg, known := MSPOrg[msp]
	if !known {
		return "", fmt.Errorf("unknown MSP %q", msp)
	}
	want, ok := roleOrg[role]
	if !ok {
		return "", fmt.Errorf("certificate has no valid role attribute (got %q)", role)
	}
	if want == mspOrg || (want == OrgSupplier && (mspOrg == OrgBank || mspOrg == OrgSupplier)) {
		return want, nil
	}
	return "", fmt.Errorf("role %s cannot be issued by %s", role, msp)
}
