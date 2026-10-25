package events

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func ev(t EventType, tranche int64, payload string) InboundEvent {
	return InboundEvent{EventType: t, IdemKey: "warehouse:GR-1", DealID: "DL-20261020-0001", TrancheNo: tranche,
		Payload: json.RawMessage(payload), OccurredAt: time.Date(2026, 10, 20, 7, 54, 0, 0, time.UTC)}
}

func TestChainCallMapping(t *testing.T) {
	cases := []struct {
		name string
		e    InboundEvent
		fn   string
		args []string
	}{
		{"GR object", ev(GoodsReceipt, 1, `{"lines":[{"lineNo":1,"deliveredQty":100}]}`), "PostGoodsReceipt",
			[]string{"DL-20261020-0001", "1", `[{"lineNo":1,"deliveredQty":100}]`, "warehouse:GR-1"}},
		{"GR bare array", ev(GoodsReceipt, 2, `[{"lineNo":1,"deliveredQty":5}]`), "PostGoodsReceipt",
			[]string{"DL-20261020-0001", "2", `[{"lineNo":1,"deliveredQty":5}]`, "warehouse:GR-1"}},
		{"QC", ev(QualityCheck, 1, `{"results":[{"lineNo":1,"acceptedQty":80,"rejectedQty":20}]}`), "RecordQualityCheck",
			[]string{"DL-20261020-0001", "1", `[{"lineNo":1,"acceptedQty":80,"rejectedQty":20}]`, "warehouse:GR-1"}},
		{"invoice", ev(Invoice, 0, `{"irn":"IRN-77"}`), "VerifyInvoice", []string{"DL-20261020-0001", "IRN-77", "warehouse:GR-1"}},
		{"lien", ev(BankLien, 0, `{"lienRef":"LIEN-9","amountPaise":100000000}`), "ConfirmLien", []string{"DL-20261020-0001", "LIEN-9", "100000000"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.e.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			fn, args, err := c.e.ChainCall()
			if err != nil || fn != c.fn || strings.Join(args, "|") != strings.Join(c.args, "|") {
				t.Fatalf("got %q %q %v, want %q %q", fn, args, err, c.fn, c.args)
			}
		})
	}
}

func TestValidateRejects(t *testing.T) {
	good := func() InboundEvent { return ev(GoodsReceipt, 1, `{"lines":[{"lineNo":1,"deliveredQty":10}]}`) }
	mut := []struct {
		name string
		f    func(e *InboundEvent)
	}{
		{"bad type", func(e *InboundEvent) { e.EventType = "NOPE" }},
		{"bad idem key", func(e *InboundEvent) { e.IdemKey = "nocolon" }},
		{"upper-case idem source", func(e *InboundEvent) { e.IdemKey = "ERP:QC-1" }}, // chaincode rejects it
		{"slash in idem source", func(e *InboundEvent) { e.IdemKey = "erp/x:QC-1" }},
		{"bad deal id", func(e *InboundEvent) { e.DealID = "DL-1" }},
		{"no occurredAt", func(e *InboundEvent) { e.OccurredAt = time.Time{} }},
		{"no payload", func(e *InboundEvent) { e.Payload = nil }},
		{"null payload", func(e *InboundEvent) { e.Payload = json.RawMessage(`null`) }},
		{"GR no tranche", func(e *InboundEvent) { e.TrancheNo = 0 }},
		{"GR empty lines", func(e *InboundEvent) { e.Payload = json.RawMessage(`{"lines":[]}`) }},
		{"GR zero qty", func(e *InboundEvent) { e.Payload = json.RawMessage(`{"lines":[{"lineNo":1,"deliveredQty":0}]}`) }},
		{"GR negative qty", func(e *InboundEvent) { e.Payload = json.RawMessage(`{"lines":[{"lineNo":1,"deliveredQty":-3}]}`) }},
		{"GR dup line", func(e *InboundEvent) {
			e.Payload = json.RawMessage(`[{"lineNo":1,"deliveredQty":1},{"lineNo":1,"deliveredQty":2}]`)
		}},
		{"GR unknown field", func(e *InboundEvent) {
			e.Payload = json.RawMessage(`{"lines":[{"lineNo":1,"deliveredQty":1,"x":1}]}`)
		}},
		{"GR extra key", func(e *InboundEvent) { e.Payload = json.RawMessage(`{"lines":[],"x":1}`) }},
		{"GR huge qty", func(e *InboundEvent) { e.Payload = json.RawMessage(`[{"lineNo":1,"deliveredQty":9999999999}]`) }},
		{"GR wrong key", func(e *InboundEvent) { e.Payload = json.RawMessage(`{"results":[]}`) }},
		{"GR garbage", func(e *InboundEvent) { e.Payload = json.RawMessage(`"str"`) }},
		{"QC both zero", func(e *InboundEvent) {
			e.EventType = QualityCheck
			e.Payload = json.RawMessage(`[{"lineNo":1,"acceptedQty":0,"rejectedQty":0}]`)
		}},
		{"invoice no irn", func(e *InboundEvent) { e.EventType = Invoice; e.Payload = json.RawMessage(`{}`) }},
		{"lien zero", func(e *InboundEvent) {
			e.EventType = BankLien
			e.Payload = json.RawMessage(`{"lienRef":"L","amountPaise":0}`)
		}},
	}
	g := good()
	if err := g.Validate(); err != nil {
		t.Fatalf("baseline must be valid: %v", err)
	}
	for _, m := range mut {
		t.Run(m.name, func(t *testing.T) {
			e := good()
			m.f(&e)
			err := e.Validate()
			var ve *ValidationError
			if err == nil || !errors.As(err, &ve) {
				t.Fatalf("want ValidationError, got %v", err)
			}
		})
	}
}
