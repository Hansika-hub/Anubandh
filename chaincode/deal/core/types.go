package core

import (
	"encoding/json"
	"time"
)

type (
	DealStatus    string
	TrancheStatus string
	HeldExit      string
	Org           string
	Role          string
	MseCategory   string
)

const (
	DealCreated           DealStatus    = "CREATED"
	DealFunded            DealStatus    = "FUNDED"
	DealDelivered         DealStatus    = "DELIVERED"
	DealAccepted          DealStatus    = "ACCEPTED"
	DealPartiallyAccepted DealStatus    = "PARTIALLY_ACCEPTED"
	DealReleased          DealStatus    = "RELEASED"
	DealSettled           DealStatus    = "SETTLED"
	DealDisputed          DealStatus    = "DISPUTED"
	DealExpired           DealStatus    = "EXPIRED"
	DealClosed            DealStatus    = "CLOSED"
	TrPending             TrancheStatus = "PENDING"
	TrDelivered           TrancheStatus = "DELIVERED"
	TrAccepted            TrancheStatus = "ACCEPTED"
	TrDeemedAccepted      TrancheStatus = "DEEMED_ACCEPTED"
	TrApproved            TrancheStatus = "APPROVED"
	TrReleased            TrancheStatus = "RELEASED"
	TrSettled             TrancheStatus = "SETTLED"
	TrHeld                TrancheStatus = "HELD"
	TrClosed              TrancheStatus = "CLOSED"
	ExitReplacement       HeldExit      = "REPLACEMENT"
	ExitCreditNote        HeldExit      = "CREDIT_NOTE"
	ExitPriceAdjustment   HeldExit      = "PRICE_ADJUSTMENT"
	ExitDispute           HeldExit      = "DISPUTE"
	OrgBuyer              Org           = "BUYER"
	OrgSupplier           Org           = "SUPPLIER"
	OrgBank               Org           = "BANK"
	OrgAuditor            Org           = "AUDITOR"
	RoleWarehouse         Role          = "WAREHOUSE"
	RolePlantHead         Role          = "PLANT_HEAD"
	RoleCFO               Role          = "CFO"
	RoleProcurement       Role          = "PROCUREMENT"
	RoleTreasurer         Role          = "TREASURER"
	RoleSupplierUser      Role          = "SUPPLIER_USER"
	RoleBankOfficer       Role          = "BANK_OFFICER"
	RoleArbiter           Role          = "ARBITER"
	RoleAuditor           Role          = "AUDITOR"
	MseMicro              MseCategory   = "MICRO"
	MseSmall              MseCategory   = "SMALL"
	MseMedium             MseCategory   = "MEDIUM"
	MseNone               MseCategory   = "NONE"
	ResolutionRelease                   = "RELEASE_TO_SUPPLIER"
	ResolutionRefund                    = "REFUND_TO_BUYER"
	HeldStateHeld                       = "HELD"
	HeldStateReplacing                  = "REPLACING"
	HeldStateDisputed                   = "DISPUTED"
	HeldStateClosed                     = "CLOSED"
	PrivateCollection                   = "collBuyerSupplierBank"
)

// MSE: BUILD §4.2 plus ONE additive field (writtenAgreement) — see CONTRACT_CHANGE_001.
type MSE struct {
	Category         MseCategory `json:"category"`
	TermDays         int         `json:"termDays"`
	UdyamNo          string      `json:"udyamNo"`
	Applies          bool        `json:"applies"`
	WrittenAgreement bool        `json:"writtenAgreement,omitempty"`
}

type PolicyBand struct {
	MinPaise      int64  `json:"minPaise"`
	RequiredRoles []Role `json:"requiredRoles"`
}

type Invoice struct {
	IRN      *string `json:"irn"`
	Verified bool    `json:"verified"`
}

type Deal struct {
	DealID             string       `json:"dealId"`
	PONumber           string       `json:"poNumber"`
	BuyerID            string       `json:"buyerId"`
	SupplierID         string       `json:"supplierId"`
	Status             DealStatus   `json:"status"`
	Currency           string       `json:"currency"`
	LienAmountPaise    int64        `json:"lienAmountPaise"`
	LienRef            string       `json:"lienRef"`
	TotalReleasedPaise int64        `json:"totalReleasedPaise"`
	TotalHeldPaise     int64        `json:"totalHeldPaise"`
	MSE                MSE          `json:"mse"`
	ApprovalPolicy     []PolicyBand `json:"approvalPolicy"`
	ToleranceBps       int64        `json:"toleranceBps"`
	DeliveryDeadline   time.Time    `json:"deliveryDeadline"`
	Invoice            Invoice      `json:"invoice"`
	SupplierAccountRef string       `json:"supplierAccountRef,omitempty"` // additive, CC-001
	CreatedAt          time.Time    `json:"createdAt"`
	UpdatedAt          time.Time    `json:"updatedAt"`
}

// Line lives in the PRIVATE collection (price + terms).
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

type TrancheLine struct {
	LineNo                 int64 `json:"lineNo"`
	DeliveredQty           int64 `json:"deliveredQty"`
	AcceptedQty            int64 `json:"acceptedQty"`
	RejectedQty            int64 `json:"rejectedQty"`
	AdjustedUnitPricePaise int64 `json:"adjustedUnitPricePaise,omitempty"` // additive, CC-001
}

type Approval struct {
	Role     Role      `json:"role"`
	UserID   string    `json:"userId"`
	SignedAt time.Time `json:"signedAt"`
}

type Objection struct {
	Reason           string     `json:"reason"`
	RaisedAt         time.Time  `json:"raisedAt"`
	BuyerSignedAt    *time.Time `json:"buyerSignedAt,omitempty"`
	SupplierSignedAt *time.Time `json:"supplierSignedAt,omitempty"`
	ResolvedAt       *time.Time `json:"resolvedAt,omitempty"`
}

type Ref struct {
	TrancheNo int64 `json:"trancheNo"`
	LineNo    int64 `json:"lineNo"`
}

type Tranche struct {
	DealID             string        `json:"dealId"`
	TrancheNo          int64         `json:"trancheNo"`
	Status             TrancheStatus `json:"status"`
	Lines              []TrancheLine `json:"lines"`
	AcceptedValuePaise int64         `json:"acceptedValuePaise"`
	HeldValuePaise     int64         `json:"heldValuePaise"`
	DeliveredAt        *time.Time    `json:"deliveredAt"`
	AcceptedAt         *time.Time    `json:"acceptedAt"`
	DeemedAcceptance   bool          `json:"deemedAcceptance"`
	Approvals          []Approval    `json:"approvals"`
	ReleaseIdemKey     *string       `json:"releaseIdemKey"`
	BankRef            *string       `json:"bankRef"`
	Rail               *string       `json:"rail"`
	MseClockStart      *time.Time    `json:"mseClockStart"`
	MseDeadline        *time.Time    `json:"mseDeadline"`
	HeldExit           *HeldExit     `json:"heldExit"`
	Objection          *Objection    `json:"objection"`
	// additive (CC-001)
	AckedAt    *time.Time `json:"ackedAt,omitempty"`
	ReleasedAt *time.Time `json:"releasedAt,omitempty"`
	SettledAt  *time.Time `json:"settledAt,omitempty"`
	Replaces   *Ref       `json:"replaces,omitempty"`
	MseAlerted bool       `json:"mseAlerted,omitempty"`
}

type Proposal struct {
	Kind                   HeldExit `json:"kind"`
	ProposedBy             Org      `json:"proposedBy"`
	ValuePaise             int64    `json:"valuePaise,omitempty"`
	AdjustedUnitPricePaise int64    `json:"adjustedUnitPricePaise,omitempty"`
}

type HeldPortion struct {
	DealID     string    `json:"dealId"`
	TrancheNo  int64     `json:"trancheNo"`
	LineNo     int64     `json:"lineNo"`
	Qty        int64     `json:"qty"`
	ValuePaise int64     `json:"valuePaise"`
	Exit       *HeldExit `json:"exit"`
	State      string    `json:"state"`
	// additive (CC-001)
	Proposal        *Proposal  `json:"proposal,omitempty"`
	DisputeRaisedAt *time.Time `json:"disputeRaisedAt,omitempty"`
	ReplacementNo   int64      `json:"replacementTrancheNo,omitempty"`
}

type HistoryEntry struct {
	TxID      string          `json:"txId"`
	Timestamp time.Time       `json:"timestamp"`
	IsDelete  bool            `json:"isDelete"`
	Value     json.RawMessage `json:"value"`
}

// MseClock is returned by GetMseClock. Not defined in BUILD §4 (only referenced) — see CC-001.
type MseClock struct {
	DealID             string     `json:"dealId"`
	TrancheNo          int64      `json:"trancheNo"`
	Applies            bool       `json:"applies"`
	TermDays           int        `json:"termDays"`
	State              string     `json:"state"` // NOT_APPLICABLE | NOT_STARTED | RUNNING | OVERDUE | PAID
	ClockStart         *time.Time `json:"clockStart"`
	Deadline           *time.Time `json:"deadline"`
	DaysLeft           int64      `json:"daysLeft"` // negative once overdue
	DeemedAcceptanceAt *time.Time `json:"deemedAcceptanceAt"`
}

// Event is one logical chain event (BUILD §6). The adapter emits it via SetEvent.
type Event struct {
	Name    string         `json:"name"`
	Payload map[string]any `json:"payload"`
}
