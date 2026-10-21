package main

import (
	"encoding/json"

	"github.com/hyperledger/fabric-contract-api-go/contractapi"

	"github.com/Hansika-hub/Anubandh/chaincode/deal/core"
)

// DealContract is the thin Fabric shell. ALL logic lives in ./core (unit-tested without a peer).
// Function names and argument order are frozen in BUILD.md §5.3.
type DealContract struct{ contractapi.Contract }

type ctxT = contractapi.TransactionContextInterface

// run executes one write transaction and emits its chain event(s) on success.
func run(ctx ctxT, f func(e *core.Engine) error) error {
	l, err := newFabricLedger(ctx)
	if err != nil {
		return err
	}
	e := core.NewEngine(l)
	if err := f(e); err != nil {
		return err
	}
	name, payload, err := core.EncodeEvents(e.Events())
	if err != nil {
		return err
	}
	if name == "" {
		return nil
	}
	// Fabric keeps ONE event per tx; core.EncodeEvents wraps multi-event txs in TxEventBatch.
	return ctx.GetStub().SetEvent(name, payload)
}

// query runs a read-only function and returns its result as JSON text. Returned as string
// (not struct) because contractapi serialises a string verbatim, so the bytes on the wire
// are identical to a struct return while avoiding schema generation for time/RawMessage fields.
func query(ctx ctxT, f func(e *core.Engine) (any, error)) (string, error) {
	l, err := newFabricLedger(ctx)
	if err != nil {
		return "", err
	}
	v, err := f(core.NewEngine(l))
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(v)
	return string(b), err
}

// ---- Deal lifecycle ----
func (c *DealContract) CreateDeal(ctx ctxT, dealJSON string) error {
	return run(ctx, func(e *core.Engine) error { return e.CreateDeal(dealJSON) })
}
func (c *DealContract) ConfirmLien(ctx ctxT, dealID string, lienRef string, amountPaise int64) error {
	return run(ctx, func(e *core.Engine) error { return e.ConfirmLien(dealID, lienRef, amountPaise) })
}
func (c *DealContract) VerifyInvoice(ctx ctxT, dealID string, irn string, idemKey string) error {
	return run(ctx, func(e *core.Engine) error { return e.VerifyInvoice(dealID, irn, idemKey) })
}
func (c *DealContract) ExpireDeal(ctx ctxT, dealID string) error {
	return run(ctx, func(e *core.Engine) error { return e.ExpireDeal(dealID) })
}

// ---- Delivery & QC ----
func (c *DealContract) PostGoodsReceipt(ctx ctxT, dealID string, trancheNo int64, linesJSON string, idemKey string) error {
	return run(ctx, func(e *core.Engine) error { return e.PostGoodsReceipt(dealID, trancheNo, linesJSON, idemKey) })
}
func (c *DealContract) AcknowledgeDelivery(ctx ctxT, dealID string, trancheNo int64) error {
	return run(ctx, func(e *core.Engine) error { return e.AcknowledgeDelivery(dealID, trancheNo) })
}
func (c *DealContract) RecordQualityCheck(ctx ctxT, dealID string, trancheNo int64, resultsJSON string, idemKey string) error {
	return run(ctx, func(e *core.Engine) error { return e.RecordQualityCheck(dealID, trancheNo, resultsJSON, idemKey) })
}

// ---- Approval & release ----
func (c *DealContract) ApproveRelease(ctx ctxT, dealID string, trancheNo int64) error {
	return run(ctx, func(e *core.Engine) error { return e.ApproveRelease(dealID, trancheNo) })
}
func (c *DealContract) ReleaseTranche(ctx ctxT, dealID string, trancheNo int64, idemKey string) error {
	return run(ctx, func(e *core.Engine) error { return e.ReleaseTranche(dealID, trancheNo, idemKey) })
}
func (c *DealContract) ConfirmSettlement(ctx ctxT, dealID string, trancheNo int64, bankRef string, rail string) error {
	return run(ctx, func(e *core.Engine) error { return e.ConfirmSettlement(dealID, trancheNo, bankRef, rail) })
}

// ---- Held portion exits ----
func (c *DealContract) ShipReplacement(ctx ctxT, dealID string, trancheNo int64, lineNo int64, qty int64) error {
	return run(ctx, func(e *core.Engine) error { return e.ShipReplacement(dealID, trancheNo, lineNo, qty) })
}
func (c *DealContract) ProposeCreditNote(ctx ctxT, dealID string, trancheNo int64, lineNo int64, valuePaise int64) error {
	return run(ctx, func(e *core.Engine) error { return e.ProposeCreditNote(dealID, trancheNo, lineNo, valuePaise) })
}
func (c *DealContract) SignCreditNote(ctx ctxT, dealID string, trancheNo int64, lineNo int64) error {
	return run(ctx, func(e *core.Engine) error { return e.SignCreditNote(dealID, trancheNo, lineNo) })
}
func (c *DealContract) ProposePriceAdjustment(ctx ctxT, dealID string, trancheNo int64, lineNo int64, adjustedUnitPricePaise int64) error {
	return run(ctx, func(e *core.Engine) error {
		return e.ProposePriceAdjustment(dealID, trancheNo, lineNo, adjustedUnitPricePaise)
	})
}
func (c *DealContract) SignPriceAdjustment(ctx ctxT, dealID string, trancheNo int64, lineNo int64) error {
	return run(ctx, func(e *core.Engine) error { return e.SignPriceAdjustment(dealID, trancheNo, lineNo) })
}

// ---- Disputes & objections ----
func (c *DealContract) RaiseObjection(ctx ctxT, dealID string, trancheNo int64, reason string) error {
	return run(ctx, func(e *core.Engine) error { return e.RaiseObjection(dealID, trancheNo, reason) })
}
func (c *DealContract) ResolveObjection(ctx ctxT, dealID string, trancheNo int64) error {
	return run(ctx, func(e *core.Engine) error { return e.ResolveObjection(dealID, trancheNo) })
}
func (c *DealContract) RaiseDispute(ctx ctxT, dealID string, trancheNo int64, lineNo int64, reason string) error {
	return run(ctx, func(e *core.Engine) error { return e.RaiseDispute(dealID, trancheNo, lineNo, reason) })
}
func (c *DealContract) ResolveDispute(ctx ctxT, dealID string, trancheNo int64, lineNo int64, resolution string) error {
	return run(ctx, func(e *core.Engine) error { return e.ResolveDispute(dealID, trancheNo, lineNo, resolution) })
}
func (c *DealContract) ApplyDisputeTimeout(ctx ctxT, dealID string, trancheNo int64, lineNo int64) error {
	return run(ctx, func(e *core.Engine) error { return e.ApplyDisputeTimeout(dealID, trancheNo, lineNo) })
}

// ---- Time rules ----
func (c *DealContract) TickDeadlines(ctx ctxT, dealID string) error {
	return run(ctx, func(e *core.Engine) error { return e.TickDeadlines(dealID) })
}

// ---- Queries (evaluate) ----
func (c *DealContract) GetDeal(ctx ctxT, dealID string) (string, error) {
	return query(ctx, func(e *core.Engine) (any, error) { return e.GetDeal(dealID) })
}
func (c *DealContract) GetTranche(ctx ctxT, dealID string, trancheNo int64) (string, error) {
	return query(ctx, func(e *core.Engine) (any, error) { return e.GetTranche(dealID, trancheNo) })
}
func (c *DealContract) ListTranches(ctx ctxT, dealID string) (string, error) {
	return query(ctx, func(e *core.Engine) (any, error) { return e.ListTranches(dealID) })
}
func (c *DealContract) GetHeldPortions(ctx ctxT, dealID string) (string, error) {
	return query(ctx, func(e *core.Engine) (any, error) { return e.GetHeldPortions(dealID) })
}
func (c *DealContract) GetDealHistory(ctx ctxT, dealID string) (string, error) {
	return query(ctx, func(e *core.Engine) (any, error) { return e.GetDealHistory(dealID) })
}
func (c *DealContract) QueryDealsByStatus(ctx ctxT, status string) (string, error) {
	return query(ctx, func(e *core.Engine) (any, error) { return e.QueryDealsByStatus(status) })
}
func (c *DealContract) QueryMseDealsNearDeadline(ctx ctxT, withinDays int64) (string, error) {
	return query(ctx, func(e *core.Engine) (any, error) { return e.QueryMseDealsNearDeadline(withinDays) })
}
func (c *DealContract) GetMseClock(ctx ctxT, dealID string, trancheNo int64) (string, error) {
	return query(ctx, func(e *core.Engine) (any, error) { return e.GetMseClock(dealID, trancheNo) })
}
