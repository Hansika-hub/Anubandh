package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hansika-hub/anubandh/ingest/internal/logx"
	"github.com/Hansika-hub/anubandh/ingest/pipeline"
)

var secrets = map[string][]byte{
	"warehouse": []byte("warehouse-secret-0123456789"),
	"erp":       []byte("erp-secret-0123456789abcdef"),
	"gst":       []byte("gst-secret-0123456789abcdef"),
	"bank":      []byte("bank-secret-0123456789abcdef"),
}

type failingProducer struct{}

func (failingProducer) Publish(context.Context, pipeline.Message) error {
	return errors.New("kafka down")
}

func newTestServer(prod pipeline.Producer, br *pipeline.MemBroker, now func() time.Time, rps float64, burst int) (*server, *httptest.Server) {
	s := &server{prod: prod, secrets: secrets, limiter: newRateLimiter(rps, burst, now),
		dlq: &pipeline.DLQ{Reader: br, Out: br, Now: time.Now}, operatorToken: "op-token", log: logx.New("test", io.Discard)}
	return s, httptest.NewServer(s.routes())
}

const grBody = `{"eventType":"GOODS_RECEIPT","idemKey":"warehouse:GR-88123","dealId":"DL-20261020-0001","trancheNo":1,` +
	`"payload":{"lines":[{"lineNo":1,"deliveredQty":100}]},"occurredAt":"2026-10-20T07:54:00Z"}`

func post(t *testing.T, url, source, body, idem string, sign bool) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("X-Source", source)
	req.Header.Set("X-Idempotency-Key", idem)
	if sign {
		req.Header.Set("X-Signature", Sign(secrets[source], []byte(body)))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func errCode(t *testing.T, r *http.Response) string {
	t.Helper()
	defer r.Body.Close()
	var b errorBody
	_ = json.NewDecoder(r.Body).Decode(&b)
	return b.Error.Code
}

func TestAcceptedPublishesKeyedByDealID(t *testing.T) {
	br := pipeline.NewMemBroker()
	_, ts := newTestServer(br, br, nil, 50, 100)
	defer ts.Close()
	resp := post(t, ts.URL+"/ingest/v1/events/goods-receipt", "warehouse", grBody, "warehouse:GR-88123", true)
	defer resp.Body.Close()
	var got map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if resp.StatusCode != 202 || got["accepted"] != true || got["idemKey"] != "warehouse:GR-88123" {
		t.Fatalf("status=%d body=%v", resp.StatusCode, got)
	}
	msgs, _ := br.ReadAll(context.Background(), pipeline.TopicInbound)
	if len(msgs) != 1 || msgs[0].Key != "DL-20261020-0001" || msgs[0].Headers[pipeline.HdrIdemKey] != "warehouse:GR-88123" {
		t.Fatalf("published: %+v", msgs)
	}
}

func TestSignatureEnforced(t *testing.T) {
	br := pipeline.NewMemBroker()
	_, ts := newTestServer(br, br, nil, 50, 100)
	defer ts.Close()
	url := ts.URL + "/ingest/v1/events/goods-receipt"
	cases := []struct {
		name   string
		mutate func(*http.Request)
	}{
		{"missing signature", func(r *http.Request) { r.Header.Del("X-Signature") }},
		{"garbage signature", func(r *http.Request) { r.Header.Set("X-Signature", "zz") }},
		{"wrong secret", func(r *http.Request) {
			r.Header.Set("X-Signature", Sign([]byte("not-the-secret-123456"), []byte(grBody)))
		}},
		{"other source's secret", func(r *http.Request) { r.Header.Set("X-Signature", Sign(secrets["erp"], []byte(grBody))) }},
		{"tampered body", func(r *http.Request) {
			r.Body = io.NopCloser(strings.NewReader(strings.Replace(grBody, "100", "900", 1)))
		}},
		{"unknown source", func(r *http.Request) { r.Header.Set("X-Source", "mallory") }},
		{"missing source", func(r *http.Request) { r.Header.Del("X-Source") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(grBody))
			req.Header.Set("X-Source", "warehouse")
			req.Header.Set("X-Idempotency-Key", "warehouse:GR-88123")
			req.Header.Set("X-Signature", Sign(secrets["warehouse"], []byte(grBody)))
			c.mutate(req)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != 401 || errCode(t, resp) != "ERR_BAD_SIGNATURE" {
				t.Fatalf("status %d", resp.StatusCode)
			}
		})
	}
	if br.Len(pipeline.TopicInbound) != 0 {
		t.Fatal("nothing unsigned may reach Kafka")
	}
}

func TestSchemaAndHeaderValidation(t *testing.T) {
	br := pipeline.NewMemBroker()
	_, ts := newTestServer(br, br, nil, 50, 100)
	defer ts.Close()
	gr := ts.URL + "/ingest/v1/events/goods-receipt"
	qc := ts.URL + "/ingest/v1/events/quality-check"
	cases := []struct {
		name, url, src, body, idem string
		want                       int
		code                       string
	}{
		{"wrong endpoint for type", qc, "warehouse", grBody, "warehouse:GR-88123", 400, "ERR_BAD_REQUEST"},
		{"idem header mismatch", gr, "warehouse", grBody, "warehouse:OTHER", 400, "ERR_BAD_REQUEST"},
		{"idem header missing", gr, "warehouse", grBody, "", 400, "ERR_BAD_REQUEST"},
		{"idem prefix != source", gr, "erp", strings.Replace(grBody, "warehouse:GR", "warehouse:GR", 1), "warehouse:GR-88123", 400, "ERR_BAD_REQUEST"},
		{"bad payload", gr, "warehouse", strings.Replace(grBody, `"deliveredQty":100`, `"deliveredQty":0`, 1), "warehouse:GR-88123", 400, "ERR_BAD_REQUEST"},
		{"unknown envelope field", gr, "warehouse", strings.Replace(grBody, `"trancheNo":1,`, `"trancheNo":1,"evil":true,`, 1), "warehouse:GR-88123", 400, "ERR_BAD_REQUEST"},
		{"not json", gr, "warehouse", `nope`, "warehouse:GR-88123", 400, "ERR_BAD_REQUEST"},
		{"trailing data", gr, "warehouse", grBody + `{}`, "warehouse:GR-88123", 400, "ERR_BAD_REQUEST"},
		{"gst may not post goods receipts", gr, "gst", strings.Replace(grBody, "warehouse:", "gst:", 1), "gst:GR-88123", 403, "ERR_FORBIDDEN_ROLE"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := post(t, c.url, c.src, c.body, c.idem, true)
			if resp.StatusCode != c.want || errCode(t, resp) != c.code {
				t.Fatalf("status %d, want %d %s", resp.StatusCode, c.want, c.code)
			}
		})
	}
	if br.Len(pipeline.TopicInbound) != 0 {
		t.Fatal("rejected events must not be published")
	}
}

func TestAllFourEndpoints(t *testing.T) {
	br := pipeline.NewMemBroker()
	_, ts := newTestServer(br, br, nil, 50, 100)
	defer ts.Close()
	cases := []struct{ path, src, body string }{
		{"quality-check", "warehouse", `{"eventType":"QUALITY_CHECK","idemKey":"warehouse:QC-1","dealId":"DL-20261020-0001","trancheNo":1,"payload":{"results":[{"lineNo":1,"acceptedQty":80,"rejectedQty":20}]},"occurredAt":"2026-10-20T09:00:00Z"}`},
		{"invoice", "gst", `{"eventType":"INVOICE","idemKey":"gst:IRN-1","dealId":"DL-20261020-0001","payload":{"irn":"IRN-1"},"occurredAt":"2026-10-20T09:00:00Z"}`},
		{"bank-lien", "bank", `{"eventType":"BANK_LIEN","idemKey":"bank:LIEN-1","dealId":"DL-20261020-0001","payload":{"lienRef":"LIEN-1","amountPaise":100000000},"occurredAt":"2026-10-20T09:00:00Z"}`},
	}
	for _, c := range cases {
		var m map[string]any
		_ = json.Unmarshal([]byte(c.body), &m)
		resp := post(t, ts.URL+"/ingest/v1/events/"+c.path, c.src, c.body, m["idemKey"].(string), true)
		if resp.StatusCode != 202 {
			t.Fatalf("%s: %d %s", c.path, resp.StatusCode, errCode(t, resp))
		}
		resp.Body.Close()
	}
	if br.Len(pipeline.TopicInbound) != 3 {
		t.Fatalf("published %d", br.Len(pipeline.TopicInbound))
	}
}

func TestQueueDownIs503NotLost(t *testing.T) {
	br := pipeline.NewMemBroker()
	_, ts := newTestServer(failingProducer{}, br, nil, 50, 100)
	defer ts.Close()
	resp := post(t, ts.URL+"/ingest/v1/events/goods-receipt", "warehouse", grBody, "warehouse:GR-88123", true)
	if resp.StatusCode != 503 || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("status %d: a failed publish must NOT be acknowledged with 202", resp.StatusCode)
	}
}

func TestRateLimitPerSource(t *testing.T) {
	br := pipeline.NewMemBroker()
	var mu sync.Mutex
	clock := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	_, ts := newTestServer(br, br, now, 50, 100)
	defer ts.Close()
	url := ts.URL + "/ingest/v1/events/goods-receipt"
	var ok, limited int
	var retry string
	for i := 0; i < 130; i++ {
		resp := post(t, url, "warehouse", grBody, "warehouse:GR-88123", true)
		switch resp.StatusCode {
		case 202:
			ok++
		case 429:
			limited++
			retry = resp.Header.Get("Retry-After")
			if errCode(t, resp) != "ERR_RATE_LIMITED" {
				t.Fatal("wrong error code")
			}
		default:
			t.Fatalf("status %d", resp.StatusCode)
		}
		if resp.Body != nil {
			resp.Body.Close()
		}
	}
	if ok != 100 || limited != 30 || retry != "1" {
		t.Fatalf("ok=%d limited=%d retry=%q, want burst of exactly 100 then 429 with Retry-After 1", ok, limited, retry)
	}
	// another source is unaffected
	erpBody := strings.Replace(grBody, "warehouse:", "erp:", 1)
	if r := post(t, url, "erp", erpBody, "erp:GR-88123", true); r.StatusCode != 202 {
		t.Fatalf("erp throttled by warehouse traffic: %d", r.StatusCode)
	}
	// 50 rps: one second later 50 more tokens are available
	mu.Lock()
	clock = clock.Add(time.Second)
	mu.Unlock()
	ok = 0
	for i := 0; i < 60; i++ {
		resp := post(t, url, "warehouse", grBody, "warehouse:GR-88123", true)
		if resp.StatusCode == 202 {
			ok++
		}
		resp.Body.Close()
	}
	if ok != 50 {
		t.Fatalf("after 1s refill got %d, want 50", ok)
	}
}

func TestUnsignedTrafficCannotDrainBucket(t *testing.T) {
	br := pipeline.NewMemBroker()
	_, ts := newTestServer(br, br, nil, 1, 2)
	defer ts.Close()
	url := ts.URL + "/ingest/v1/events/goods-receipt"
	for i := 0; i < 20; i++ {
		post(t, url, "warehouse", grBody, "warehouse:GR-88123", false).Body.Close()
	}
	if r := post(t, url, "warehouse", grBody, "warehouse:GR-88123", true); r.StatusCode != 202 {
		t.Fatalf("legit request throttled by forged traffic: %d", r.StatusCode)
	}
}

func TestBodyTooLarge(t *testing.T) {
	br := pipeline.NewMemBroker()
	_, ts := newTestServer(br, br, nil, 50, 100)
	defer ts.Close()
	big := `{"x":"` + strings.Repeat("a", maxBody+10) + `"}`
	resp := post(t, ts.URL+"/ingest/v1/events/goods-receipt", "warehouse", big, "warehouse:GR-88123", true)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestDLQEndpoints(t *testing.T) {
	br := pipeline.NewMemBroker()
	s, ts := newTestServer(br, br, nil, 50, 100)
	defer ts.Close()
	w := pipeline.NewWorker(pipeline.MockChain{}, pipeline.NewMemStore(), br, pipeline.WorkerConfig{}, nil)
	var ev pipeline.InboundEvent
	_ = json.Unmarshal([]byte(grBody), &ev)
	if err := w.SendToDLQ(context.Background(), ev, "chain down"); err != nil {
		t.Fatal(err)
	}
	do := func(method, path, token string) (*http.Response, []byte) {
		req, _ := http.NewRequest(method, ts.URL+path, nil)
		if token != "" {
			req.Header.Set("X-Operator-Token", token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, b
	}
	if r, _ := do("GET", "/ingest/v1/dlq", ""); r.StatusCode != 401 {
		t.Fatalf("no token: %d", r.StatusCode)
	}
	if r, _ := do("GET", "/ingest/v1/dlq", "wrong"); r.StatusCode != 401 {
		t.Fatalf("wrong token: %d", r.StatusCode)
	}
	r, b := do("GET", "/ingest/v1/dlq", "op-token")
	var list struct {
		Items []pipeline.DLQEntry `json:"items"`
		Count int                 `json:"count"`
	}
	_ = json.Unmarshal(b, &list)
	if r.StatusCode != 200 || list.Count != 1 || list.Items[0].Reason != "chain down" {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	id := list.Items[0].ID
	if r, _ := do("POST", "/ingest/v1/dlq/nope/replay", "op-token"); r.StatusCode != 404 {
		t.Fatalf("unknown id: %d", r.StatusCode)
	}
	if r, b := do("POST", "/ingest/v1/dlq/"+id+"/replay", "op-token"); r.StatusCode != 202 {
		t.Fatalf("replay: %d %s", r.StatusCode, b)
	}
	if r, _ := do("POST", "/ingest/v1/dlq/"+id+"/replay", "op-token"); r.StatusCode != 409 {
		t.Fatalf("second replay: %d", r.StatusCode)
	}
	if br.Len(pipeline.TopicInbound) != 1 {
		t.Fatal("replay must re-enqueue the event")
	}
	// disabled when no token is configured
	s.operatorToken = ""
	if r, _ := do("GET", "/ingest/v1/dlq", "op-token"); r.StatusCode != 403 {
		t.Fatalf("disabled operator endpoints: %d", r.StatusCode)
	}
}

func TestHealth(t *testing.T) {
	br := pipeline.NewMemBroker()
	_, ts := newTestServer(br, br, nil, 50, 100)
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/ingest/v1/health")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("%v", err)
	}
	b, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(b, []byte(`"ok"`)) {
		t.Fatal(string(b))
	}
}
