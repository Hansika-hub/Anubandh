package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Hansika-hub/anubandh/gateway/internal/model"
)

// EventRow is one deal_events row.
type EventRow struct {
	DealID    string
	TrancheNo *int64
	EventName string
	Payload   json.RawMessage
	TxID      string
	BlockNo   *uint64
	Ts        time.Time
}

// InsertEvent records a chain event once. inserted=false means (txId, eventName) was already
// projected, so the caller must not apply the event's side effects again.
func InsertEvent(ctx context.Context, q DBTX, e EventRow) (id int64, inserted bool, err error) {
	payload := e.Payload
	if !json.Valid(payload) {
		payload, _ = json.Marshal(map[string]string{"raw": string(e.Payload)})
	}
	var txID *string
	if e.TxID != "" {
		txID = &e.TxID
	}
	var block *int64
	if e.BlockNo != nil {
		b, err := blockToDB(*e.BlockNo)
		if err != nil {
			return 0, false, err
		}
		block = &b
	}
	err = q.QueryRow(ctx, `
		INSERT INTO deal_events (deal_id, tranche_no, event_name, payload, tx_id, block_no, ts)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (tx_id, event_name) WHERE tx_id IS NOT NULL DO NOTHING
		RETURNING id`,
		e.DealID, e.TrancheNo, e.EventName, payload, txID, block, e.Ts.UTC()).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	return id, err == nil, err
}

// Checkpoint is the listener resume position: next block to read and how many of that block's
// events were already handled (one chaincode event per transaction, so this is a tx ordinal).
type Checkpoint struct {
	BlockNo uint64
	TxIndex int
}

// GetCheckpoint returns the stored checkpoint (found=false on first start).
func GetCheckpoint(ctx context.Context, q DBTX, listenerID string) (cp Checkpoint, found bool, err error) {
	var block int64
	err = q.QueryRow(ctx, `SELECT block_no, tx_index FROM chain_checkpoint WHERE listener_id = $1`, listenerID).Scan(&block, &cp.TxIndex)
	if errors.Is(err, pgx.ErrNoRows) {
		return Checkpoint{}, false, nil
	}
	if err != nil {
		return Checkpoint{}, false, err
	}
	if block < 0 {
		return Checkpoint{}, false, fmt.Errorf("chain_checkpoint %s has negative block %d", listenerID, block)
	}
	cp.BlockNo = uint64(block)
	return cp, true, nil
}

// blockToDB converts a Fabric block number (uint64) to a PostgreSQL bigint.
func blockToDB(b uint64) (int64, error) {
	if b > math.MaxInt64 {
		return 0, fmt.Errorf("block number %d exceeds bigint", b)
	}
	return int64(b), nil
}

// SaveCheckpoint upserts the checkpoint.
func SaveCheckpoint(ctx context.Context, q DBTX, listenerID string, cp Checkpoint) error {
	block, err := blockToDB(cp.BlockNo)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `
		INSERT INTO chain_checkpoint (listener_id, block_no, tx_index) VALUES ($1, $2, $3)
		ON CONFLICT (listener_id) DO UPDATE SET block_no = EXCLUDED.block_no, tx_index = EXCLUDED.tx_index`,
		listenerID, block, cp.TxIndex)
	return err
}

var dealRank = map[string]int{
	model.DealCreated: 1, model.DealFunded: 2, model.DealDelivered: 3, model.DealAccepted: 4,
	model.DealPartiallyAccepted: 4, model.DealReleased: 5, model.DealSettled: 6,
	model.DealDisputed: 7, model.DealExpired: 8, model.DealClosed: 9,
}

var trancheRank = map[string]int{
	model.TranchePending: 1, model.TrancheDelivered: 2, model.TrancheAccepted: 3, model.TrancheDeemedAccepted: 3,
	model.TrancheHeld: 3, model.TrancheApproved: 4, model.TrancheReleased: 5, model.TrancheSettled: 6, model.TrancheClosed: 7,
}

// EnsureDeal inserts a skeleton deal row if none exists (DealCreated, or a write accepted by the
// gateway before its event is projected).
func EnsureDeal(ctx context.Context, q DBTX, d model.Deal) error {
	created := time.Now().UTC()
	if d.CreatedAt != nil {
		created = d.CreatedAt.UTC()
	}
	status := d.Status
	if status == "" {
		status = model.DealCreated
	}
	_, err := q.Exec(ctx, `
		INSERT INTO deals (deal_id, po_number, buyer_id, supplier_id, status, lien_amount_paise, mse_category,
		                   mse_term_days, mse_applies, udyam_no, delivery_deadline, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $12)
		ON CONFLICT (deal_id) DO NOTHING`,
		d.DealID, d.PONumber, d.BuyerID, d.SupplierID, status, d.LienAmountPaise, nullStr(d.MSE.Category),
		nullInt(d.MSE.TermDays), d.MSE.Applies, nullStr(d.MSE.UdyamNo), d.DeliveryDeadline, created)
	return err
}

// AdvanceDealStatus moves a deal forward only (status rank never decreases), so an older event
// replayed after a newer reconcile cannot regress the read model.
func AdvanceDealStatus(ctx context.Context, q DBTX, dealID, to string, ts time.Time) error {
	var cur string
	err := q.QueryRow(ctx, `SELECT status FROM deals WHERE deal_id = $1 FOR UPDATE`, dealID).Scan(&cur)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if dealRank[to] <= dealRank[cur] {
		return nil
	}
	_, err = q.Exec(ctx, `UPDATE deals SET status = $2, updated_at = GREATEST(updated_at, $3) WHERE deal_id = $1`, dealID, to, ts.UTC())
	return err
}

// SetLienRef records the bank lien reference.
func SetLienRef(ctx context.Context, q DBTX, dealID, lienRef string) error {
	_, err := q.Exec(ctx, `UPDATE deals SET lien_ref = COALESCE(lien_ref, $2) WHERE deal_id = $1`, dealID, lienRef)
	return err
}

// SetInvoice records a verified IRN.
func SetInvoice(ctx context.Context, q DBTX, dealID, irn string) error {
	_, err := q.Exec(ctx, `UPDATE deals SET invoice_irn = $2, invoice_verified = true WHERE deal_id = $1`, dealID, irn)
	return err
}

// TrancheUpdate is a partial, forward-only tranche update from an event payload.
type TrancheUpdate struct {
	Status        string
	DeliveredAt   *time.Time
	AcceptedAt    *time.Time
	AcceptedValue *int64
	HeldValue     *int64
	Deemed        *bool
	MseClockStart *time.Time
	MseDeadline   *time.Time
	RestartClock  bool // MseClockRestarted: overwrite the clock instead of first-write-wins
	BankRef       string
	Rail          string
	ReleasedAt    *time.Time
	SettledAt     *time.Time
}

// ApplyTrancheUpdate creates the tranche row if needed and applies u. Status only moves forward;
// timestamps and references are first-write-wins except an explicit clock restart.
func ApplyTrancheUpdate(ctx context.Context, q DBTX, dealID string, n int64, u TrancheUpdate) error {
	if _, err := q.Exec(ctx, `INSERT INTO tranches (deal_id, tranche_no, status) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
		dealID, n, firstNonEmpty(u.Status, model.TranchePending)); err != nil {
		return err
	}
	var cur string
	if err := q.QueryRow(ctx, `SELECT status FROM tranches WHERE deal_id = $1 AND tranche_no = $2 FOR UPDATE`, dealID, n).Scan(&cur); err != nil {
		return err
	}
	status := cur
	if u.Status != "" && trancheRank[u.Status] > trancheRank[cur] {
		status = u.Status
	}
	_, err := q.Exec(ctx, `
		UPDATE tranches SET
		    status               = $3,
		    delivered_at         = COALESCE(delivered_at, $4),
		    accepted_at          = COALESCE(accepted_at, $5),
		    accepted_value_paise = COALESCE($6, accepted_value_paise),
		    held_value_paise     = COALESCE($7, held_value_paise),
		    deemed               = COALESCE($8, deemed),
		    mse_clock_start      = CASE WHEN $11 THEN COALESCE($9, mse_clock_start) ELSE COALESCE(mse_clock_start, $9) END,
		    mse_deadline         = CASE WHEN $11 THEN COALESCE($10, mse_deadline) ELSE COALESCE(mse_deadline, $10) END,
		    bank_ref             = COALESCE(bank_ref, NULLIF($12, '')),
		    rail                 = COALESCE(NULLIF($13, ''), rail),
		    released_at          = COALESCE(released_at, $14),
		    settled_at           = COALESCE(settled_at, $15)
		WHERE deal_id = $1 AND tranche_no = $2`,
		dealID, n, status, u.DeliveredAt, u.AcceptedAt, u.AcceptedValue, u.HeldValue, u.Deemed,
		u.MseClockStart, u.MseDeadline, u.RestartClock, u.BankRef, u.Rail, u.ReleasedAt, u.SettledAt)
	return err
}

// UpsertApproval records one approver signature.
func UpsertApproval(ctx context.Context, q DBTX, dealID string, n int64, role, userID string, signedAt time.Time) error {
	_, err := q.Exec(ctx, `
		INSERT INTO approvals (deal_id, tranche_no, role, user_id, signed_at) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (deal_id, tranche_no, role) DO NOTHING`, dealID, n, role, userID, signedAt.UTC())
	return err
}

// UpsertHeldPortion inserts or updates a held portion.
func UpsertHeldPortion(ctx context.Context, q DBTX, h model.HeldPortion) error {
	_, err := q.Exec(ctx, `
		INSERT INTO held_portions (deal_id, tranche_no, line_no, qty, value_paise, exit, state)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (deal_id, tranche_no, line_no) DO UPDATE SET
		    qty = EXCLUDED.qty, value_paise = EXCLUDED.value_paise,
		    exit = COALESCE(EXCLUDED.exit, held_portions.exit), state = EXCLUDED.state`,
		h.DealID, h.TrancheNo, h.LineNo, h.Qty, h.ValuePaise, h.Exit, firstNonEmpty(h.State, model.TrancheHeld))
	return err
}

// SetHeldExit records a chosen exit (and optionally a new state) for an existing held portion.
func SetHeldExit(ctx context.Context, q DBTX, dealID string, n, lineNo int64, exit, state string) error {
	_, err := q.Exec(ctx, `
		UPDATE held_portions SET exit = $4, state = COALESCE(NULLIF($5, ''), state)
		WHERE deal_id = $1 AND tranche_no = $2 AND line_no = $3`, dealID, n, lineNo, exit, state)
	return err
}

// UpsertLines writes the buyer-supplied PO lines (sku, qty, price). Aggregates are left alone.
func UpsertLines(ctx context.Context, q DBTX, lines []model.Line) error {
	for _, l := range lines {
		if _, err := q.Exec(ctx, `
			INSERT INTO deal_lines (deal_id, line_no, sku, ordered_qty, unit_price_paise) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (deal_id, line_no) DO UPDATE SET
			    sku = EXCLUDED.sku, ordered_qty = EXCLUDED.ordered_qty, unit_price_paise = EXCLUDED.unit_price_paise`,
			l.DealID, l.LineNo, l.SKU, l.OrderedQty, l.UnitPricePaise); err != nil {
			return err
		}
	}
	return nil
}

// ---- reconcile from chain state (the ledger is the source of truth) ----

// ReconcileDeal overwrites the read model of one deal with the ledger's current state.
// released_at / settled_at are not on the ledger objects and are kept from the event stream.
func ReconcileDeal(ctx context.Context, tx pgx.Tx, d model.Deal, tranches []model.Tranche, held []model.HeldPortion) error {
	if err := LockDeal(ctx, tx, d.DealID); err != nil {
		return err
	}
	created := time.Now().UTC()
	if d.CreatedAt != nil {
		created = d.CreatedAt.UTC()
	}
	updated := created
	if d.UpdatedAt != nil {
		updated = d.UpdatedAt.UTC()
	}
	var irn *string
	if d.Invoice.IRN != nil && *d.Invoice.IRN != "" {
		irn = d.Invoice.IRN
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO deals (deal_id, po_number, buyer_id, supplier_id, status, lien_amount_paise, lien_ref,
		                   total_released_paise, total_held_paise, mse_category, mse_term_days, mse_applies, udyam_no,
		                   delivery_deadline, invoice_irn, invoice_verified, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
		ON CONFLICT (deal_id) DO UPDATE SET
		    po_number = EXCLUDED.po_number, buyer_id = EXCLUDED.buyer_id, supplier_id = EXCLUDED.supplier_id,
		    status = EXCLUDED.status, lien_amount_paise = EXCLUDED.lien_amount_paise, lien_ref = EXCLUDED.lien_ref,
		    total_released_paise = EXCLUDED.total_released_paise, total_held_paise = EXCLUDED.total_held_paise,
		    mse_category = EXCLUDED.mse_category, mse_term_days = EXCLUDED.mse_term_days, mse_applies = EXCLUDED.mse_applies,
		    udyam_no = EXCLUDED.udyam_no, delivery_deadline = EXCLUDED.delivery_deadline,
		    invoice_irn = EXCLUDED.invoice_irn, invoice_verified = EXCLUDED.invoice_verified,
		    created_at = EXCLUDED.created_at, updated_at = GREATEST(deals.updated_at, EXCLUDED.updated_at)`,
		d.DealID, d.PONumber, d.BuyerID, d.SupplierID, d.Status, d.LienAmountPaise, d.LienRef,
		d.TotalReleasedPaise, d.TotalHeldPaise, nullStr(d.MSE.Category), nullInt(d.MSE.TermDays), d.MSE.Applies,
		nullStr(d.MSE.UdyamNo), d.DeliveryDeadline, irn, d.Invoice.Verified, created, updated); err != nil {
		return err
	}
	type agg struct{ delivered, accepted, rejected, reaccepted, held int64 }
	lines := map[int64]*agg{}
	get := func(n int64) *agg {
		if lines[n] == nil {
			lines[n] = &agg{}
		}
		return lines[n]
	}
	for _, t := range tranches {
		if _, err := tx.Exec(ctx, `
			INSERT INTO tranches (deal_id, tranche_no, status, accepted_value_paise, held_value_paise, delivered_at, accepted_at,
			                      deemed, mse_clock_start, mse_deadline, bank_ref, rail)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			ON CONFLICT (deal_id, tranche_no) DO UPDATE SET
			    status = EXCLUDED.status, accepted_value_paise = EXCLUDED.accepted_value_paise,
			    held_value_paise = EXCLUDED.held_value_paise, delivered_at = EXCLUDED.delivered_at,
			    accepted_at = EXCLUDED.accepted_at, deemed = EXCLUDED.deemed,
			    mse_clock_start = EXCLUDED.mse_clock_start, mse_deadline = EXCLUDED.mse_deadline,
			    bank_ref = COALESCE(EXCLUDED.bank_ref, tranches.bank_ref), rail = COALESCE(EXCLUDED.rail, tranches.rail)`,
			d.DealID, t.TrancheNo, t.Status, t.AcceptedValuePaise, t.HeldValuePaise, t.DeliveredAt, t.AcceptedAt,
			t.DeemedAcceptance, t.MseClockStart, t.MseDeadline, t.BankRef, t.Rail); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM approvals WHERE deal_id = $1 AND tranche_no = $2`, d.DealID, t.TrancheNo); err != nil {
			return err
		}
		for _, ap := range t.Approvals {
			if _, err := tx.Exec(ctx, `INSERT INTO approvals (deal_id, tranche_no, role, user_id, signed_at) VALUES ($1, $2, $3, $4, $5)
				ON CONFLICT (deal_id, tranche_no, role) DO UPDATE SET user_id = EXCLUDED.user_id, signed_at = EXCLUDED.signed_at`,
				d.DealID, t.TrancheNo, string(ap.Role), ap.UserID, ap.SignedAt); err != nil {
				return err
			}
		}
		for _, l := range t.Lines {
			a := get(l.LineNo)
			a.delivered += l.DeliveredQty
			a.accepted += l.AcceptedQty
			a.rejected += l.RejectedQty
			if l.DeliveredQty == 0 {
				a.reaccepted += l.AcceptedQty // held units accepted later (price adjustment / dispute)
			}
		}
	}
	for _, h := range held {
		if err := UpsertHeldPortion(ctx, tx, h); err != nil {
			return err
		}
		if h.State == model.TrancheHeld || h.State == model.DealDisputed {
			get(h.LineNo).held += h.Qty
		}
	}
	for lineNo, a := range lines {
		rejected := a.rejected - a.reaccepted
		if rejected < 0 {
			rejected = 0
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO deal_lines (deal_id, line_no, delivered_qty, accepted_qty, rejected_qty, held_qty) VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (deal_id, line_no) DO UPDATE SET
			    delivered_qty = EXCLUDED.delivered_qty, accepted_qty = EXCLUDED.accepted_qty,
			    rejected_qty = EXCLUDED.rejected_qty, held_qty = EXCLUDED.held_qty`,
			d.DealID, lineNo, a.delivered, a.accepted, rejected, a.held); err != nil {
			return err
		}
	}
	return nil
}

func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nullInt(i int) *int {
	if i == 0 {
		return nil
	}
	return &i
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
