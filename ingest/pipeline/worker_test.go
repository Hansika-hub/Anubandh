package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeChain struct {
	mu    sync.Mutex
	calls []string
	fn    func(n int, fn string, args []string, idem string) error
}

func (f *fakeChain) Submit(_ context.Context, fn string, args []string, idem string) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, idem)
	n := len(f.calls)
	f.mu.Unlock()
	if f.fn != nil {
		if err := f.fn(n, fn, args, idem); err != nil {
			return "", err
		}
	}
	return "tx", nil
}

func (f *fakeChain) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }

func sampleEvent(idem string) InboundEvent {
	return InboundEvent{EventType: "GOODS_RECEIPT", IdemKey: idem, DealID: "DL-20261020-0001", TrancheNo: 1,
		Payload: json.RawMessage(`{"lines":[{"lineNo":1,"deliveredQty":100}]}`), OccurredAt: time.Date(2026, 10, 20, 7, 54, 0, 0, time.UTC)}
}

func msgOf(t *testing.T, ev InboundEvent, attempt int) Message {
	t.Helper()
	b, _ := json.Marshal(ev)
	h := map[string]string{HdrIdemKey: ev.IdemKey}
	if attempt > 0 {
		h[HdrAttempt] = strconv.Itoa(attempt)
	}
	return Message{Topic: TopicInbound, Key: ev.DealID, Value: b, Headers: h}
}

func newW(c Chain) (*Worker, *MemStore, *MemBroker) {
	st, br := NewMemStore(), NewMemBroker()
	w := NewWorker(c, st, br, WorkerConfig{MaxAttempts: 5, RequeueDelay: 10 * time.Millisecond, BackoffBase: time.Millisecond, BackoffMax: 4 * time.Millisecond}, nil)
	return w, st, br
}

func TestAppliedOnceThenDuplicateDropped(t *testing.T) {
	fc := &fakeChain{}
	w, st, _ := newW(fc)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if out, err := w.HandleMessage(ctx, msgOf(t, sampleEvent("warehouse:GR-1"), 0)); err != nil || out != OutcomeApplied {
			t.Fatalf("iteration %d: %v %v", i, out, err)
		}
	}
	if fc.count() != 1 {
		t.Fatalf("chain submits = %d, want exactly 1", fc.count())
	}
	if st.Len() != 1 {
		t.Fatalf("processed keys = %d", st.Len())
	}
}

func TestChainDuplicateMarksProcessed(t *testing.T) {
	fc := &fakeChain{fn: func(int, string, []string, string) error { return &ChainError{Status: 409, Code: CodeDuplicate} }}
	w, st, br := newW(fc)
	out, err := w.HandleMessage(context.Background(), msgOf(t, sampleEvent("warehouse:GR-2"), 0))
	if err != nil || out != OutcomeApplied {
		t.Fatalf("%v %v", out, err)
	}
	if ok, _ := st.AlreadyProcessed(context.Background(), "warehouse:GR-2"); !ok {
		t.Fatal("ERR_DUPLICATE must mark the key processed")
	}
	if br.Len(TopicRetry)+br.Len(TopicDLQ) != 0 {
		t.Fatal("duplicate must not requeue or dead-letter")
	}
}

func TestInvalidTransitionRequeuesWithDelayAndCountsAttempt(t *testing.T) {
	fc := &fakeChain{fn: func(int, string, []string, string) error {
		return &ChainError{Status: 409, Code: CodeInvalidTransition}
	}}
	w, st, br := newW(fc)
	out, err := w.HandleMessage(context.Background(), msgOf(t, sampleEvent("warehouse:GR-3"), 0))
	if err != nil || out != OutcomeRequeued {
		t.Fatalf("%v %v", out, err)
	}
	if ok, _ := st.AlreadyProcessed(context.Background(), "warehouse:GR-3"); ok {
		t.Fatal("out-of-order event must NOT be marked processed")
	}
	msgs, _ := br.ReadAll(context.Background(), TopicRetry)
	if len(msgs) != 1 || msgs[0].Headers[HdrAttempt] != "1" {
		t.Fatalf("retry msgs: %+v", msgs)
	}
	at, err := time.Parse(time.RFC3339Nano, msgs[0].Headers[HdrRetryAt])
	if err != nil || !at.After(time.Now().Add(-time.Second)) {
		t.Fatalf("bad retryAt %q: %v", msgs[0].Headers[HdrRetryAt], err)
	}
}

func TestTransientErrorRetriesWithBackoffThenDLQAfterFive(t *testing.T) {
	fc := &fakeChain{fn: func(int, string, []string, string) error { return &ChainError{Status: 503, Code: "ERR_MVCC_CONFLICT"} }}
	w, _, br := newW(fc)
	ctx := context.Background()
	var last Outcome
	for attempt := 0; attempt < 5; attempt++ {
		var err error
		last, err = w.HandleMessage(ctx, msgOf(t, sampleEvent("warehouse:GR-4"), attempt))
		if err != nil {
			t.Fatal(err)
		}
		if attempt < 4 && last != OutcomeRetried {
			t.Fatalf("attempt %d outcome %s", attempt, last)
		}
	}
	if last != OutcomeDead {
		t.Fatalf("5th failure outcome = %s, want dead", last)
	}
	if br.Len(TopicRetry) != 4 || br.Len(TopicDLQ) != 1 {
		t.Fatalf("retry=%d dlq=%d, want 4 and 1", br.Len(TopicRetry), br.Len(TopicDLQ))
	}
	dl, _ := br.ReadAll(ctx, TopicDLQ)
	var rec DLQRecord
	_ = json.Unmarshal(dl[0].Value, &rec)
	if rec.Attempts != 5 || rec.Event == nil || rec.Event.IdemKey != "warehouse:GR-4" {
		t.Fatalf("dlq record %+v", rec)
	}
}

func TestMalformedEventGoesStraightToDLQ(t *testing.T) {
	fc := &fakeChain{}
	w, _, br := newW(fc)
	ev := sampleEvent("warehouse:GR-5")
	ev.Payload = json.RawMessage(`{"lines":[]}`)
	if out, err := w.HandleMessage(context.Background(), msgOf(t, ev, 0)); err != nil || out != OutcomeDead {
		t.Fatalf("%v %v", out, err)
	}
	if fc.count() != 0 || br.Len(TopicDLQ) != 1 {
		t.Fatal("malformed event must not reach the chain and must be dead-lettered")
	}
	// undecodable bytes
	if out, err := w.HandleMessage(context.Background(), Message{Key: "DL-20261020-0001", Value: []byte("{not json"), Headers: map[string]string{}}); err != nil || out != OutcomeDead {
		t.Fatalf("%v %v", out, err)
	}
}

func TestQueueFailureIsReportedNotSwallowed(t *testing.T) {
	fc := &fakeChain{fn: func(int, string, []string, string) error { return &ChainError{Status: 503} }}
	w, _, br := newW(fc)
	br.Close() // publishes now fail
	if _, err := w.HandleMessage(context.Background(), msgOf(t, sampleEvent("warehouse:GR-6"), 0)); err == nil {
		t.Fatal("a failed requeue must surface so the message is not committed")
	}
}

func TestConcurrentDuplicatesSubmitOnce(t *testing.T) {
	var inFlight, maxInFlight int32
	fc := &fakeChain{fn: func(int, string, []string, string) error {
		n := atomic.AddInt32(&inFlight, 1)
		for {
			m := atomic.LoadInt32(&maxInFlight)
			if n <= m || atomic.CompareAndSwapInt32(&maxInFlight, m, n) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
		return nil
	}}
	w, _, _ := newW(fc)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := w.ProcessEvent(context.Background(), sampleEvent("warehouse:GR-7")); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if fc.count() != 1 || maxInFlight != 1 {
		t.Fatalf("submits=%d maxInFlight=%d, want 1 and 1", fc.count(), maxInFlight)
	}
}

func TestRetryTopicHonoursRetryAt(t *testing.T) {
	fc := &fakeChain{}
	w, _, br := newW(fc)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m := msgOf(t, sampleEvent("warehouse:GR-8"), 1)
	m.Topic = TopicRetry
	m.Headers[HdrRetryAt] = time.Now().Add(150 * time.Millisecond).UTC().Format(time.RFC3339Nano)
	start := time.Now()
	_ = br.Publish(ctx, m)
	done := make(chan struct{})
	go func() { w.Run(ctx, br.Consumer(TopicRetry), 1, true); close(done) }()
	for fc.count() == 0 {
		if time.Since(start) > 3*time.Second {
			t.Fatal("never processed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if el := time.Since(start); el < 140*time.Millisecond {
		t.Fatalf("processed after %v, before retryAt", el)
	}
	cancel()
	<-done
}

func TestDLQListAndReplay(t *testing.T) {
	w, _, br := newW(&fakeChain{})
	ctx := context.Background()
	ev := sampleEvent("warehouse:GR-9")
	if err := w.SendToDLQ(WithAttempt(ctx, 4), ev, "boom"); err != nil {
		t.Fatal(err)
	}
	d := &DLQ{Reader: br, Out: br, Now: time.Now}
	es, err := d.List(ctx, 10, false)
	if err != nil || len(es) != 1 || es[0].Reason != "boom" || es[0].Attempts != 5 {
		t.Fatalf("%+v %v", es, err)
	}
	if err := d.Replay(ctx, "nope"); !errors.Is(err, ErrDLQNotFound) {
		t.Fatalf("want not found, got %v", err)
	}
	if err := d.Replay(ctx, es[0].ID); err != nil {
		t.Fatal(err)
	}
	if br.Len(TopicInbound) != 1 {
		t.Fatal("replay must re-enqueue on inbound")
	}
	if err := d.Replay(ctx, es[0].ID); !errors.Is(err, ErrDLQReplayed) {
		t.Fatalf("second replay: %v", err)
	}
	if es, _ = d.List(ctx, 10, false); len(es) != 0 {
		t.Fatal("replayed entries are hidden by default")
	}
	if es, _ = d.List(ctx, 10, true); len(es) != 1 || !es[0].Replayed {
		t.Fatalf("%+v", es)
	}
}

func TestHTTPChainErrorMapping(t *testing.T) {
	var gotTok string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTok = r.Header.Get("X-Internal-Token")
		var b struct {
			Fn      string   `json:"fn"`
			Args    []string `json:"args"`
			IdemKey string   `json:"idemKey"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		switch b.IdemKey {
		case "a:1":
			_, _ = w.Write([]byte(`{"txId":"T1"}`))
		case "a:2":
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`{"error":{"code":"ERR_INVALID_TRANSITION","message":"x","details":{}}}`))
		default:
			w.WriteHeader(502)
			_, _ = w.Write([]byte(`<html>bad gateway</html>`))
		}
	}))
	defer srv.Close()
	c := NewHTTPChain(srv.URL+"/", "tok")
	if id, err := c.Submit(context.Background(), "PostGoodsReceipt", []string{"x"}, "a:1"); err != nil || id != "T1" || gotTok != "tok" {
		t.Fatalf("%q %v tok=%q", id, err, gotTok)
	}
	_, err := c.Submit(context.Background(), "f", nil, "a:2")
	var ce *ChainError
	if !errors.As(err, &ce) || ce.Code != CodeInvalidTransition || ce.Status != 409 {
		t.Fatalf("%v", err)
	}
	_, err = c.Submit(context.Background(), "f", nil, "a:3")
	if !errors.As(err, &ce) || ce.Code != "" || ce.Status != 502 {
		t.Fatalf("%v", err)
	}
	srv.Close()
	_, err = c.Submit(context.Background(), "f", nil, "a:1")
	if !errors.As(err, &ce) || ce.Status != 0 {
		t.Fatalf("transport error should be a retryable ChainError, got %v", err)
	}
}
