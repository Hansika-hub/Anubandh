// Package model holds the shared Anubandh data model (BUILD.md §4) as the gateway sees it.
// Field names and enum values are frozen by the contract; do not rename.
package model

import (
	"encoding/json"
	"regexp"
	"time"
)

// Deal statuses (BUILD.md §4.1).
const (
	DealCreated           = "CREATED"
	DealFunded            = "FUNDED"
	DealDelivered         = "DELIVERED"
	DealAccepted          = "ACCEPTED"
	DealPartiallyAccepted = "PARTIALLY_ACCEPTED"
	DealReleased          = "RELEASED"
	DealSettled           = "SETTLED"
	DealDisputed          = "DISPUTED"
	DealExpired           = "EXPIRED"
	DealClosed            = "CLOSED"
)

// Tranche statuses (BUILD.md §4.1).
const (
	TranchePending        = "PENDING"
	TrancheDelivered      = "DELIVERED"
	TrancheAccepted       = "ACCEPTED"
	TrancheDeemedAccepted = "DEEMED_ACCEPTED"
	TrancheApproved       = "APPROVED"
	TrancheReleased       = "RELEASED"
	TrancheSettled        = "SETTLED"
	TrancheHeld           = "HELD"
	TrancheClosed         = "CLOSED"
)

// Held portion exits (BUILD.md §4.1).
const (
	ExitReplacement     = "REPLACEMENT"
	ExitCreditNote      = "CREDIT_NOTE"
	ExitPriceAdjustment = "PRICE_ADJUSTMENT"
	ExitDispute         = "DISPUTE"
)

// MSE categories (BUILD.md §4.1). NONE = trader / not covered.
const (
	MseMicro  = "MICRO"
	MseSmall  = "SMALL"
	MseMedium = "MEDIUM"
	MseNone   = "NONE"
)

// Dispute resolutions (BUILD.md §4.1).
const (
	ResolutionReleaseToSupplier = "RELEASE_TO_SUPPLIER"
	ResolutionRefundToBuyer     = "REFUND_TO_BUYER"
)

// Rail is a payment rail (BUILD.md §4.1).
type Rail string

// Payment rails.
const (
	RailUPI  Rail = "UPI"
	RailIMPS Rail = "IMPS"
	RailNEFT Rail = "NEFT"
	RailRTGS Rail = "RTGS"
)

// Valid reports whether r is one of the frozen rails.
func (r Rail) Valid() bool {
	switch r {
	case RailUPI, RailIMPS, RailNEFT, RailRTGS:
		return true
	}
	return false
}

// Org is a network organisation (BUILD.md §4.1).
type Org string

// Network organisations.
const (
	OrgBuyer    Org = "BUYER"
	OrgSupplier Org = "SUPPLIER"
	OrgBank     Org = "BANK"
	OrgAuditor  Org = "AUDITOR"
)

// Valid reports whether o is one of the frozen orgs.
func (o Org) Valid() bool {
	switch o {
	case OrgBuyer, OrgSupplier, OrgBank, OrgAuditor:
		return true
	}
	return false
}

// Role is a user role carried as the `role` certificate attribute (BUILD.md §4.1).
type Role string

// User roles.
const (
	RoleWarehouse    Role = "WAREHOUSE"
	RolePlantHead    Role = "PLANT_HEAD"
	RoleCFO          Role = "CFO"
	RoleProcurement  Role = "PROCUREMENT"
	RoleTreasurer    Role = "TREASURER"
	RoleSupplierUser Role = "SUPPLIER_USER"
	RoleBankOfficer  Role = "BANK_OFFICER"
	RoleArbiter      Role = "ARBITER"
	RoleAuditor      Role = "AUDITOR"
)

// Valid reports whether r is one of the frozen roles.
func (r Role) Valid() bool {
	switch r {
	case RoleWarehouse, RolePlantHead, RoleCFO, RoleProcurement, RoleTreasurer,
		RoleSupplierUser, RoleBankOfficer, RoleArbiter, RoleAuditor:
		return true
	}
	return false
}

// IsBuyerApprover reports whether r may appear in a deal approval policy band.
func (r Role) IsBuyerApprover() bool {
	switch r {
	case RoleWarehouse, RolePlantHead, RoleCFO, RoleProcurement, RoleTreasurer:
		return true
	}
	return false
}

// IdemKeyRe is "<source>:<sourceEventId>" (BUILD.md §2) with the chaincode's character classes
// (chaincode/deal/core/idempotency.go): the source is lower case without dots or slashes. A key the
// chaincode would reject must fail here with 400, not on chain. Lengths are capped at 32 + 95.
var IdemKeyRe = regexp.MustCompile(`^[a-z0-9_-]{1,32}:[A-Za-z0-9._-]{1,95}$`)

// MSE is the MSMED Act classification of a deal's supplier. WrittenAgreement is the additive field
// from chaincode/deal/CONTRACT_CHANGE_001.md: termDays 45 needs it (MSMED Act §15).
type MSE struct {
	Category         string `json:"category"`
	TermDays         int    `json:"termDays"`
	UdyamNo          string `json:"udyamNo,omitempty"`
	Applies          bool   `json:"applies"`
	WrittenAgreement bool   `json:"writtenAgreement,omitempty"`
}

// PolicyBand is one approval band; at release time the highest band with MinPaise <= value wins.
type PolicyBand struct {
	MinPaise      int64  `json:"minPaise"`
	RequiredRoles []Role `json:"requiredRoles"`
}

// Invoice is the GST e-invoice verification state.
type Invoice struct {
	IRN      *string `json:"irn"`
	Verified bool    `json:"verified"`
}

// Deal is the public part of a deal on the channel ledger.
type Deal struct {
	DealID             string       `json:"dealId"`
	PONumber           string       `json:"poNumber"`
	BuyerID            string       `json:"buyerId"`
	SupplierID         string       `json:"supplierId"`
	Status             string       `json:"status"`
	Currency           string       `json:"currency"`
	LienAmountPaise    int64        `json:"lienAmountPaise"`
	LienRef            *string      `json:"lienRef"`
	TotalReleasedPaise int64        `json:"totalReleasedPaise"`
	TotalHeldPaise     int64        `json:"totalHeldPaise"`
	MSE                MSE          `json:"mse"`
	ApprovalPolicy     []PolicyBand `json:"approvalPolicy"`
	ToleranceBps       int64        `json:"toleranceBps"`
	DeliveryDeadline   *time.Time   `json:"deliveryDeadline"`
	Invoice            Invoice      `json:"invoice"`
	CreatedAt          *time.Time   `json:"createdAt"`
	UpdatedAt          *time.Time   `json:"updatedAt"`
}

// Line is a PO line. Lives in the private data collection collBuyerSupplierBank.
type Line struct {
	DealID         string `json:"dealId"`
	LineNo         int64  `json:"lineNo"`
	SKU            string `json:"sku"`
	OrderedQty     int64  `json:"orderedQty"`
	UnitPricePaise int64  `json:"unitPricePaise"`
	DeliveredQty   int64  `json:"deliveredQty"`
	AcceptedQty    int64  `json:"acceptedQty"`
	RejectedQty    int64  `json:"rejectedQty"`
	HeldQty        int64  `json:"heldQty"`
}

// TrancheLine is the per-line quantity breakdown of one delivery batch.
type TrancheLine struct {
	LineNo       int64 `json:"lineNo"`
	DeliveredQty int64 `json:"deliveredQty"`
	AcceptedQty  int64 `json:"acceptedQty"`
	RejectedQty  int64 `json:"rejectedQty"`
}

// Approval is one approver signature on a tranche.
type Approval struct {
	Role     Role       `json:"role"`
	UserID   string     `json:"userId"`
	SignedAt *time.Time `json:"signedAt"`
}

// Tranche is one delivery batch. Objection is kept raw: its inner shape is owned by the chaincode.
type Tranche struct {
	DealID             string          `json:"dealId"`
	TrancheNo          int64           `json:"trancheNo"`
	Status             string          `json:"status"`
	Lines              []TrancheLine   `json:"lines"`
	AcceptedValuePaise int64           `json:"acceptedValuePaise"`
	HeldValuePaise     int64           `json:"heldValuePaise"`
	DeliveredAt        *time.Time      `json:"deliveredAt"`
	AcceptedAt         *time.Time      `json:"acceptedAt"`
	DeemedAcceptance   bool            `json:"deemedAcceptance"`
	Approvals          []Approval      `json:"approvals"`
	ReleaseIdemKey     *string         `json:"releaseIdemKey"`
	BankRef            *string         `json:"bankRef"`
	Rail               *string         `json:"rail"`
	MseClockStart      *time.Time      `json:"mseClockStart"`
	MseDeadline        *time.Time      `json:"mseDeadline"`
	HeldExit           *string         `json:"heldExit"`
	Objection          json.RawMessage `json:"objection"`
}

// HeldPortion is the rejected quantity of one line in one tranche, awaiting an exit.
type HeldPortion struct {
	DealID     string  `json:"dealId"`
	TrancheNo  int64   `json:"trancheNo"`
	LineNo     int64   `json:"lineNo"`
	Qty        int64   `json:"qty"`
	ValuePaise int64   `json:"valuePaise"`
	Exit       *string `json:"exit"`
	State      string  `json:"state"`
}

// HistoryEntry is one GetHistoryForKey record.
type HistoryEntry struct {
	TxID      string          `json:"txId"`
	Timestamp *time.Time      `json:"timestamp"`
	IsDelete  bool            `json:"isDelete"`
	Value     json.RawMessage `json:"value"`
}

// MseClock is the MSMED Act §15 payment clock of one tranche.
type MseClock struct {
	DealID        string     `json:"dealId"`
	TrancheNo     int64      `json:"trancheNo"`
	TermDays      int        `json:"termDays"`
	MseClockStart *time.Time `json:"mseClockStart"`
	MseDeadline   *time.Time `json:"mseDeadline"`
	DaysLeft      int64      `json:"daysLeft"`
	Paused        bool       `json:"paused"`
}
