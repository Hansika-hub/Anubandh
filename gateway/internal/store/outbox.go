package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Outbox statuses (BUILD.md §7).
const (
	OutboxPending   = "PENDING"
	OutboxSent      = "SENT"
	OutboxConfirmed = "CONFIRMED"
	OutboxFailed    = "FAILED"
)

// OutboxRow is one payment instruction.
type OutboxRow struct {
	DealID             string     `json:"dealId"`
	TrancheNo          int64      `json:"trancheNo"`
	AmountPaise        int64      `json:"amountPaise"`
	SupplierAccountRef string     `json:"supplierAccountRef"`
	Rail               string     `json:"rail"`
	Status             string     `json:"status"`
	BankRef            *string    `json:"bankRef"`
	Attempts           int        `json:"attempts"`
	LastError          *string    `json:"lastError"`
	CreatedAt          *time.Time `json:"createdAt"`
	UpdatedAt          *time.Time `json:"updatedAt"`
}

const outboxCols = `deal_id, tranche_no, amount_paise, supplier_account_ref, rail, status, bank_ref, attempts, last_error, created_at, updated_at`

func scanOutbox(row pgx.Row) (OutboxRow, error) {
	var r OutboxRow
	err := row.Scan(&r.DealID, &r.TrancheNo, &r.AmountPaise, &r.SupplierAccountRef, &r.Rail, &r.Status, &r.BankRef,
		&r.Attempts, &r.LastError, &r.CreatedAt, &r.UpdatedAt)
	r.CreatedAt, r.UpdatedAt = utc(r.CreatedAt), utc(r.UpdatedAt)
	return r, err
}

// EnqueuePayment inserts a PENDING instruction; a second call for the same (deal, tranche) is a
// no-op, which is what makes a replayed TrancheReleased event harmless.
func EnqueuePayment(ctx context.Context, q DBTX, r OutboxRow) (inserted bool, err error) {
	tag, err := q.Exec(ctx, `
		INSERT INTO payment_outbox (deal_id, tranche_no, amount_paise, supplier_account_ref, rail, status)
		VALUES ($1, $2, $3, $4, $5, 'PENDING') ON CONFLICT (deal_id, tranche_no) DO NOTHING`,
		r.DealID, r.TrancheNo, r.AmountPaise, r.SupplierAccountRef, r.Rail)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ClaimNext locks the oldest actionable row in status (PENDING rows only once their backoff has
// elapsed) using SKIP LOCKED, so several gateway replicas never process the same row.
func ClaimNext(ctx context.Context, tx pgx.Tx, status string, maxBackoff time.Duration) (OutboxRow, bool, error) {
	r, err := scanOutbox(tx.QueryRow(ctx, `
		SELECT `+outboxCols+` FROM payment_outbox
		WHERE status = $1
		  AND (attempts = 0 OR updated_at + LEAST(power(2, LEAST(attempts, 20)) * interval '1 second', $2 * interval '1 second') <= now())
		ORDER BY updated_at, deal_id, tranche_no
		LIMIT 1 FOR UPDATE SKIP LOCKED`, status, maxBackoff.Seconds()))
	if errors.Is(err, pgx.ErrNoRows) {
		return OutboxRow{}, false, nil
	}
	return r, err == nil, err
}

// MarkOutbox updates a row's status, bank ref and error. attempts counts consecutive failures in
// the current phase: it is bumped when failed is true and reset when the row changes phase, so a
// payment that needed retries to reach the bank is not then polled on a stale backoff.
func MarkOutbox(ctx context.Context, q DBTX, dealID string, n int64, status string, bankRef *string, lastErr *string, failed bool) error {
	_, err := q.Exec(ctx, `
		UPDATE payment_outbox SET bank_ref = COALESCE($4, bank_ref), last_error = $5,
		    attempts = CASE WHEN $6 THEN attempts + 1 WHEN status <> $3 THEN 0 ELSE attempts END,
		    status = $3, updated_at = now()
		WHERE deal_id = $1 AND tranche_no = $2`, dealID, n, status, bankRef, lastErr, failed)
	return err
}

// ListOutbox returns outbox rows, newest first, optionally filtered.
func ListOutbox(ctx context.Context, q DBTX, status, dealID string, limit, offset int) ([]OutboxRow, int, error) {
	var total int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM payment_outbox WHERE ($1 = '' OR status = $1) AND ($2 = '' OR deal_id = $2)`,
		status, dealID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := q.Query(ctx, `SELECT `+outboxCols+` FROM payment_outbox WHERE ($1 = '' OR status = $1) AND ($2 = '' OR deal_id = $2)
		ORDER BY created_at DESC, deal_id, tranche_no LIMIT $3 OFFSET $4`, status, dealID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []OutboxRow{}
	for rows.Next() {
		r, err := scanOutbox(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// OutboxCounts returns row counts per status (health endpoint).
func OutboxCounts(ctx context.Context, q DBTX) (map[string]int, error) {
	rows, err := q.Query(ctx, `SELECT status, count(*) FROM payment_outbox GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{OutboxPending: 0, OutboxSent: 0, OutboxConfirmed: 0, OutboxFailed: 0}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			return nil, err
		}
		out[s] = n
	}
	return out, rows.Err()
}
