// Package e2e runs the gateway end to end: production wiring (internal/app), real PostgreSQL,
// the mock deal chaincode and a fake bank. Set PG_TEST_DSN to run; the database is wiped.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hansika-hub/anubandh/gateway/internal/app"
	"github.com/Hansika-hub/anubandh/gateway/internal/bank"
	"github.com/Hansika-hub/anubandh/gateway/internal/clock"
	"github.com/Hansika-hub/anubandh/gateway/internal/config"
	"github.com/Hansika-hub/anubandh/gateway/internal/fabric"
	"github.com/Hansika-hub/anubandh/gateway/internal/store"
)

func TestMain(m *testing.M) {
	if os.Getenv("PG_TEST_DSN") == "" {
		fmt.Println("e2e: PG_TEST_DSN not set; skipping (see gateway/README.md)")
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fakeBank implements the sim-bank contract (BUILD.md §11): duplicate instructionId returns the
// same bankRef; _chaos failNext makes the next n payment calls fail.
type fakeBank struct {
	mu          sync.Mutex
	byInstr     map[string]*fakePayment
	byRef       map[string]*fakePayment
	failNext    int
	failStatus  int
	settleOnGet bool
	sendCalls   int
	seq         int
}

type fakePayment struct {
	instr   bank.PaymentInstruction
	bankRef string
	status  string
	sends   int
}

func newFakeBank() *fakeBank {
	return &fakeBank{byInstr: map[string]*fakePayment{}, byRef: map[string]*fakePayment{}, failStatus: 500, settleOnGet: true}
}

func (b *fakeBank) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/bank/v1/payments":
		b.sendCalls++
		if b.failNext > 0 {
			b.failNext--
			http.Error(w, `{"error":"chaos"}`, b.failStatus)
			return
		}
		var in bank.PaymentInstruction
		_ = json.NewDecoder(r.Body).Decode(&in)
		p, ok := b.byInstr[in.InstructionID]
		if !ok {
			b.seq++
			p = &fakePayment{instr: in, bankRef: fmt.Sprintf("UTR-%04d", b.seq), status: bank.BankAccepted}
			b.byInstr[in.InstructionID], b.byRef[p.bankRef] = p, p
		}
		p.sends++
		_ = json.NewEncoder(w).Encode(map[string]string{"bankRef": p.bankRef, "status": p.status})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/bank/v1/payments/"):
		p, ok := b.byRef[strings.TrimPrefix(r.URL.Path, "/bank/v1/payments/")]
		if !ok {
			http.Error(w, "not found", 404)
			return
		}
		if b.settleOnGet {
			p.status = bank.BankSettled
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": p.status})
	default:
		http.Error(w, "no route", 404)
	}
}

func (b *fakeBank) payments() map[string]*fakePayment {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := map[string]*fakePayment{}
	for k, v := range b.byInstr {
		cp := *v
		out[k] = &cp
	}
	return out
}

type env struct {
	t      *testing.T
	ctx    context.Context
	app    *app.App
	mock   *fabric.MockClient
	clk    *clock.Offset
	srv    *httptest.Server
	bank   *fakeBank
	bankS  *httptest.Server
	st     *store.Store
	stopL  context.CancelFunc
	lstCtx context.Context
}

type options struct{ noAutoRelease bool }

func setup(t *testing.T) *env { return setupWith(t, options{}) }

func setupWith(t *testing.T, o options) *env {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	st, err := store.Open(ctx, os.Getenv("PG_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if _, err := st.Pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // migrations must be idempotent
		if _, err := st.Migrate(ctx, "../../../db/migrations"); err != nil {
			t.Fatalf("migrate pass %d: %v", i+1, err)
		}
	}
	fb := newFakeBank()
	bankSrv := httptest.NewServer(fb)
	t.Cleanup(bankSrv.Close)

	clk := clock.NewOffset(nil)
	mock, err := fabric.NewMockClient(fabric.MockOptions{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Channel: "anubandh-ch", Chaincode: "deal", UsersFile: "../../config/users.json",
		OutboxPoll: 50 * time.Millisecond, OutboxMaxAttempts: 4, TickInterval: time.Hour, ListenerID: "e2e",
		AutoRelease: !o.noAutoRelease, CORSOrigins: []string{"http://localhost:3000"},
	}
	level := slog.LevelWarn
	if os.Getenv("E2E_VERBOSE") != "" {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	fast := fabric.RetryPolicy{MaxAttempts: 5, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond}
	a, err := app.Build(ctx, cfg, st, log, "e2e", app.Overrides{Chain: mock, Bank: bank.NewHTTPClient(bankSrv.URL), RetryPolicy: &fast})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a.Handler)
	t.Cleanup(srv.Close)
	e := &env{t: t, ctx: ctx, app: a, mock: mock, clk: clk, srv: srv, bank: fb, bankS: bankSrv, st: st}
	e.startListener()
	// Runs first (cleanups are LIFO): stop background work before the pool and servers close.
	t.Cleanup(func() {
		cancel()
		deadline := time.Now().Add(5 * time.Second)
		for a.Listener.Status().Running && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(50 * time.Millisecond)
	})
	return e
}

func (e *env) startListener() {
	lctx, cancel := context.WithCancel(e.ctx)
	e.stopL, e.lstCtx = cancel, lctx
	go e.app.Listener.Run(lctx)
	e.eventually("listener running", func() bool { return e.app.Listener.Status().Running })
}

func (e *env) stopListener() {
	e.stopL()
	e.eventually("listener stopped", func() bool { return !e.app.Listener.Status().Running })
}

type resp struct {
	status int
	body   map[string]any
	raw    []byte
	header http.Header
}

func (e *env) do(method, path, org, user string, body any) resp {
	e.t.Helper()
	var rdr io.Reader
	if body != nil {
		if s, ok := body.(string); ok {
			rdr = strings.NewReader(s)
		} else {
			b, _ := json.Marshal(body)
			rdr = bytes.NewReader(b)
		}
	}
	req, _ := http.NewRequestWithContext(e.ctx, method, e.srv.URL+path, rdr)
	if org != "" {
		req.Header.Set("X-Org", org)
	}
	if user != "" {
		req.Header.Set("X-User", user)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	out := resp{status: res.StatusCode, raw: raw, header: res.Header}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	_ = dec.Decode(&out.body)
	return out
}

func (e *env) must(want int, r resp, what string) resp {
	e.t.Helper()
	if r.status != want {
		e.t.Fatalf("%s: want HTTP %d, got %d: %s", what, want, r.status, r.raw)
	}
	return r
}

func (e *env) mustErr(wantStatus int, wantCode string, r resp, what string) {
	e.t.Helper()
	code := ""
	if er, ok := r.body["error"].(map[string]any); ok {
		code, _ = er["code"].(string)
	}
	if r.status != wantStatus || code != wantCode {
		e.t.Fatalf("%s: want %d %s, got %d %s", what, wantStatus, wantCode, r.status, r.raw)
	}
}

func (e *env) internal(fn string, args ...string) resp {
	idem := ""
	if len(args) == 4 {
		idem = args[3]
	}
	return e.do("POST", "/internal/v1/submit", "", "", map[string]any{"fn": fn, "args": args, "idemKey": idem})
}

func (e *env) eventually(what string, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.t.Fatalf("timed out waiting for %s", what)
}

// processOutbox drives the payment outbox until it stops changing.
func (e *env) processOutbox() {
	e.t.Helper()
	for i := 0; i < 5; i++ {
		if err := e.app.Adapter.ProcessOnce(e.ctx); err != nil {
			e.t.Fatal(err)
		}
	}
}

func (e *env) scalar(sql string, args ...any) int64 {
	e.t.Helper()
	var n int64
	if err := e.st.Pool.QueryRow(e.ctx, sql, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func (e *env) text(sql string, args ...any) string {
	e.t.Helper()
	var s *string
	if err := e.st.Pool.QueryRow(e.ctx, sql, args...).Scan(&s); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	if s == nil {
		return ""
	}
	return *s
}

// waitIdle waits until the listener has processed every event on the mock ledger.
func (e *env) waitIdle() {
	e.t.Helper()
	e.eventually("listener caught up", func() bool {
		return e.scalar(`SELECT count(*) FROM deal_events`) >= int64(e.mock.EventCount())
	})
	time.Sleep(30 * time.Millisecond) // let the post-event reconcile finish
}

func num(v any) int64 {
	switch x := v.(type) {
	case json.Number:
		i, _ := x.Int64()
		return i
	case float64:
		return int64(x)
	}
	return -1
}

func obj(v any) map[string]any { m, _ := v.(map[string]any); return m }
func arr(v any) []any          { a, _ := v.([]any); return a }

func (e *env) dealBody(id, supplier, category string, termDays int, applies bool, qty, price int64, extra string) string {
	return fmt.Sprintf(`{"dealId":%q,"poNumber":"PO-4411","buyerId":"BUYER-001","supplierId":%q,
		"deliveryDeadline":%q,"mse":{"category":%q,"termDays":%d,"udyamNo":"UDYAM-KL-00-0000007","applies":%v}%s,
		"lines":[{"lineNo":1,"sku":"SKU-STEEL-12","orderedQty":%d,"unitPricePaise":%d}]}`,
		id, supplier, e.clk.Now().Add(60*24*time.Hour).UTC().Format(time.RFC3339), category, termDays, applies, extra, qty, price)
}
