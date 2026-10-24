package api

import (
	"encoding/csv"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/Hansika-hub/anubandh/gateway/internal/model"
	"github.com/Hansika-hub/anubandh/gateway/internal/store"
)

// MseClockItem is one row of GET /mse/clocks.
type MseClockItem struct {
	DealID           string     `json:"dealId"`
	TrancheNo        int64      `json:"trancheNo"`
	BuyerID          string     `json:"buyerId"`
	SupplierID       string     `json:"supplierId"`
	MseCategory      *string    `json:"mseCategory"`
	TermDays         *int       `json:"termDays"`
	UdyamNo          *string    `json:"udyamNo"`
	Status           string     `json:"status"`
	AmountPaise      int64      `json:"amountPaise"`
	DeliveredAt      *time.Time `json:"deliveredAt"`
	AcceptedAt       *time.Time `json:"acceptedAt"`
	DeemedAcceptance bool       `json:"deemedAcceptance"`
	MseClockStart    *time.Time `json:"mseClockStart"`
	MseDeadline      *time.Time `json:"mseDeadline"`
	DaysLeft         int64      `json:"daysLeft"`
	SecondsLeft      int64      `json:"secondsLeft"`
	Paid             bool       `json:"paid"`
	PaidAt           *time.Time `json:"paidAt"`
	AtRisk           bool       `json:"atRisk"`
	Overdue          bool       `json:"overdue"`
}

func paid(r store.MseTrancheRow) (bool, *time.Time) {
	switch {
	case r.SettledAt != nil:
		return true, r.SettledAt
	case r.ReleasedAt != nil:
		return true, r.ReleasedAt
	case r.Status == model.TrancheReleased || r.Status == model.TrancheSettled:
		return true, nil
	}
	return false, nil
}

// daysLeft rounds up so "4 days 1 hour" shows as 5; overdue values are negative.
func daysLeft(d time.Duration) int64 {
	return int64(math.Ceil(d.Hours() / 24))
}

// mseClocks lists MSMED Act payment clocks. riskDays (optional) keeps only unpaid tranches due
// within that many days (overdue included); atRisk always uses riskDays or 5.
func (h *Handlers) mseClocks(w http.ResponseWriter, r *http.Request) {
	risk, err := queryInt(r, "riskDays", -1, 0, 3650)
	if err != nil {
		writeErr(w, h.log, err)
		return
	}
	rows, err := store.MseTranches(r.Context(), h.store.Pool)
	if err != nil {
		writeErr(w, h.log, err)
		return
	}
	threshold := int64(5)
	if risk >= 0 {
		threshold = int64(risk)
	}
	q := r.URL.Query()
	now := h.clock.Now()
	items := []MseClockItem{}
	for _, row := range rows {
		if s := q.Get("supplierId"); s != "" && row.SupplierID != s {
			continue
		}
		if s := q.Get("dealId"); s != "" && row.DealID != s {
			continue
		}
		isPaid, paidAt := paid(row)
		left := row.MseDeadline.Sub(now)
		it := MseClockItem{
			DealID: row.DealID, TrancheNo: row.TrancheNo, BuyerID: row.BuyerID, SupplierID: row.SupplierID,
			MseCategory: row.Category, TermDays: row.TermDays, UdyamNo: row.UdyamNo, Status: row.Status,
			AmountPaise: row.AcceptedValuePaise, DeliveredAt: row.DeliveredAt, AcceptedAt: row.AcceptedAt,
			DeemedAcceptance: row.Deemed, MseClockStart: row.MseClockStart, MseDeadline: row.MseDeadline,
			DaysLeft: daysLeft(left), SecondsLeft: int64(left / time.Second), Paid: isPaid, PaidAt: paidAt,
		}
		it.Overdue = !isPaid && left < 0
		it.AtRisk = !isPaid && it.DaysLeft <= threshold
		if risk >= 0 && !it.AtRisk {
			continue
		}
		items = append(items, it)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items), "asOf": now.Truncate(time.Second), "riskDays": threshold})
}

// ComplianceItem is one row of the auditor's MSE compliance report.
type ComplianceItem struct {
	DealID           string     `json:"dealId"`
	TrancheNo        int64      `json:"trancheNo"`
	BuyerID          string     `json:"buyerId"`
	SupplierID       string     `json:"supplierId"`
	UdyamNo          *string    `json:"udyamNo"`
	MseCategory      *string    `json:"mseCategory"`
	TermDays         *int       `json:"termDays"`
	AmountPaise      int64      `json:"amountPaise"`
	DeliveredAt      *time.Time `json:"deliveredAt"`
	AcceptanceDate   *time.Time `json:"acceptanceDate"`
	DeemedAcceptance bool       `json:"deemedAcceptance"`
	MseDeadline      *time.Time `json:"mseDeadline"`
	PaidDate         *time.Time `json:"paidDate"`
	OnTime           *bool      `json:"onTime"`
	DaysLate         int64      `json:"daysLate"`
	ComplianceStatus string     `json:"complianceStatus"` // PAID_ON_TIME | PAID_LATE | PENDING | OVERDUE
}

// complianceReport is the auditor's export of every MSE tranche: acceptance date (statutory
// "appointed day" anchor), due date, paid date and an on-time flag. ?format=csv downloads CSV.
func (h *Handlers) complianceReport(w http.ResponseWriter, r *http.Request) {
	if err := requireOrg(principalFrom(r.Context()), model.OrgAuditor); err != nil {
		writeErr(w, h.log, err)
		return
	}
	rows, err := store.MseTranches(r.Context(), h.store.Pool)
	if err != nil {
		writeErr(w, h.log, err)
		return
	}
	now := h.clock.Now()
	items := []ComplianceItem{}
	summary := map[string]int{"PAID_ON_TIME": 0, "PAID_LATE": 0, "PENDING": 0, "OVERDUE": 0}
	for _, row := range rows {
		_, paidAt := paid(row)
		acc := row.MseClockStart
		if acc == nil {
			acc = row.AcceptedAt
		}
		it := ComplianceItem{
			DealID: row.DealID, TrancheNo: row.TrancheNo, BuyerID: row.BuyerID, SupplierID: row.SupplierID, UdyamNo: row.UdyamNo,
			MseCategory: row.Category, TermDays: row.TermDays, AmountPaise: row.AcceptedValuePaise, DeliveredAt: row.DeliveredAt,
			AcceptanceDate: acc, DeemedAcceptance: row.Deemed, MseDeadline: row.MseDeadline, PaidDate: paidAt,
		}
		deadline := *row.MseDeadline
		switch {
		case paidAt != nil:
			onTime := !paidAt.After(deadline)
			it.OnTime = &onTime
			if onTime {
				it.ComplianceStatus = "PAID_ON_TIME"
			} else {
				it.ComplianceStatus = "PAID_LATE"
				it.DaysLate = daysLeft(paidAt.Sub(deadline))
			}
		case now.After(deadline):
			f := false
			it.OnTime = &f
			it.ComplianceStatus = "OVERDUE"
			it.DaysLate = daysLeft(now.Sub(deadline))
		default:
			it.ComplianceStatus = "PENDING"
		}
		summary[it.ComplianceStatus]++
		items = append(items, it)
	}
	if r.URL.Query().Get("format") == "csv" {
		writeComplianceCSV(w, items)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items), "summary": summary, "generatedAt": now.Truncate(time.Second)})
}

func fmtTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func writeComplianceCSV(w http.ResponseWriter, items []ComplianceItem) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="mse-compliance-report.csv"`)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"dealId", "trancheNo", "buyerId", "supplierId", "udyamNo", "mseCategory", "termDays", "amountPaise",
		"deliveredAt", "acceptanceDate", "deemedAcceptance", "mseDeadline", "paidDate", "onTime", "daysLate", "complianceStatus"})
	for _, it := range items {
		onTime := ""
		if it.OnTime != nil {
			onTime = strconv.FormatBool(*it.OnTime)
		}
		_ = cw.Write([]string{csvSafe(it.DealID), strconv.FormatInt(it.TrancheNo, 10), csvSafe(it.BuyerID), csvSafe(it.SupplierID),
			csvSafe(deref(it.UdyamNo)), deref(it.MseCategory), strconv.Itoa(deref(it.TermDays)), strconv.FormatInt(it.AmountPaise, 10),
			fmtTime(it.DeliveredAt), fmtTime(it.AcceptanceDate), strconv.FormatBool(it.DeemedAcceptance), fmtTime(it.MseDeadline),
			fmtTime(it.PaidDate), onTime, strconv.FormatInt(it.DaysLate, 10), it.ComplianceStatus})
	}
	cw.Flush()
}

// csvSafe neutralises spreadsheet formula injection (=, +, -, @ at the start of a cell).
func csvSafe(s string) string {
	if s != "" && (s[0] == '=' || s[0] == '+' || s[0] == '-' || s[0] == '@' || s[0] == '\t' || s[0] == '\r') {
		return "'" + s
	}
	return s
}
