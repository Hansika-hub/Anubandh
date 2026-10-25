package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Hansika-hub/anubandh/ingest/pipeline"
)

func TestDLQTool(t *testing.T) {
	br := pipeline.NewMemBroker()
	be := &backend{prod: br, reader: br}
	w := pipeline.NewWorker(pipeline.MockChain{}, pipeline.NewMemStore(), br, pipeline.WorkerConfig{}, nil)
	var ev pipeline.InboundEvent
	_ = json.Unmarshal([]byte(`{"eventType":"BANK_LIEN","idemKey":"bank:L-1","dealId":"DL-20261020-0001","payload":{"lienRef":"L","amountPaise":5},"occurredAt":"2026-10-20T09:00:00Z"}`), &ev)
	_ = w.SendToDLQ(context.Background(), ev, "gateway unreachable")

	var out, errw bytes.Buffer
	if code := dlqTool(context.Background(), be, []string{"list"}, &out, &errw); code != 0 || !strings.Contains(out.String(), "gateway unreachable") {
		t.Fatalf("list: code=%d out=%s err=%s", code, out.String(), errw.String())
	}
	var entries []pipeline.DLQEntry
	_ = json.Unmarshal(out.Bytes(), &entries)
	out.Reset()
	if code := dlqTool(context.Background(), be, []string{"replay", entries[0].ID}, &out, &errw); code != 0 || br.Len(pipeline.TopicInbound) != 1 {
		t.Fatalf("replay: code=%d err=%s", code, errw.String())
	}
	if code := dlqTool(context.Background(), be, []string{"replay", entries[0].ID}, &out, &errw); code != 2 {
		t.Fatalf("second replay should exit 2, got %d", code)
	}
	if code := dlqTool(context.Background(), be, []string{"bogus"}, &out, &errw); code != 2 {
		t.Fatalf("usage exit code: %d", code)
	}
}
