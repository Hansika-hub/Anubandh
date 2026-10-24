package model

import "time"

// Chain event names (BUILD.md §6). Frozen.
const (
	EvDealCreated             = "DealCreated"
	EvLienConfirmed           = "LienConfirmed"
	EvInvoiceVerified         = "InvoiceVerified"
	EvGoodsReceived           = "GoodsReceived"
	EvDeliveryAcknowledged    = "DeliveryAcknowledged"
	EvQualityRecorded         = "QualityRecorded"
	EvHeldPortionCreated      = "HeldPortionCreated"
	EvTrancheAccepted         = "TrancheAccepted"
	EvDeemedAcceptance        = "DeemedAcceptance"
	EvMseClockStarted         = "MseClockStarted"
	EvMseClockRestarted       = "MseClockRestarted"
	EvMseDeadlineApproaching  = "MseDeadlineApproaching"
	EvApprovalRecorded        = "ApprovalRecorded"
	EvTrancheReleased         = "TrancheReleased"
	EvSettlementConfirmed     = "SettlementConfirmed"
	EvReplacementShipped      = "ReplacementShipped"
	EvCreditNoteProposed      = "CreditNoteProposed"
	EvCreditNoteSigned        = "CreditNoteSigned"
	EvPriceAdjustmentProposed = "PriceAdjustmentProposed"
	EvPriceAdjustmentSigned   = "PriceAdjustmentSigned"
	EvObjectionRaised         = "ObjectionRaised"
	EvObjectionResolved       = "ObjectionResolved"
	EvDisputeRaised           = "DisputeRaised"
	EvDisputeResolved         = "DisputeResolved"
	EvDealExpired             = "DealExpired"

	// EvTxEventBatch carries two or more of the events above, in emission order, when one
	// transaction emits several: Fabric keeps one chaincode event per transaction, and a later
	// SetEvent replaces an earlier one. Payload: {dealId, txId, ts, events: [{name, payload}]}.
	// Agreed with the chaincode owner in chaincode/deal/CONTRACT_CHANGE_001.md.
	EvTxEventBatch = "TxEventBatch"
)

// EventEnvelope is the superset of every §6 payload field. Each event fills only its own fields;
// dealId, txId and ts are present on all of them.
type EventEnvelope struct {
	DealID             string        `json:"dealId"`
	TxID               string        `json:"txId"`
	Ts                 *time.Time    `json:"ts"`
	TrancheNo          *int64        `json:"trancheNo,omitempty"`
	NewTrancheNo       *int64        `json:"newTrancheNo,omitempty"`
	LineNo             *int64        `json:"lineNo,omitempty"`
	Qty                *int64        `json:"qty,omitempty"`
	ValuePaise         *int64        `json:"valuePaise,omitempty"`
	AmountPaise        *int64        `json:"amountPaise,omitempty"`
	AcceptedValuePaise *int64        `json:"acceptedValuePaise,omitempty"`
	HeldValuePaise     *int64        `json:"heldValuePaise,omitempty"`
	BuyerID            string        `json:"buyerId,omitempty"`
	SupplierID         string        `json:"supplierId,omitempty"`
	SupplierAccountRef string        `json:"supplierAccountRef,omitempty"`
	LienAmountPaise    *int64        `json:"lienAmountPaise,omitempty"`
	LienRef            string        `json:"lienRef,omitempty"`
	IRN                string        `json:"irn,omitempty"`
	Lines              []TrancheLine `json:"lines,omitempty"`
	Deemed             *bool         `json:"deemed,omitempty"`
	MseClockStart      *time.Time    `json:"mseClockStart,omitempty"`
	MseDeadline        *time.Time    `json:"mseDeadline,omitempty"`
	TermDays           *int          `json:"termDays,omitempty"`
	DaysLeft           *int64        `json:"daysLeft,omitempty"`
	Role               Role          `json:"role,omitempty"`
	UserID             string        `json:"userId,omitempty"`
	Satisfied          *bool         `json:"satisfied,omitempty"`
	BankRef            string        `json:"bankRef,omitempty"`
	Rail               string        `json:"rail,omitempty"`
	Reason             string        `json:"reason,omitempty"`
	Resolution         string        `json:"resolution,omitempty"`
	AdjustedUnitPrice  *int64        `json:"adjustedUnitPricePaise,omitempty"`
}
