package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Hansika-hub/anubandh/gateway/internal/model"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// MSEView is the deal's MSE block as served by the API.
type MSEView struct {
	Category *string `json:"category"`
	TermDays *int    `json:"termDays"`
	UdyamNo  *string `json:"udyamNo"`
	Applies  bool    `json:"applies"`
}

// InvoiceView is the deal's invoice block.
type InvoiceView struct {
	IRN      *string `json:"irn"`
	Verified bool    `json:"verified"`
}

// DealView is a deal as listed by GET /deals.
type DealView struct {
	DealID             string      `json:"dealId"`
	PONumber           string      `json:"poNumber"`
	BuyerID            string      `json:"buyerId"`
	SupplierID         string      `json:"supplierId"`
	Status             string      `json:"status"`
	Currency           string      `json:"currency"`
	LienAmountPaise    int64       `json:"lienAmountPaise"`
	LienRef            *string     `json:"lienRef"`
	TotalReleasedPaise int64       `json:"totalReleasedPaise"`
	TotalHeldPaise     int64       `json:"totalHeldPaise"`
	MSE                MSEView     `json:"mse"`
	DeliveryDeadline   *time.Time  `json:"deliveryDeadline"`
	Invoice            InvoiceView `json:"invoice"`
	CreatedAt          *time.Time  `json:"createdAt"`
	UpdatedAt          *time.Time  `json:"updatedAt"`
}

// LineView is a PO line. UnitPricePaise is nil when hidden from the viewer (AUDITOR).
type LineView struct {
	DealID         string `json:"dealId"`
	LineNo         int64  `json:"lineNo"`
	SKU            string `json:"sku"`
	OrderedQty     int64  `json:"orderedQty"`
	UnitPricePaise *int64 `json:"unitPricePaise,omitempty"`
	DeliveredQty   int64  `json:"deliveredQty"`
	AcceptedQty    int64  `json:"acceptedQty"`
	RejectedQty    int64  `json:"rejectedQty"`
	HeldQty        int64  `json:"heldQty"`
}

// ApprovalView is one approver signature.
type ApprovalView struct {
	Role     string     `json:"role"`
	UserID   string     `json:"userId"`
	SignedAt *time.Time `json:"signedAt"`
}

// TrancheView is a tranche as served by the API. Lines, Objection, HeldExit and ReleaseIdemKey are
// not in the read-model schema; they are filled from the ledger when it is reachable.
type TrancheView struct {
	DealID             string              `json:"dealId"`
	TrancheNo          int64               `json:"trancheNo"`
	Status             string              `json:"status"`
	Lines              []model.TrancheLine `json:"lines,omitempty"`
	AcceptedValuePaise int64               `json:"acceptedValuePaise"`
	HeldValuePaise     int64               `json:"heldValuePaise"`
	DeliveredAt        *time.Time          `json:"deliveredAt"`
	AcceptedAt         *time.Time          `json:"acceptedAt"`
	DeemedAcceptance   bool                `json:"deemedAcceptance"`
	Approvals          []ApprovalView      `json:"approvals"`
	ReleaseIdemKey     *string             `json:"releaseIdemKey,omitempty"`
	BankRef            *string             `json:"bankRef"`
	Rail               *string             `json:"rail"`
	MseClockStart      *time.Time          `json:"mseClockStart"`
	MseDeadline        *time.Time          `json:"mseDeadline"`
	ReleasedAt         *time.Time          `json:"releasedAt"`
	SettledAt          *time.Time          `json:"settledAt"`
	HeldExit           *string             `json:"heldExit,omitempty"`
	Objection          json.RawMessage     `json:"objection,omitempty"`
}

// HeldView is a held portion.
type HeldView struct {
	DealID     string  `json:"dealId"`
	TrancheNo  int64   `json:"trancheNo"`
	LineNo     int64   `json:"lineNo"`
	Qty        int64   `json:"qty"`
	ValuePaise int64   `json:"valuePaise"`
	Exit       *string `json:"exit"`
	State      string  `json:"state"`
}

// DealFilter is the GET /deals query.
type DealFilter struct {
	Status     string
	BuyerID    string
	SupplierID string
	MSE        *bool
	Limit      int
	Offset     int
}

const dealCols = `deal_id, po_number, buyer_id, supplier_id, status, lien_amount_paise, lien_ref, total_released_paise,
	total_held_paise, mse_category, mse_term_days, mse_applies, udyam_no, delivery_deadline, invoice_irn, invoice_verified,
	created_at, updated_at`

func scanDeal(row pgx.Row) (DealView, error) {
	var d DealView
	err := row.Scan(&d.DealID, &d.PONumber, &d.BuyerID, &d.SupplierID, &d.Status, &d.LienAmountPaise, &d.LienRef,
		&d.TotalReleasedPaise, &d.TotalHeldPaise, &d.MSE.Category, &d.MSE.TermDays, &d.MSE.Applies, &d.MSE.UdyamNo,
		&d.DeliveryDeadline, &d.Invoice.IRN, &d.Invoice.Verified, &d.CreatedAt, &d.UpdatedAt)
	d.Currency = "INR"
	d.DeliveryDeadline, d.CreatedAt, d.UpdatedAt = utc(d.DeliveryDeadline), utc(d.CreatedAt), utc(d.UpdatedAt)
	return d, err
}

// ListDeals returns one page of deals plus the total match count, newest first.
func ListDeals(ctx context.Context, q DBTX, f DealFilter) ([]DealView, int, error) {
	var where []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if f.Status != "" {
		add("status = $%d", f.Status)
	}
	if f.BuyerID != "" {
		add("buyer_id = $%d", f.BuyerID)
	}
	if f.SupplierID != "" {
		add("supplier_id = $%d", f.SupplierID)
	}
	if f.MSE != nil {
		add("mse_applies = $%d", *f.MSE)
	}
	cond := ""
	if len(where) > 0 {
		cond = " WHERE " + strings.Join(where, " AND ")
	}
	var total int
	if err := q.QueryRow(ctx, "SELECT count(*) FROM deals"+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, f.Limit, f.Offset)
	rows, err := q.Query(ctx, fmt.Sprintf("SELECT %s FROM deals%s ORDER BY created_at DESC, deal_id DESC LIMIT $%d OFFSET $%d",
		dealCols, cond, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []DealView{}
	for rows.Next() {
		d, err := scanDeal(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, d)
	}
	return out, total, rows.Err()
}

// GetDeal returns one deal.
func GetDeal(ctx context.Context, q DBTX, dealID string) (DealView, error) {
	d, err := scanDeal(q.QueryRow(ctx, "SELECT "+dealCols+" FROM deals WHERE deal_id = $1", dealID))
	if errors.Is(err, pgx.ErrNoRows) {
		return DealView{}, ErrNotFound
	}
	return d, err
}

// GetLines returns a deal's PO lines.
func GetLines(ctx context.Context, q DBTX, dealID string) ([]LineView, error) {
	rows, err := q.Query(ctx, `SELECT deal_id, line_no, sku, ordered_qty, unit_price_paise, delivered_qty, accepted_qty, rejected_qty, held_qty
		FROM deal_lines WHERE deal_id = $1 ORDER BY line_no`, dealID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LineView{}
	for rows.Next() {
		var l LineView
		var price int64
		if err := rows.Scan(&l.DealID, &l.LineNo, &l.SKU, &l.OrderedQty, &price, &l.DeliveredQty, &l.AcceptedQty, &l.RejectedQty, &l.HeldQty); err != nil {
			return nil, err
		}
		l.UnitPricePaise = &price
		out = append(out, l)
	}
	return out, rows.Err()
}

// GetTranches returns a deal's tranches with their approvals.
func GetTranches(ctx context.Context, q DBTX, dealID string) ([]TrancheView, error) {
	rows, err := q.Query(ctx, `SELECT tranche_no, status, accepted_value_paise, held_value_paise, delivered_at, accepted_at, deemed,
		mse_clock_start, mse_deadline, bank_ref, rail, released_at, settled_at FROM tranches WHERE deal_id = $1 ORDER BY tranche_no`, dealID)
	if err != nil {
		return nil, err
	}
	out := []TrancheView{}
	idx := map[int64]int{}
	for rows.Next() {
		t := TrancheView{DealID: dealID, Approvals: []ApprovalView{}}
		if err := rows.Scan(&t.TrancheNo, &t.Status, &t.AcceptedValuePaise, &t.HeldValuePaise, &t.DeliveredAt, &t.AcceptedAt,
			&t.DeemedAcceptance, &t.MseClockStart, &t.MseDeadline, &t.BankRef, &t.Rail, &t.ReleasedAt, &t.SettledAt); err != nil {
			rows.Close()
			return nil, err
		}
		t.DeliveredAt, t.AcceptedAt, t.MseClockStart = utc(t.DeliveredAt), utc(t.AcceptedAt), utc(t.MseClockStart)
		t.MseDeadline, t.ReleasedAt, t.SettledAt = utc(t.MseDeadline), utc(t.ReleasedAt), utc(t.SettledAt)
		idx[t.TrancheNo] = len(out)
		out = append(out, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	arows, err := q.Query(ctx, `SELECT tranche_no, role, user_id, signed_at FROM approvals WHERE deal_id = $1 ORDER BY tranche_no, signed_at, role`, dealID)
	if err != nil {
		return nil, err
	}
	defer arows.Close()
	for arows.Next() {
		var n int64
		var a ApprovalView
		if err := arows.Scan(&n, &a.Role, &a.UserID, &a.SignedAt); err != nil {
			return nil, err
		}
		a.SignedAt = utc(a.SignedAt)
		if i, ok := idx[n]; ok {
			out[i].Approvals = append(out[i].Approvals, a)
		}
	}
	return out, arows.Err()
}

// GetHeld returns a deal's held portions.
func GetHeld(ctx context.Context, q DBTX, dealID string) ([]HeldView, error) {
	rows, err := q.Query(ctx, `SELECT tranche_no, line_no, qty, value_paise, exit, state FROM held_portions
		WHERE deal_id = $1 ORDER BY tranche_no, line_no`, dealID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HeldView{}
	for rows.Next() {
		h := HeldView{DealID: dealID}
		if err := rows.Scan(&h.TrancheNo, &h.LineNo, &h.Qty, &h.ValuePaise, &h.Exit, &h.State); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// DealParties returns buyer and supplier of a deal (authorization of party users).
func DealParties(ctx context.Context, q DBTX, dealID string) (buyerID, supplierID string, err error) {
	err = q.QueryRow(ctx, `SELECT buyer_id, supplier_id FROM deals WHERE deal_id = $1`, dealID).Scan(&buyerID, &supplierID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNotFound
	}
	return buyerID, supplierID, err
}

// ActiveDealIDs returns deals that time rules can still act on.
func ActiveDealIDs(ctx context.Context, q DBTX) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT deal_id FROM deals WHERE status NOT IN ('SETTLED', 'EXPIRED', 'CLOSED') ORDER BY deal_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// MseTrancheRow is one MSE-covered tranche with a running payment clock.
type MseTrancheRow struct {
	DealID             string
	TrancheNo          int64
	BuyerID            string
	SupplierID         string
	Category           *string
	TermDays           *int
	UdyamNo            *string
	Status             string
	AcceptedValuePaise int64
	DeliveredAt        *time.Time
	AcceptedAt         *time.Time
	Deemed             bool
	MseClockStart      *time.Time
	MseDeadline        *time.Time
	ReleasedAt         *time.Time
	SettledAt          *time.Time
}

// MseTranches returns every MSE tranche whose clock has started, earliest deadline first.
func MseTranches(ctx context.Context, q DBTX) ([]MseTrancheRow, error) {
	rows, err := q.Query(ctx, `
		SELECT t.deal_id, t.tranche_no, d.buyer_id, d.supplier_id, d.mse_category, d.mse_term_days, d.udyam_no, t.status,
		       t.accepted_value_paise, t.delivered_at, t.accepted_at, t.deemed, t.mse_clock_start, t.mse_deadline,
		       t.released_at, t.settled_at
		FROM tranches t JOIN deals d ON d.deal_id = t.deal_id
		WHERE d.mse_applies AND t.mse_deadline IS NOT NULL
		ORDER BY t.mse_deadline, t.deal_id, t.tranche_no`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MseTrancheRow
	for rows.Next() {
		var r MseTrancheRow
		if err := rows.Scan(&r.DealID, &r.TrancheNo, &r.BuyerID, &r.SupplierID, &r.Category, &r.TermDays, &r.UdyamNo, &r.Status,
			&r.AcceptedValuePaise, &r.DeliveredAt, &r.AcceptedAt, &r.Deemed, &r.MseClockStart, &r.MseDeadline,
			&r.ReleasedAt, &r.SettledAt); err != nil {
			return nil, err
		}
		r.DeliveredAt, r.AcceptedAt, r.MseClockStart = utc(r.DeliveredAt), utc(r.AcceptedAt), utc(r.MseClockStart)
		r.MseDeadline, r.ReleasedAt, r.SettledAt = utc(r.MseDeadline), utc(r.ReleasedAt), utc(r.SettledAt)
		out = append(out, r)
	}
	return out, rows.Err()
}

// EventView is one projected chain event.
type EventView struct {
	ID        int64           `json:"id"`
	DealID    string          `json:"dealId"`
	TrancheNo *int64          `json:"trancheNo"`
	EventName string          `json:"eventName"`
	TxID      *string         `json:"txId"`
	BlockNo   *int64          `json:"blockNo"`
	Ts        *time.Time      `json:"ts"`
	Payload   json.RawMessage `json:"payload"`
}

// EventsAfter returns up to limit events with id > afterID (optionally for one deal).
func EventsAfter(ctx context.Context, q DBTX, afterID int64, dealID string, limit int) ([]EventView, error) {
	rows, err := q.Query(ctx, `SELECT id, deal_id, tranche_no, event_name, tx_id, block_no, ts, payload FROM deal_events
		WHERE id > $1 AND ($2 = '' OR deal_id = $2) ORDER BY id LIMIT $3`, afterID, dealID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EventView{}
	for rows.Next() {
		var e EventView
		if err := rows.Scan(&e.ID, &e.DealID, &e.TrancheNo, &e.EventName, &e.TxID, &e.BlockNo, &e.Ts, &e.Payload); err != nil {
			return nil, err
		}
		e.Ts = utc(e.Ts)
		out = append(out, e)
	}
	return out, rows.Err()
}
