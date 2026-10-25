// Burst test (BUILD.md M3 Day 9): 500 duplicate + out-of-order events must produce 0 double releases.
//
// It runs the REAL pipeline.Worker (the code ingest-worker runs) against an HTTP fake of the
// gateway's /internal/v1/submit that models the deal state machine
//
//	CREATED -ConfirmLien-> FUNDED -PostGoodsReceipt-> DELIVERED -RecordQualityCheck-> ACCEPTED (release)
//
// The fake runs in two modes:
//   - strict: behaves like the contract (ERR_DUPLICATE on a repeated idemKey, ERR_INVALID_TRANSITION on
//     a repeated or premature transition).
//   - weak:   NO duplicate protection at all; it re-applies any repeated transition. Only the ingest
//     layer (processed_keys + per-key locking) stands between a duplicate and a second release.
//
// It also injects transient 503s so the retry path is exercised during the storm.
package burst

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hansika-hub/anubandh/ingest/pipeline"
)

const (
	deals       = 25
	copies      = 5 // 25 deals x 4 events x 5 copies = 500 messages
	uniqueCount = deals * 4
)

type tranche struct {
	status   string
	released int // payment releases triggered for this tranche; must end at exactly 1
}

type fakeGateway struct {
	strict bool

	mu       sync.Mutex
	status   map[string]string // dealId -> CREATED|FUNDED
	tranches map[string]*tranche
	seen     map[string]bool // idemKey -> applied
	applied  map[string]int  // idemKey -> times a state change was applied
	calls    int
	injected int
	outOfOrd int // ERR_INVALID_TRANSITION replies: proves events really arrived out of order
}

func newGateway(strict bool) *fakeGateway {
	g := &fakeGateway{strict: strict, status: map[string]string{}, tranches: map[string]*tranche{}, seen: map[string]bool{}, applied: map[string]int{}}
	for i := 1; i <= deals; i++ {
		g.status[dealID(i)] = "CREATED" // the buyer created every deal before the storm
	}
	return g
}

func dealID(i int) string { return fmt.Sprintf("DL-20261020-%04d", i) }

func (g *fakeGateway) reply(w http.ResponseWriter, code int, errCode string) {
	if errCode == "ERR_INVALID_TRANSITION" {
		g.outOfOrd++
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if errCode == "" {
		_, _ = w.Write([]byte(`{"txId":"tx"}`))
		return
	}
	_, _ = fmt.Fprintf(w, `{"error":{"code":%q,"message":"fake","details":{}}}`, errCode)
}

func (g *fakeGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Fn      string   `json:"fn"`
		Args    []string `json:"args"`
		IdemKey string   `json:"idemKey"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		g.reply(w, 400, "ERR_BAD_REQUEST")
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls++
	if g.calls%17 == 0 { // transient failure before any state is touched
		g.injected++
		g.reply(w, 503, "ERR_MVCC_CONFLICT")
		return
	}
	deal := req.Args[0]
	idem := req.IdemKey
	if g.strict && idem != "" && req.Fn != "ConfirmLien" && g.seen[idem] {
		g.reply(w, 409, "ERR_DUPLICATE")
		return
	}
	key := func(t string) string { return deal + "/" + t }
	apply := func() {
		g.seen[idem] = true
		g.applied[idem]++
		g.reply(w, 200, "")
	}
	switch req.Fn {
	case "ConfirmLien":
		if g.status[deal] != "CREATED" && g.strict {
			g.reply(w, 409, "ERR_INVALID_TRANSITION")
			return
		}
		g.status[deal] = "FUNDED"
		apply()
	case "VerifyInvoice":
		if g.status[deal] != "FUNDED" {
			g.reply(w, 409, "ERR_INVALID_TRANSITION")
			return
		}
		apply()
	case "PostGoodsReceipt":
		if g.status[deal] != "FUNDED" {
			g.reply(w, 409, "ERR_INVALID_TRANSITION")
			return
		}
		k := key(req.Args[1])
		if _, exists := g.tranches[k]; exists && g.strict {
			g.reply(w, 409, "ERR_INVALID_TRANSITION")
			return
		}
		g.tranches[k] = &tranche{status: "DELIVERED"}
		apply()
	case "RecordQualityCheck":
		t, ok := g.tranches[key(req.Args[1])]
		if !ok || (t.status != "DELIVERED" && g.strict) {
			g.reply(w, 409, "ERR_INVALID_TRANSITION")
			return
		}
		t.status = "ACCEPTED"
		t.released++ // acceptance triggers the payment release exactly once in the real system
		apply()
	default:
		g.reply(w, 403, "ERR_FORBIDDEN_ROLE")
	}
}

func stream(rng *rand.Rand) []pipeline.InboundEvent {
	var evs []pipeline.InboundEvent
	at := time.Date(2026, 10, 20, 7, 54, 0, 0, time.UTC)
	for i := 1; i <= deals; i++ {
		d := dealID(i)
		base := pipeline.InboundEvent{DealID: d, OccurredAt: at}
		lien := base
		lien.EventType, lien.IdemKey = "BANK_LIEN", fmt.Sprintf("bank:LIEN-%d", i)
		lien.Payload = json.RawMessage(`{"lienRef":"LIEN-` + strconv.Itoa(i) + `","amountPaise":100000000}`)
		gr := base
		gr.EventType, gr.IdemKey, gr.TrancheNo = "GOODS_RECEIPT", fmt.Sprintf("warehouse:GR-%d", i), 1
		gr.Payload = json.RawMessage(`{"lines":[{"lineNo":1,"deliveredQty":100}]}`)
		qc := base
		qc.EventType, qc.IdemKey, qc.TrancheNo = "QUALITY_CHECK", fmt.Sprintf("warehouse:QC-%d", i), 1
		qc.Payload = json.RawMessage(`{"results":[{"lineNo":1,"acceptedQty":80,"rejectedQty":20}]}`)
		inv := base
		inv.EventType, inv.IdemKey = "INVOICE", fmt.Sprintf("gst:IRN-%d", i)
		inv.Payload = json.RawMessage(`{"irn":"IRN-` + strconv.Itoa(i) + `"}`)
		for c := 0; c < copies; c++ {
			evs = append(evs, lien, gr, qc, inv)
		}
	}
	rng.Shuffle(len(evs), func(a, b int) { evs[a], evs[b] = evs[b], evs[a] }) // out-of-order AND interleaved duplicates
	return evs
}

func publishAll(t *testing.T, br *pipeline.MemBroker, evs []pipeline.InboundEvent) {
	t.Helper()
	for _, ev := range evs {
		b, _ := json.Marshal(ev)
		if err := br.Publish(context.Background(), pipeline.Message{Topic: pipeline.TopicInbound, Key: ev.DealID, Value: b,
			Headers: map[string]string{pipeline.HdrIdemKey: ev.IdemKey}}); err != nil {
			t.Fatal(err)
		}
	}
}

func runStorm(t *testing.T, strict bool, seed int64) {
	gw := newGateway(strict)
	srv := httptest.NewServer(gw)
	defer srv.Close()

	br, store := pipeline.NewMemBroker(), pipeline.NewMemStore()
	w := pipeline.NewWorker(pipeline.NewHTTPChain(srv.URL, ""), store, br, pipeline.WorkerConfig{
		MaxAttempts: 5, RequeueDelay: 40 * time.Millisecond, BackoffBase: 20 * time.Millisecond, BackoffMax: 80 * time.Millisecond}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); w.Run(ctx, br.Consumer(pipeline.TopicInbound), 8, false) }()
	go func() { defer wg.Done(); w.Run(ctx, br.Consumer(pipeline.TopicRetry), 8, true) }()

	evs := stream(rand.New(rand.NewSource(seed)))
	if len(evs) != 500 {
		t.Fatalf("test setup: %d events, want 500", len(evs))
	}
	publishAll(t, br, evs)

	deadline := time.Now().Add(30 * time.Second)
	for store.Len() < uniqueCount {
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %d/%d unique events processed; dlq=%d retry=%d", store.Len(), uniqueCount, br.Len(pipeline.TopicDLQ), br.Len(pipeline.TopicRetry))
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Second wave: the whole stream is delivered AGAIN after everything succeeded (broker redelivery,
	// upstream retry storm). Nothing may change on chain.
	publishAll(t, br, stream(rand.New(rand.NewSource(seed+1))))
	for br.Pending(pipeline.TopicInbound) > 0 {
		if time.Now().After(deadline) {
			t.Fatal("second wave never drained")
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(150 * time.Millisecond) // let in-flight handlers finish
	cancel()
	wg.Wait()

	gw.mu.Lock()
	defer gw.mu.Unlock()
	if gw.injected == 0 {
		t.Fatal("transient-failure injection never fired; the retry path was not exercised")
	}
	if gw.outOfOrd == 0 {
		t.Fatal("no event arrived out of order; the requeue-with-delay path was not exercised")
	}
	doubleReleases, released := 0, 0
	for k, tr := range gw.tranches {
		released += tr.released
		if tr.released > 1 {
			doubleReleases += tr.released - 1
			t.Errorf("DOUBLE RELEASE on %s: released %d times", k, tr.released)
		}
	}
	if doubleReleases != 0 {
		t.Fatalf("double releases = %d, want 0", doubleReleases)
	}
	if released != deals {
		t.Fatalf("releases = %d, want exactly %d (one per tranche; none lost)", released, deals)
	}
	if len(gw.applied) != uniqueCount {
		t.Fatalf("distinct events applied = %d, want %d", len(gw.applied), uniqueCount)
	}
	for k, n := range gw.applied {
		if n != 1 {
			t.Errorf("event %s applied %d times, want exactly 1", k, n)
		}
	}
	if n := br.Len(pipeline.TopicDLQ); n != 0 {
		msgs, _ := br.ReadAll(context.Background(), pipeline.TopicDLQ)
		t.Fatalf("%d events dead-lettered (lost): first=%s", n, msgs[0].Value)
	}
	if store.Len() != uniqueCount {
		t.Fatalf("processed_keys = %d, want %d", store.Len(), uniqueCount)
	}
	for d, s := range gw.status {
		if s != "FUNDED" {
			t.Errorf("deal %s ended %s, want FUNDED", d, s)
		}
	}
	t.Logf("strict=%v: 500 msgs (+500 redelivered), %d chain calls (%d injected 503s, %d out-of-order bounces), releases=%d, double releases=0, dlq=0",
		strict, gw.calls, gw.injected, gw.outOfOrd, released)
}

// The contract-faithful chain.
func TestBurstStrictChain(t *testing.T) { runStorm(t, true, 1) }

// A chain with no duplicate protection: proves the ingest layer alone prevents double release.
func TestBurstWeakChainIngestAloneIsSufficient(t *testing.T) { runStorm(t, false, 2) }

// Different shuffles, same guarantees.
func TestBurstManySeeds(t *testing.T) {
	for seed := int64(10); seed < 16; seed++ {
		seed := seed
		t.Run(strconv.FormatInt(seed, 10), func(t *testing.T) { runStorm(t, seed%2 == 0, seed) })
	}
}

// Control: without the dedupe layer the weak chain DOES double release. This guards the test itself
// against passing vacuously.
func TestControlWithoutDedupeTheWeakChainDoubleReleases(t *testing.T) {
	gw := newGateway(false)
	srv := httptest.NewServer(gw)
	defer srv.Close()
	chain := pipeline.NewHTTPChain(srv.URL, "")
	var errs int32
	for _, ev := range []string{"lien", "gr", "qc", "qc"} {
		var fn string
		var args []string
		switch ev {
		case "lien":
			fn, args = "ConfirmLien", []string{dealID(1), "L", "1"}
		case "gr":
			fn, args = "PostGoodsReceipt", []string{dealID(1), "1", "[]", "warehouse:GR-1"}
		case "qc":
			fn, args = "RecordQualityCheck", []string{dealID(1), "1", "[]", "warehouse:QC-1"}
		}
		for i := 0; i < 5; i++ { // retry through the injected 503s
			_, err := chain.Submit(context.Background(), fn, args, "k:"+ev)
			if err == nil {
				break
			}
			atomic.AddInt32(&errs, 1)
		}
	}
	gw.mu.Lock()
	defer gw.mu.Unlock()
	if gw.tranches[dealID(1)+"/1"].released != 2 {
		t.Fatalf("control expected 2 releases from a duplicated QC on a weak chain, got %d", gw.tranches[dealID(1)+"/1"].released)
	}
}
