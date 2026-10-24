package bank

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Hansika-hub/anubandh/gateway/internal/apperr"
	"github.com/Hansika-hub/anubandh/gateway/internal/fabric"
	"github.com/Hansika-hub/anubandh/gateway/internal/model"
	"github.com/Hansika-hub/anubandh/gateway/internal/store"
)

// TrancheReleasedEvent is the TrancheReleased chain event payload (BUILD.md §6).
type TrancheReleasedEvent struct {
	DealID             string    `json:"dealId"`
	TxID               string    `json:"txId"`
	Ts                 time.Time `json:"ts"`
	TrancheNo          int64     `json:"trancheNo"`
	AmountPaise        int64     `json:"amountPaise"`
	SupplierID         string    `json:"supplierId"`
	SupplierAccountRef string    `json:"supplierAccountRef"`
	Rail               Rail      `json:"rail,omitempty"`
	Urgent             bool      `json:"urgent,omitempty"`
}

// Bank payment statuses returned by sim-bank (BUILD.md §11).
const (
	BankAccepted = "ACCEPTED"
	BankSettled  = "SETTLED"
	BankFailed   = "FAILED"
)

// PaymentInstruction is POST /bank/v1/payments.
type PaymentInstruction struct {
	InstructionID      string `json:"instructionId"`
	DealID             string `json:"dealId"`
	TrancheNo          int64  `json:"trancheNo"`
	AmountPaise        int64  `json:"amountPaise"`
	Rail               Rail   `json:"rail"`
	SupplierAccountRef string `json:"supplierAccountRef"`
}

// PaymentResponse is the bank's answer.
type PaymentResponse struct {
	BankRef string `json:"bankRef"`
	Status  string `json:"status"`
}

// Client is the bank rail interface (sim-bank in the prototype).
type Client interface {
	SendPayment(ctx context.Context, p PaymentInstruction) (PaymentResponse, error)
	PaymentStatus(ctx context.Context, bankRef string) (string, error)
}

// PermanentError marks a bank rejection that retrying will not fix (HTTP 4xx).
type PermanentError struct{ Err error }

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

// HTTPClient calls sim-bank (or a bank API with the same contract).
type HTTPClient struct {
	BaseURL string
	HTTP    *http.Client
}

// NewHTTPClient returns a client with a 10 s timeout.
func NewHTTPClient(baseURL string) *HTTPClient {
	return &HTTPClient{BaseURL: strings.TrimRight(baseURL, "/"), HTTP: &http.Client{Timeout: 10 * time.Second}}
}

func (c *HTTPClient) do(ctx context.Context, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		err := fmt.Errorf("bank %s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(raw)))
		if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode != http.StatusRequestTimeout {
			return &PermanentError{Err: err}
		}
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("bank %s %s: bad JSON: %w", method, path, err)
	}
	return nil
}

// SendPayment implements Client.
func (c *HTTPClient) SendPayment(ctx context.Context, p PaymentInstruction) (PaymentResponse, error) {
	var r PaymentResponse
	err := c.do(ctx, http.MethodPost, "/bank/v1/payments", p, &r)
	if err == nil && r.BankRef == "" {
		err = errors.New("bank response has no bankRef")
	}
	return r, err
}

// PaymentStatus implements Client.
func (c *HTTPClient) PaymentStatus(ctx context.Context, bankRef string) (string, error) {
	var r struct {
		Status string `json:"status"`
	}
	err := c.do(ctx, http.MethodGet, "/bank/v1/payments/"+url.PathEscape(bankRef), nil, &r)
	return r.Status, err
}

// Submitter is the chain write path the adapter needs (satisfied by *fabric.Submitter).
type Submitter interface {
	Submit(ctx context.Context, req fabric.SubmitRequest) (fabric.SubmitResult, error)
	Evaluate(ctx context.Context, fn string, args ...string) ([]byte, error)
}

// Options configures the adapter.
type Options struct {
	PollInterval   time.Duration // outbox poll period (default 1s)
	MaxAttempts    int           // failures before a row is FAILED (default 10)
	MaxBackoff     time.Duration // retry backoff cap (default 5m)
	SettleOnAccept bool          // treat a bank ACCEPTED as final (sims that never report SETTLED)
	OnSettled      func(dealID string)
}

// Adapter is the transactional outbox between the ledger and the bank rails.
//
// Exactly-once payment: the outbox PK (deal, tranche) dedupes enqueues, and the instruction id
// sent to the bank is deterministic, so a crash between "bank accepted" and "row updated" resends
// the same instruction, which the bank answers with the same bankRef instead of paying twice.
type Adapter struct {
	store *store.Store
	bank  Client
	chain Submitter
	id    fabric.Identity
	opts  Options
	log   *slog.Logger
}

// NewAdapter builds an Adapter. id is the BANK identity used for ConfirmSettlement.
func NewAdapter(st *store.Store, bank Client, chain Submitter, id fabric.Identity, opts Options, log *slog.Logger) *Adapter {
	if opts.PollInterval <= 0 {
		opts.PollInterval = time.Second
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 10
	}
	if opts.MaxBackoff <= 0 {
		opts.MaxBackoff = 5 * time.Minute
	}
	if log == nil {
		log = slog.Default()
	}
	return &Adapter{store: st, bank: bank, chain: chain, id: id, opts: opts, log: log}
}

// InstructionID is the deterministic bank instruction id of a tranche payment.
func InstructionID(dealID string, trancheNo int64) string {
	return fmt.Sprintf("PAY-%s-%d", dealID, trancheNo)
}

func (a *Adapter) row(ev TrancheReleasedEvent) (store.OutboxRow, error) {
	if ev.DealID == "" || ev.TrancheNo < 1 {
		return store.OutboxRow{}, apperr.New(apperr.ErrBadRequest, "TrancheReleased needs dealId and trancheNo")
	}
	if ev.AmountPaise <= 0 {
		return store.OutboxRow{}, apperr.Newf(apperr.ErrBadRequest, "TrancheReleased %s/%d has no positive amountPaise", ev.DealID, ev.TrancheNo)
	}
	acct := ev.SupplierAccountRef
	if acct == "" {
		acct = ev.SupplierID
	}
	if acct == "" {
		return store.OutboxRow{}, apperr.Newf(apperr.ErrBadRequest, "TrancheReleased %s/%d has no supplier account", ev.DealID, ev.TrancheNo)
	}
	rail := ev.Rail
	if !rail.Valid() {
		rail = SelectRail(ev.AmountPaise, ev.Urgent)
	}
	return store.OutboxRow{DealID: ev.DealID, TrancheNo: ev.TrancheNo, AmountPaise: ev.AmountPaise, SupplierAccountRef: acct, Rail: string(rail)}, nil
}

// EnqueueFromEvent writes the payment_outbox row for a released tranche (idempotent on deal+tranche).
func (a *Adapter) EnqueueFromEvent(ev TrancheReleasedEvent) error {
	return a.EnqueueTx(context.Background(), a.store.Pool, ev)
}

// EnqueueTx is EnqueueFromEvent inside the caller's transaction, so the listener can commit the
// projection, the outbox row and its checkpoint atomically.
func (a *Adapter) EnqueueTx(ctx context.Context, q store.DBTX, ev TrancheReleasedEvent) error {
	r, err := a.row(ev)
	if err != nil {
		return err
	}
	inserted, err := store.EnqueuePayment(ctx, q, r)
	if err != nil {
		return err
	}
	if inserted {
		a.log.Info("payment enqueued", "dealId", r.DealID, "trancheNo", r.TrancheNo, "amountPaise", r.AmountPaise, "rail", r.Rail)
	}
	return nil
}

// RunOutboxLoop sends PENDING instructions to the bank and confirms settlement on chain, until ctx ends.
func (a *Adapter) RunOutboxLoop(ctx context.Context) {
	t := time.NewTicker(a.opts.PollInterval)
	defer t.Stop()
	for {
		if err := a.ProcessOnce(ctx); err != nil && ctx.Err() == nil {
			a.log.Warn("outbox pass failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ProcessOnce drains every actionable PENDING and SENT row once.
func (a *Adapter) ProcessOnce(ctx context.Context) error {
	for _, status := range []string{store.OutboxPending, store.OutboxSent} {
		for i := 0; i < 100; i++ {
			did, err := a.processNext(ctx, status)
			if err != nil {
				return err
			}
			if !did {
				break
			}
		}
	}
	return nil
}

func (a *Adapter) processNext(ctx context.Context, status string) (bool, error) {
	var settled string
	did := false
	err := a.store.InTx(ctx, func(tx pgx.Tx) error {
		r, ok, err := store.ClaimNext(ctx, tx, status, a.opts.MaxBackoff)
		if err != nil || !ok {
			return err
		}
		did = true
		if status == store.OutboxPending {
			err = a.send(ctx, tx, r)
		} else {
			err = a.followUp(ctx, tx, r)
		}
		if err == nil {
			var st string
			if qerr := tx.QueryRow(ctx, `SELECT status FROM payment_outbox WHERE deal_id = $1 AND tranche_no = $2`, r.DealID, r.TrancheNo).Scan(&st); qerr == nil && st == store.OutboxConfirmed {
				settled = r.DealID
			}
		}
		return err
	})
	if err == nil && settled != "" && a.opts.OnSettled != nil {
		a.opts.OnSettled(settled)
	}
	return did, err
}

func (a *Adapter) fail(ctx context.Context, tx pgx.Tx, r store.OutboxRow, keepStatus string, cause error) error {
	msg := cause.Error()
	next := keepStatus
	var perm *PermanentError
	if errors.As(cause, &perm) || r.Attempts+1 >= a.opts.MaxAttempts {
		next = store.OutboxFailed
	}
	if next == store.OutboxFailed {
		a.log.Error("payment FAILED; needs operator action", "dealId", r.DealID, "trancheNo", r.TrancheNo, "attempts", r.Attempts+1, "err", msg)
	} else {
		a.log.Warn("payment attempt failed; will retry", "dealId", r.DealID, "trancheNo", r.TrancheNo, "attempts", r.Attempts+1, "err", msg)
	}
	return store.MarkOutbox(ctx, tx, r.DealID, r.TrancheNo, next, nil, &msg, true)
}

func (a *Adapter) send(ctx context.Context, tx pgx.Tx, r store.OutboxRow) error {
	resp, err := a.bank.SendPayment(ctx, PaymentInstruction{
		InstructionID: InstructionID(r.DealID, r.TrancheNo), DealID: r.DealID, TrancheNo: r.TrancheNo,
		AmountPaise: r.AmountPaise, Rail: Rail(r.Rail), SupplierAccountRef: r.SupplierAccountRef,
	})
	if err != nil {
		return a.fail(ctx, tx, r, store.OutboxPending, err)
	}
	a.log.Info("payment sent", "dealId", r.DealID, "trancheNo", r.TrancheNo, "bankRef", resp.BankRef, "bankStatus", resp.Status, "rail", r.Rail)
	switch resp.Status {
	case BankFailed:
		msg := "bank reported FAILED"
		return store.MarkOutbox(ctx, tx, r.DealID, r.TrancheNo, store.OutboxFailed, &resp.BankRef, &msg, true)
	case BankSettled, BankAccepted:
	default:
		return a.fail(ctx, tx, r, store.OutboxPending, fmt.Errorf("unknown bank status %q", resp.Status))
	}
	if err := store.MarkOutbox(ctx, tx, r.DealID, r.TrancheNo, store.OutboxSent, &resp.BankRef, nil, false); err != nil {
		return err
	}
	r.BankRef, r.Status = &resp.BankRef, store.OutboxSent
	if resp.Status == BankSettled || a.opts.SettleOnAccept {
		return a.confirm(ctx, tx, r)
	}
	return nil
}

func (a *Adapter) followUp(ctx context.Context, tx pgx.Tx, r store.OutboxRow) error {
	if r.BankRef == nil || *r.BankRef == "" {
		return store.MarkOutbox(ctx, tx, r.DealID, r.TrancheNo, store.OutboxPending, nil, nil, false)
	}
	if !a.opts.SettleOnAccept {
		st, err := a.bank.PaymentStatus(ctx, *r.BankRef)
		if err != nil {
			return a.fail(ctx, tx, r, store.OutboxSent, err)
		}
		switch st {
		case BankAccepted:
			return nil // still in flight; poll again next pass
		case BankFailed:
			msg := "bank reported FAILED for " + *r.BankRef
			return store.MarkOutbox(ctx, tx, r.DealID, r.TrancheNo, store.OutboxFailed, nil, &msg, true)
		case BankSettled:
		default:
			return a.fail(ctx, tx, r, store.OutboxSent, fmt.Errorf("unknown bank status %q", st))
		}
	}
	return a.confirm(ctx, tx, r)
}

// confirm records the settlement on chain (ConfirmSettlement, signed by the BANK identity).
func (a *Adapter) confirm(ctx context.Context, tx pgx.Tx, r store.OutboxRow) error {
	cctx := fabric.WithIdentity(ctx, a.id)
	_, err := a.chain.Submit(cctx, fabric.SubmitRequest{Fn: "ConfirmSettlement", Args: []string{r.DealID, fmt.Sprint(r.TrancheNo), *r.BankRef, r.Rail}})
	switch {
	case err == nil, apperr.Is(err, apperr.ErrDuplicate):
	case apperr.Is(err, apperr.ErrInvalidTransition) && a.alreadySettled(cctx, r):
	default:
		return a.fail(ctx, tx, r, store.OutboxSent, fmt.Errorf("ConfirmSettlement: %w", err))
	}
	a.log.Info("settlement confirmed on chain", "dealId", r.DealID, "trancheNo", r.TrancheNo, "bankRef", *r.BankRef)
	return store.MarkOutbox(ctx, tx, r.DealID, r.TrancheNo, store.OutboxConfirmed, r.BankRef, nil, false)
}

func (a *Adapter) alreadySettled(ctx context.Context, r store.OutboxRow) bool {
	b, err := a.chain.Evaluate(ctx, "GetTranche", r.DealID, fmt.Sprint(r.TrancheNo))
	if err != nil {
		return false
	}
	var t model.Tranche
	return json.Unmarshal(b, &t) == nil && t.Status == model.TrancheSettled
}
