// Package projector maintains the PostgreSQL read model from chain events.
//
// Two layers, so the read model is right regardless of how rich each event payload is:
//  1. ApplyTx applies the event payload (BUILD.md §6) with forward-only guards, records the event
//     once in deal_events, and is atomic with the listener checkpoint and the payment outbox.
//  2. Reconcile re-reads the deal from the ledger (GetDeal / ListTranches / GetHeldPortions) and
//     overwrites the projection. Fabric allows one chaincode event per transaction, so a
//     transaction that changes several things (e.g. QC creating held portions) announces only
//     one of them; reconciling from the ledger closes that gap.
package projector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Hansika-hub/anubandh/gateway/internal/apperr"
	"github.com/Hansika-hub/anubandh/gateway/internal/fabric"
	"github.com/Hansika-hub/anubandh/gateway/internal/model"
	"github.com/Hansika-hub/anubandh/gateway/internal/store"
)

// Reader is the read-only chain access the projector needs (satisfied by *fabric.Submitter).
type Reader interface {
	Evaluate(ctx context.Context, fn string, args ...string) ([]byte, error)
}

// Projector applies chain events to the read model.
type Projector struct {
	store  *store.Store
	reader Reader
	id     fabric.Identity
	log    *slog.Logger
}

// New builds a Projector. reader may be nil (payload-only projection).
func New(st *store.Store, reader Reader, id fabric.Identity, log *slog.Logger) *Projector {
	if log == nil {
		log = slog.Default()
	}
	return &Projector{store: st, reader: reader, id: id, log: log}
}

// Meta is the event's position on the chain.
type Meta struct {
	TxID    string
	BlockNo *uint64
}

// Result reports what ApplyTx did.
type Result struct {
	EventID  int64
	Applied  bool // false: this (txId, event) was already projected
	Envelope model.EventEnvelope
}

// Apply projects one event in its own transaction and reconciles the deal (BUILD.md §8.2).
func (p *Projector) Apply(eventName string, payload []byte) error {
	ctx := context.Background()
	var res Result
	err := p.store.InTx(ctx, func(tx pgx.Tx) error {
		var err error
		res, err = p.ApplyTx(ctx, tx, eventName, payload, Meta{})
		return err
	})
	if err != nil {
		return err
	}
	if res.Envelope.DealID != "" {
		return p.Reconcile(ctx, res.Envelope.DealID)
	}
	return nil
}

// ErrBadPayload marks an event whose payload cannot be projected. Retrying will not help.
var ErrBadPayload = errors.New("bad event payload")

// ParseEnvelope decodes the common §6 payload fields.
func ParseEnvelope(payload []byte) (model.EventEnvelope, error) {
	var env model.EventEnvelope
	if len(payload) == 0 {
		return env, fmt.Errorf("%w: empty", ErrBadPayload)
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		return env, fmt.Errorf("%w: not valid JSON: %w", ErrBadPayload, err)
	}
	return env, nil
}

// ApplyTx projects one event inside tx. A payload that does not parse is still recorded in
// deal_events (as raw text) so nothing is silently lost, and is reported as an error.
func (p *Projector) ApplyTx(ctx context.Context, tx pgx.Tx, eventName string, payload []byte, meta Meta) (Result, error) {
	env, perr := ParseEnvelope(payload)
	res := Result{Envelope: env}
	if meta.TxID == "" {
		meta.TxID = env.TxID
	}
	ts := time.Now().UTC()
	if env.Ts != nil {
		ts = env.Ts.UTC()
	}
	if env.DealID != "" {
		if err := store.LockDeal(ctx, tx, env.DealID); err != nil {
			return res, err
		}
	}
	id, inserted, err := store.InsertEvent(ctx, tx, store.EventRow{
		DealID: env.DealID, TrancheNo: env.TrancheNo, EventName: eventName, Payload: payload,
		TxID: meta.TxID, BlockNo: meta.BlockNo, Ts: ts,
	})
	if err != nil {
		return res, err
	}
	res.EventID, res.Applied = id, inserted
	if perr != nil {
		return res, perr
	}
	if !inserted || env.DealID == "" {
		return res, nil
	}
	return res, p.applyPayload(ctx, tx, eventName, env, ts)
}

func (p *Projector) applyPayload(ctx context.Context, tx pgx.Tx, name string, e model.EventEnvelope, ts time.Time) error {
	tn := func() (int64, error) {
		if e.TrancheNo == nil || *e.TrancheNo < 1 {
			return 0, fmt.Errorf("%w: %s has no trancheNo", ErrBadPayload, name)
		}
		return *e.TrancheNo, nil
	}
	upd := func(u store.TrancheUpdate) error {
		n, err := tn()
		if err != nil {
			return err
		}
		return store.ApplyTrancheUpdate(ctx, tx, e.DealID, n, u)
	}
	heldExit := func(exit, state string) error {
		n, err := tn()
		if err != nil {
			return err
		}
		if e.LineNo == nil {
			return fmt.Errorf("%w: %s has no lineNo", ErrBadPayload, name)
		}
		return store.SetHeldExit(ctx, tx, e.DealID, n, *e.LineNo, exit, state)
	}
	switch name {
	case model.EvDealCreated:
		d := model.Deal{DealID: e.DealID, BuyerID: e.BuyerID, SupplierID: e.SupplierID, Status: model.DealCreated, CreatedAt: &ts}
		if e.LienAmountPaise != nil {
			d.LienAmountPaise = *e.LienAmountPaise
		}
		return store.EnsureDeal(ctx, tx, d)
	case model.EvLienConfirmed:
		if err := store.SetLienRef(ctx, tx, e.DealID, e.LienRef); err != nil {
			return err
		}
		return store.AdvanceDealStatus(ctx, tx, e.DealID, model.DealFunded, ts)
	case model.EvInvoiceVerified:
		return store.SetInvoice(ctx, tx, e.DealID, e.IRN)
	case model.EvGoodsReceived:
		if err := upd(store.TrancheUpdate{Status: model.TrancheDelivered, DeliveredAt: &ts}); err != nil {
			return err
		}
		return store.AdvanceDealStatus(ctx, tx, e.DealID, model.DealDelivered, ts)
	case model.EvQualityRecorded:
		status := model.TrancheAccepted
		if e.AcceptedValuePaise != nil && *e.AcceptedValuePaise == 0 {
			status = model.TrancheHeld
		}
		return upd(store.TrancheUpdate{Status: status, AcceptedAt: &ts, AcceptedValue: e.AcceptedValuePaise, HeldValue: e.HeldValuePaise})
	case model.EvHeldPortionCreated:
		n, err := tn()
		if err != nil {
			return err
		}
		if e.LineNo == nil {
			return fmt.Errorf("%w: %s has no lineNo", ErrBadPayload, name)
		}
		h := model.HeldPortion{DealID: e.DealID, TrancheNo: n, LineNo: *e.LineNo, State: model.TrancheHeld}
		if e.Qty != nil {
			h.Qty = *e.Qty
		}
		if e.ValuePaise != nil {
			h.ValuePaise = *e.ValuePaise
		}
		return store.UpsertHeldPortion(ctx, tx, h)
	case model.EvTrancheAccepted:
		f := false
		return upd(store.TrancheUpdate{Status: model.TrancheAccepted, AcceptedAt: &ts, Deemed: &f, MseClockStart: e.MseClockStart, MseDeadline: e.MseDeadline})
	case model.EvDeemedAcceptance:
		t := true
		acc := ts
		if e.MseClockStart != nil {
			acc = *e.MseClockStart
		}
		return upd(store.TrancheUpdate{Status: model.TrancheDeemedAccepted, AcceptedAt: &acc, Deemed: &t, MseClockStart: e.MseClockStart, MseDeadline: e.MseDeadline})
	case model.EvMseClockStarted:
		return upd(store.TrancheUpdate{MseClockStart: e.MseClockStart, MseDeadline: e.MseDeadline})
	case model.EvMseClockRestarted:
		return upd(store.TrancheUpdate{MseClockStart: e.MseClockStart, MseDeadline: e.MseDeadline, RestartClock: true})
	case model.EvApprovalRecorded:
		n, err := tn()
		if err != nil {
			return err
		}
		if e.Role != "" {
			if err := store.UpsertApproval(ctx, tx, e.DealID, n, string(e.Role), e.UserID, ts); err != nil {
				return err
			}
		}
		if e.Satisfied != nil && *e.Satisfied {
			return upd(store.TrancheUpdate{Status: model.TrancheApproved})
		}
		return nil
	case model.EvTrancheReleased:
		return upd(store.TrancheUpdate{Status: model.TrancheReleased, ReleasedAt: &ts, Rail: e.Rail})
	case model.EvSettlementConfirmed:
		return upd(store.TrancheUpdate{Status: model.TrancheSettled, SettledAt: &ts, BankRef: e.BankRef, Rail: e.Rail})
	case model.EvReplacementShipped:
		if err := heldExit(model.ExitReplacement, ""); err != nil {
			return err
		}
		if e.NewTrancheNo != nil && *e.NewTrancheNo >= 1 {
			return store.ApplyTrancheUpdate(ctx, tx, e.DealID, *e.NewTrancheNo, store.TrancheUpdate{Status: model.TranchePending})
		}
		return nil
	case model.EvCreditNoteSigned:
		return heldExit(model.ExitCreditNote, model.TrancheClosed)
	case model.EvPriceAdjustmentSigned:
		return heldExit(model.ExitPriceAdjustment, model.TrancheClosed)
	case model.EvDisputeRaised:
		return heldExit(model.ExitDispute, "")
	case model.EvDisputeResolved:
		return heldExit(model.ExitDispute, model.TrancheClosed)
	case model.EvDealExpired:
		return store.AdvanceDealStatus(ctx, tx, e.DealID, model.DealExpired, ts)
	case model.EvDeliveryAcknowledged, model.EvMseDeadlineApproaching, model.EvCreditNoteProposed,
		model.EvPriceAdjustmentProposed, model.EvObjectionRaised, model.EvObjectionResolved:
		return nil // recorded in deal_events; state is picked up by Reconcile
	default:
		p.log.Warn("unknown chain event recorded without projection", "event", name, "dealId", e.DealID)
		return nil
	}
}

// Reconcile overwrites one deal's projection with the ledger's current state.
func (p *Projector) Reconcile(ctx context.Context, dealID string) error {
	if p.reader == nil || dealID == "" {
		return nil
	}
	ctx = fabric.WithIdentity(ctx, p.id)
	dealB, err := p.reader.Evaluate(ctx, "GetDeal", dealID)
	if err != nil {
		if apperr.Is(err, apperr.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("reconcile %s: GetDeal: %w", dealID, err)
	}
	trB, err := p.reader.Evaluate(ctx, "ListTranches", dealID)
	if err != nil {
		return fmt.Errorf("reconcile %s: ListTranches: %w", dealID, err)
	}
	heldB, err := p.reader.Evaluate(ctx, "GetHeldPortions", dealID)
	if err != nil {
		return fmt.Errorf("reconcile %s: GetHeldPortions: %w", dealID, err)
	}
	var d model.Deal
	if err := json.Unmarshal(dealB, &d); err != nil {
		return fmt.Errorf("reconcile %s: decode deal: %w", dealID, err)
	}
	var tranches []model.Tranche
	if len(trB) > 0 {
		if err := json.Unmarshal(trB, &tranches); err != nil {
			return fmt.Errorf("reconcile %s: decode tranches: %w", dealID, err)
		}
	}
	var held []model.HeldPortion
	if len(heldB) > 0 {
		if err := json.Unmarshal(heldB, &held); err != nil {
			return fmt.Errorf("reconcile %s: decode held portions: %w", dealID, err)
		}
	}
	if d.DealID == "" {
		d.DealID = dealID
	}
	return p.store.InTx(ctx, func(tx pgx.Tx) error {
		return store.ReconcileDeal(ctx, tx, d, tranches, held)
	})
}
