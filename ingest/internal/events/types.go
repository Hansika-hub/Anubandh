// Package events defines the unified inbound envelope (BUILD.md section 9.2), its validation, and
// the mapping from an event to the gateway's internal submit call. Stdlib only.
package events

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// EventType is the closed set of inbound event kinds.
type EventType string

const (
	GoodsReceipt EventType = "GOODS_RECEIPT"
	QualityCheck EventType = "QUALITY_CHECK"
	Invoice      EventType = "INVOICE"
	BankLien     EventType = "BANK_LIEN"
)

// InboundEvent is the envelope every webhook carries and every Kafka message holds.
type InboundEvent struct {
	EventType  EventType       `json:"eventType"`
	IdemKey    string          `json:"idemKey"`
	DealID     string          `json:"dealId"`
	TrancheNo  int64           `json:"trancheNo,omitempty"`
	Payload    json.RawMessage `json:"payload"`
	OccurredAt time.Time       `json:"occurredAt"`
}

// Same expressions the gateway enforces on /internal/v1/submit; rejecting here keeps bad events
// out of Kafka instead of bouncing them through five retries.
var (
	dealIDRe  = regexp.MustCompile(`^DL-\d{8}-\d{4}$`)
	idemKeyRe = regexp.MustCompile(`^[a-z0-9_-]{1,32}:[A-Za-z0-9._-]{1,95}$`) // chaincode core/idempotency.go
)

const maxQty = 1_000_000_000

// ValidationError is a client-side schema failure (HTTP 400 ERR_BAD_REQUEST).
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func bad(format string, a ...any) error { return &ValidationError{Msg: fmt.Sprintf(format, a...)} }

// GRLine is one delivered line of a goods receipt (chaincode linesJSON element).
type GRLine struct {
	LineNo       int64 `json:"lineNo"`
	DeliveredQty int64 `json:"deliveredQty"`
}

// QCResult is one line of a quality check (chaincode resultsJSON element).
type QCResult struct {
	LineNo      int64 `json:"lineNo"`
	AcceptedQty int64 `json:"acceptedQty"`
	RejectedQty int64 `json:"rejectedQty"`
}

type invoicePayload struct {
	IRN string `json:"irn"`
}

type lienPayload struct {
	LienRef     string `json:"lienRef"`
	AmountPaise int64  `json:"amountPaise"`
}

func strictDecode(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("trailing data")
	}
	return nil
}

// payload accepts either a bare array or an object wrapper ({"lines":[...]} / {"results":[...]}).
func decodeList[T any](raw json.RawMessage, wrapperKey string) ([]T, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var out []T
		return out, strictDecode(trimmed, &out)
	}
	var m map[string]json.RawMessage
	if err := strictDecode(trimmed, &m); err != nil {
		return nil, err
	}
	if len(m) != 1 {
		return nil, fmt.Errorf("payload object must contain only %q", wrapperKey)
	}
	inner, ok := m[wrapperKey]
	if !ok {
		return nil, fmt.Errorf("payload object must contain %q", wrapperKey)
	}
	var out []T
	return out, strictDecode(inner, &out)
}

// Validate checks the envelope and the type-specific payload.
func (e *InboundEvent) Validate() error {
	switch e.EventType {
	case GoodsReceipt, QualityCheck, Invoice, BankLien:
	default:
		return bad("eventType must be one of GOODS_RECEIPT, QUALITY_CHECK, INVOICE, BANK_LIEN")
	}
	if !idemKeyRe.MatchString(e.IdemKey) {
		return bad("idemKey must look like <source>:<id>")
	}
	if !dealIDRe.MatchString(e.DealID) {
		return bad("dealId must look like DL-YYYYMMDD-NNNN")
	}
	if e.OccurredAt.IsZero() {
		return bad("occurredAt is required (RFC 3339)")
	}
	if len(bytes.TrimSpace(e.Payload)) == 0 || bytes.Equal(bytes.TrimSpace(e.Payload), []byte("null")) {
		return bad("payload is required")
	}
	_, _, err := e.ChainCall()
	return err
}

func (e *InboundEvent) needTranche() error {
	if e.TrancheNo < 1 {
		return bad("trancheNo must be >= 1 for %s", e.EventType)
	}
	return nil
}

// ChainCall maps the event to the gateway fn and args (BUILD.md section 9.4). Args are strings;
// JSON args are compact JSON. A *ValidationError means the payload is malformed.
func (e *InboundEvent) ChainCall() (fn string, args []string, err error) {
	switch e.EventType {
	case GoodsReceipt:
		if err := e.needTranche(); err != nil {
			return "", nil, err
		}
		lines, derr := decodeList[GRLine](e.Payload, "lines")
		if derr != nil {
			return "", nil, bad("GOODS_RECEIPT payload: %v", derr)
		}
		if len(lines) == 0 {
			return "", nil, bad("GOODS_RECEIPT needs at least one line")
		}
		seen := map[int64]bool{}
		for _, l := range lines {
			if l.LineNo < 1 || l.DeliveredQty < 1 || l.DeliveredQty > maxQty {
				return "", nil, bad("GOODS_RECEIPT line %d: lineNo>=1 and 1<=deliveredQty<=%d required", l.LineNo, maxQty)
			}
			if seen[l.LineNo] {
				return "", nil, bad("GOODS_RECEIPT duplicate lineNo %d", l.LineNo)
			}
			seen[l.LineNo] = true
		}
		b, _ := json.Marshal(lines)
		return "PostGoodsReceipt", []string{e.DealID, strconv.FormatInt(e.TrancheNo, 10), string(b), e.IdemKey}, nil
	case QualityCheck:
		if err := e.needTranche(); err != nil {
			return "", nil, err
		}
		res, derr := decodeList[QCResult](e.Payload, "results")
		if derr != nil {
			return "", nil, bad("QUALITY_CHECK payload: %v", derr)
		}
		if len(res) == 0 {
			return "", nil, bad("QUALITY_CHECK needs at least one result")
		}
		seen := map[int64]bool{}
		for _, r := range res {
			if r.LineNo < 1 || r.AcceptedQty < 0 || r.RejectedQty < 0 || r.AcceptedQty+r.RejectedQty < 1 ||
				r.AcceptedQty > maxQty || r.RejectedQty > maxQty {
				return "", nil, bad("QUALITY_CHECK line %d: lineNo>=1, quantities in [0,%d], not both zero", r.LineNo, maxQty)
			}
			if seen[r.LineNo] {
				return "", nil, bad("QUALITY_CHECK duplicate lineNo %d", r.LineNo)
			}
			seen[r.LineNo] = true
		}
		b, _ := json.Marshal(res)
		return "RecordQualityCheck", []string{e.DealID, strconv.FormatInt(e.TrancheNo, 10), string(b), e.IdemKey}, nil
	case Invoice:
		var p invoicePayload
		if derr := strictDecode(e.Payload, &p); derr != nil {
			return "", nil, bad("INVOICE payload: %v", derr)
		}
		if p.IRN == "" || len(p.IRN) > 128 {
			return "", nil, bad("INVOICE payload needs irn (1..128 chars)")
		}
		return "VerifyInvoice", []string{e.DealID, p.IRN, e.IdemKey}, nil
	case BankLien:
		var p lienPayload
		if derr := strictDecode(e.Payload, &p); derr != nil {
			return "", nil, bad("BANK_LIEN payload: %v", derr)
		}
		if p.LienRef == "" || len(p.LienRef) > 128 || p.AmountPaise < 1 {
			return "", nil, bad("BANK_LIEN payload needs lienRef and amountPaise>0")
		}
		// ConfirmLien has no idemKey argument on chain; idempotency is the worker's processed_keys.
		return "ConfirmLien", []string{e.DealID, p.LienRef, strconv.FormatInt(p.AmountPaise, 10)}, nil
	}
	return "", nil, bad("unknown eventType %q", e.EventType)
}
