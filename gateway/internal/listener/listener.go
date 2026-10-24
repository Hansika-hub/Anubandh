// Package listener consumes chain events from a checkpoint and fans them out to the projector,
// the payment outbox and the live SSE feed.
package listener

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Hansika-hub/anubandh/gateway/internal/apperr"
	"github.com/Hansika-hub/anubandh/gateway/internal/bank"
	"github.com/Hansika-hub/anubandh/gateway/internal/events"
	"github.com/Hansika-hub/anubandh/gateway/internal/fabric"
	"github.com/Hansika-hub/anubandh/gateway/internal/model"
	"github.com/Hansika-hub/anubandh/gateway/internal/projector"
	"github.com/Hansika-hub/anubandh/gateway/internal/store"
)

// Options configures a Listener.
type Options struct {
	ListenerID       string // chain_checkpoint.listener_id
	StartBlock       uint64 // first block when no checkpoint exists (0 = replay the whole channel)
	PoisonRetries    int    // attempts before a non-transient failure is recorded and skipped
	OnApprovalSigned func(dealID string, trancheNo int64)
}

// Status is the listener's health snapshot.
type Status struct {
	Running     bool       `json:"running"`
	BlockNo     uint64     `json:"blockNo"`
	TxIndex     int        `json:"txIndex"`
	Processed   int64      `json:"processed"`
	Skipped     int64      `json:"skipped"`
	LastEventAt *time.Time `json:"lastEventAt"`
	LastError   string     `json:"lastError,omitempty"`
}

// Listener is the checkpointed chain event consumer.
//
// Delivery is at-least-once from the chain; effects are exactly-once because the projection,
// the outbox row and the checkpoint commit in one database transaction, and deal_events is
// unique on (txId, eventName).
type Listener struct {
	client  fabric.Client
	id      fabric.Identity
	store   *store.Store
	proj    *projector.Projector
	adapter *bank.Adapter
	hub     *events.Hub
	opts    Options
	log     *slog.Logger

	runCtx context.Context
	pos    store.Checkpoint

	mu     sync.Mutex
	status Status
	dirty  map[string]struct{}
}

// New builds a Listener. id signs the event stream request (any channel member may read events).
func New(c fabric.Client, id fabric.Identity, st *store.Store, proj *projector.Projector, adapter *bank.Adapter, hub *events.Hub, opts Options, log *slog.Logger) *Listener {
	if opts.ListenerID == "" {
		opts.ListenerID = "gateway"
	}
	if opts.PoisonRetries <= 0 {
		opts.PoisonRetries = 3
	}
	if log == nil {
		log = slog.Default()
	}
	return &Listener{client: c, id: id, store: st, proj: proj, adapter: adapter, hub: hub, opts: opts, log: log, dirty: map[string]struct{}{}}
}

// Status returns a snapshot for /health.
func (l *Listener) Status() Status {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.status
}

func (l *Listener) setErr(err error) {
	l.mu.Lock()
	if err != nil {
		l.status.LastError = err.Error()
	} else {
		l.status.LastError = ""
	}
	l.mu.Unlock()
}

// Run calls Start in a loop with backoff until ctx ends, so a dropped peer connection or a
// restarted network never needs a gateway restart.
func (l *Listener) Run(ctx context.Context) {
	go l.reconcileLoop(ctx)
	backoff := time.Second
	for ctx.Err() == nil {
		err := l.Start(ctx)
		if ctx.Err() != nil {
			return
		}
		l.setErr(err)
		l.log.Warn("chain event stream ended; reconnecting", "err", err, "inMs", backoff.Milliseconds())
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// Start resumes from chain_checkpoint and processes events until the stream ends (BUILD.md §8.2).
func (l *Listener) Start(ctx context.Context) error {
	cp, found, err := store.GetCheckpoint(ctx, l.store.Pool, l.opts.ListenerID)
	if err != nil {
		return fmt.Errorf("load checkpoint: %w", err)
	}
	if !found {
		cp = store.Checkpoint{BlockNo: l.opts.StartBlock}
	}
	stream, err := l.client.ChaincodeEvents(ctx, l.id, cp.BlockNo)
	if err != nil {
		return fmt.Errorf("open event stream: %w", err)
	}
	l.runCtx = ctx
	l.mu.Lock()
	l.status.Running, l.status.BlockNo, l.status.TxIndex = true, cp.BlockNo, cp.TxIndex
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		l.status.Running = false
		l.mu.Unlock()
	}()
	l.log.Info("chain listener started", "listenerId", l.opts.ListenerID, "fromBlock", cp.BlockNo, "skip", cp.TxIndex, "mode", l.client.Mode())

	block, ordinal := cp.BlockNo, 0
	for ev := range stream {
		if ev.BlockNumber != block {
			block, ordinal = ev.BlockNumber, 0
		}
		ordinal++
		if block == cp.BlockNo && ordinal <= cp.TxIndex {
			continue // already handled before the restart
		}
		l.pos = store.Checkpoint{BlockNo: block, TxIndex: ordinal}
		if err := l.handleWithRetry(ctx, ev); err != nil {
			return err
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.New("event stream closed by peer")
}

func (l *Listener) handleWithRetry(ctx context.Context, ev *fabric.ChaincodeEvent) error {
	delay := 200 * time.Millisecond
	for attempt := 1; ; attempt++ {
		err := l.handle(ev)
		if err == nil {
			l.setErr(nil)
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		l.setErr(err)
		if isDataError(err) && attempt >= l.opts.PoisonRetries {
			return l.skip(ctx, ev, err)
		}
		l.log.Warn("event handling failed; retrying", "event", ev.EventName, "block", ev.BlockNumber, "txId", ev.TransactionID, "attempt", attempt, "err", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		if delay < 10*time.Second {
			delay *= 2
		}
	}
}

// dataError marks failures caused by the event content itself; retrying cannot fix them.
type dataError struct{ err error }

func (e *dataError) Error() string { return e.err.Error() }
func (e *dataError) Unwrap() error { return e.err }

// classify separates content errors (bad payload, constraint violation, invalid amount) from
// infrastructure errors (database or peer unreachable), which are retried until they clear.
func classify(err error) error {
	if err == nil || errors.Is(err, context.Canceled) {
		return err
	}
	if errors.Is(err, projector.ErrBadPayload) {
		return &dataError{err}
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && !store.IsTransient(err) {
		return &dataError{err}
	}
	if ae, ok := apperr.As(err); ok && (ae.Code == apperr.ErrBadRequest || ae.Code == apperr.ErrNotFound) {
		return &dataError{err}
	}
	return err
}

func isDataError(err error) bool {
	var de *dataError
	return errors.As(err, &de)
}

// skip records an event that cannot be projected (so the stream is not blocked forever) and
// advances the checkpoint. It is logged at ERROR and counted in /health.
func (l *Listener) skip(ctx context.Context, ev *fabric.ChaincodeEvent, cause error) error {
	l.log.Error("CRITICAL: chain event could not be projected; recorded raw and skipped",
		"event", ev.EventName, "block", ev.BlockNumber, "txId", ev.TransactionID, "err", cause)
	env, _ := projector.ParseEnvelope(ev.Payload)
	block := ev.BlockNumber
	err := l.store.InTx(ctx, func(tx pgx.Tx) error {
		if _, _, err := store.InsertEvent(ctx, tx, store.EventRow{DealID: env.DealID, EventName: ev.EventName, Payload: ev.Payload,
			TxID: ev.TransactionID, BlockNo: &block, Ts: time.Now().UTC()}); err != nil {
			return err
		}
		return store.SaveCheckpoint(ctx, tx, l.opts.ListenerID, l.pos)
	})
	if err != nil {
		return err
	}
	l.mu.Lock()
	l.status.Skipped++
	l.status.BlockNo, l.status.TxIndex = l.pos.BlockNo, l.pos.TxIndex
	l.mu.Unlock()
	if env.DealID != "" {
		l.markDirty(env.DealID)
	}
	return nil
}

// handle projects one event, enqueues payments and advances the checkpoint atomically, then
// reconciles the deal from the ledger and publishes the event to SSE subscribers.
func (l *Listener) handle(ev *fabric.ChaincodeEvent) error {
	ctx := l.runCtx
	if ctx == nil {
		ctx = context.Background()
	}
	block := ev.BlockNumber
	var res projector.Result
	err := l.store.InTx(ctx, func(tx pgx.Tx) error {
		var err error
		res, err = l.proj.ApplyTx(ctx, tx, ev.EventName, ev.Payload, projector.Meta{TxID: ev.TransactionID, BlockNo: &block})
		if err != nil {
			return err
		}
		if res.Applied && ev.EventName == model.EvTrancheReleased && l.adapter != nil {
			rel, err := l.releasedEvent(ctx, res.Envelope)
			if err != nil {
				return err
			}
			if err := l.adapter.EnqueueTx(ctx, tx, rel); err != nil {
				return err
			}
		}
		return store.SaveCheckpoint(ctx, tx, l.opts.ListenerID, l.pos)
	})
	if err != nil {
		return classify(err)
	}
	now := time.Now().UTC()
	l.mu.Lock()
	l.status.Processed++
	l.status.BlockNo, l.status.TxIndex, l.status.LastEventAt = l.pos.BlockNo, l.pos.TxIndex, &now
	l.mu.Unlock()

	dealID := res.Envelope.DealID
	if dealID != "" {
		rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if err := l.proj.Reconcile(rctx, dealID); err != nil {
			l.log.Warn("reconcile after event failed; will retry", "dealId", dealID, "err", err)
			l.markDirty(dealID)
		}
		cancel()
	}
	if res.Applied && l.hub != nil {
		l.hub.Publish(events.Message{ID: res.EventID, Event: ev.EventName, DealID: dealID, Data: sseData(res.EventID, ev, res.Envelope)})
	}
	if res.Applied && ev.EventName == model.EvApprovalRecorded && l.opts.OnApprovalSigned != nil &&
		res.Envelope.Satisfied != nil && *res.Envelope.Satisfied && res.Envelope.TrancheNo != nil {
		go l.opts.OnApprovalSigned(dealID, *res.Envelope.TrancheNo)
	}
	return nil
}

// releasedEvent builds the outbox input. If the payload lacks the amount, the amount is read
// from the ledger tranche (computed on-chain), never guessed.
func (l *Listener) releasedEvent(ctx context.Context, e model.EventEnvelope) (bank.TrancheReleasedEvent, error) {
	if e.TrancheNo == nil {
		return bank.TrancheReleasedEvent{}, fmt.Errorf("%w: TrancheReleased without trancheNo", projector.ErrBadPayload)
	}
	rel := bank.TrancheReleasedEvent{DealID: e.DealID, TxID: e.TxID, TrancheNo: *e.TrancheNo, SupplierID: e.SupplierID,
		SupplierAccountRef: e.SupplierAccountRef, Rail: model.Rail(e.Rail)}
	if e.Ts != nil {
		rel.Ts = *e.Ts
	}
	if e.AmountPaise != nil {
		rel.AmountPaise = *e.AmountPaise
	}
	if rel.AmountPaise > 0 && (rel.SupplierAccountRef != "" || rel.SupplierID != "") {
		return rel, nil
	}
	sub := fabric.Default()
	if sub == nil {
		return rel, fmt.Errorf("%w: TrancheReleased payload incomplete and no chain reader", projector.ErrBadPayload)
	}
	rctx := fabric.WithIdentity(ctx, l.id)
	if rel.AmountPaise <= 0 {
		b, err := sub.Evaluate(rctx, "GetTranche", e.DealID, fmt.Sprint(rel.TrancheNo))
		if err != nil {
			return rel, err
		}
		var t model.Tranche
		if err := json.Unmarshal(b, &t); err != nil {
			return rel, fmt.Errorf("%w: GetTranche: %w", projector.ErrBadPayload, err)
		}
		rel.AmountPaise = t.AcceptedValuePaise
	}
	if rel.SupplierID == "" && rel.SupplierAccountRef == "" {
		b, err := sub.Evaluate(rctx, "GetDeal", e.DealID)
		if err != nil {
			return rel, err
		}
		var d model.Deal
		if err := json.Unmarshal(b, &d); err != nil {
			return rel, fmt.Errorf("%w: GetDeal: %w", projector.ErrBadPayload, err)
		}
		rel.SupplierID = d.SupplierID
	}
	return rel, nil
}

func sseData(id int64, ev *fabric.ChaincodeEvent, env model.EventEnvelope) []byte {
	payload := json.RawMessage(ev.Payload)
	if !json.Valid(payload) {
		payload = json.RawMessage("null")
	}
	b, _ := json.Marshal(map[string]any{
		"id": id, "eventName": ev.EventName, "dealId": env.DealID, "trancheNo": env.TrancheNo,
		"txId": ev.TransactionID, "blockNo": ev.BlockNumber, "ts": env.Ts, "payload": payload,
	})
	return b
}

func (l *Listener) markDirty(dealID string) {
	l.mu.Lock()
	l.dirty[dealID] = struct{}{}
	l.mu.Unlock()
}

// reconcileLoop retries reconciles that failed (e.g. a peer was briefly unreachable).
func (l *Listener) reconcileLoop(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		l.mu.Lock()
		ids := make([]string, 0, len(l.dirty))
		for id := range l.dirty {
			ids = append(ids, id)
		}
		l.mu.Unlock()
		for _, id := range ids {
			rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := l.proj.Reconcile(rctx, id)
			cancel()
			if err == nil {
				l.mu.Lock()
				delete(l.dirty, id)
				l.mu.Unlock()
			}
		}
	}
}
