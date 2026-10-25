package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/Hansika-hub/anubandh/ingest/internal/events"
)

// WorkerConfig tunes retry behaviour. Zero values get the defaults below.
type WorkerConfig struct {
	MaxAttempts  int           // failures before the DLQ (BUILD.md: 5)
	RequeueDelay time.Duration // delay for out-of-order (ERR_INVALID_TRANSITION) events
	BackoffBase  time.Duration // first retry delay for other errors; doubles per attempt
	BackoffMax   time.Duration
}

func (c WorkerConfig) withDefaults() WorkerConfig {
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 5
	}
	if c.RequeueDelay <= 0 {
		c.RequeueDelay = 2 * time.Second
	}
	if c.BackoffBase <= 0 {
		c.BackoffBase = time.Second
	}
	if c.BackoffMax <= 0 {
		c.BackoffMax = 30 * time.Second
	}
	return c
}

// Worker turns queued events into chain submissions exactly once per idemKey:
//
//	dedupe (processed_keys) -> gateway submit -> record processed
//
// with requeue-with-delay for out-of-order events, backoff retry for transient failures and a
// dead-letter queue after MaxAttempts failures. The method names ProcessEvent, AlreadyProcessed,
// MarkProcessed, SubmitToChain, RequeueWithDelay and SendToDLQ are the frozen names of BUILD.md 9.4.
type Worker struct {
	Chain Chain
	Store ProcessedStore
	Out   Producer
	Cfg   WorkerConfig
	Log   *slog.Logger
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error

	locks keyedMutex
}

// NewWorker builds a Worker with real clock/sleep.
func NewWorker(chain Chain, store ProcessedStore, out Producer, cfg WorkerConfig, log *slog.Logger) *Worker {
	if log == nil {
		log = slog.New(slog.NewJSONHandler(discard{}, nil))
	}
	return &Worker{Chain: chain, Store: store, Out: out, Cfg: cfg.withDefaults(), Log: log, Now: time.Now, Sleep: sleepCtx}
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

type attemptKey struct{}

// WithAttempt records how many times an event has already failed; RequeueWithDelay and SendToDLQ
// read it so their frozen signatures need no attempt parameter.
func WithAttempt(ctx context.Context, n int) context.Context {
	return context.WithValue(ctx, attemptKey{}, n)
}

func attemptOf(ctx context.Context) int {
	n, _ := ctx.Value(attemptKey{}).(int)
	return n
}

// keyedMutex serialises work per idemKey inside one process so two goroutines can never be inside
// "check -> submit -> mark" for the same key at once.
type keyedMutex struct {
	mu sync.Mutex
	m  map[string]*keyLock
}

type keyLock struct {
	sync.Mutex
	refs int
}

func (k *keyedMutex) lock(key string) func() {
	k.mu.Lock()
	if k.m == nil {
		k.m = map[string]*keyLock{}
	}
	l := k.m[key]
	if l == nil {
		l = &keyLock{}
		k.m[key] = l
	}
	l.refs++
	k.mu.Unlock()
	l.Lock()
	return func() {
		l.Unlock()
		k.mu.Lock()
		if l.refs--; l.refs == 0 {
			delete(k.m, key)
		}
		k.mu.Unlock()
	}
}

// AlreadyProcessed reports whether idemKey has been submitted (or reported ERR_DUPLICATE) before.
func (w *Worker) AlreadyProcessed(ctx context.Context, idemKey string) (bool, error) {
	return w.Store.AlreadyProcessed(ctx, idemKey)
}

// MarkProcessed records idemKey as done. Idempotent.
func (w *Worker) MarkProcessed(ctx context.Context, idemKey string) error {
	return w.Store.MarkProcessed(ctx, idemKey)
}

// SubmitToChain maps ev to a gateway call and submits it. A *events.ValidationError means the
// event itself is malformed and can never succeed.
func (w *Worker) SubmitToChain(ctx context.Context, ev InboundEvent) error {
	fn, args, err := ev.ChainCall()
	if err != nil {
		return err
	}
	_, err = w.Chain.Submit(ctx, fn, args, ev.IdemKey)
	return err
}

// ProcessEvent dedupes, submits and records one event. nil means "done, never process again":
// either submitted now, seen before, or the chain reported ERR_DUPLICATE. Any other error is
// returned for the caller to requeue, retry or dead-letter.
func (w *Worker) ProcessEvent(ctx context.Context, ev InboundEvent) error {
	unlock := w.locks.lock(ev.IdemKey)
	defer unlock()

	done, err := w.AlreadyProcessed(ctx, ev.IdemKey)
	if err != nil {
		return fmt.Errorf("processed_keys lookup: %w", err)
	}
	if done {
		w.logEv(ev).Info("duplicate dropped: already processed")
		return nil
	}
	err = w.SubmitToChain(ctx, ev)
	var ce *ChainError
	switch {
	case err == nil:
	case errors.As(err, &ce) && ce.Code == CodeDuplicate:
		w.logEv(ev).Info("chain reported duplicate; marking processed")
	default:
		return err
	}
	if err := w.MarkProcessed(ctx, ev.IdemKey); err != nil {
		return fmt.Errorf("mark processed: %w", err)
	}
	w.logEv(ev).Info("event applied")
	return nil
}

// RequeueWithDelay parks ev on the retry topic until now+d, counting one more failure.
func (w *Worker) RequeueWithDelay(ctx context.Context, ev InboundEvent, d time.Duration) error {
	val, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	return w.Out.Publish(ctx, Message{Topic: TopicRetry, Key: ev.DealID, Value: val, Headers: map[string]string{
		HdrIdemKey: ev.IdemKey,
		HdrRetryAt: w.Now().Add(d).UTC().Format(time.RFC3339Nano),
		HdrAttempt: strconv.Itoa(attemptOf(ctx) + 1),
	}})
}

// SendToDLQ dead-letters ev with a human-readable reason.
func (w *Worker) SendToDLQ(ctx context.Context, ev InboundEvent, reason string) error {
	rec := DLQRecord{Kind: KindDead, Event: &ev, Reason: reason, Attempts: attemptOf(ctx) + 1, FailedAt: w.Now().UTC()}
	val, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	w.logEv(ev).Warn("event dead-lettered", "reason", reason, "attempts", rec.Attempts)
	return w.Out.Publish(ctx, Message{Topic: TopicDLQ, Key: ev.DealID, Value: val, Headers: map[string]string{HdrIdemKey: ev.IdemKey, HdrKind: KindDead}})
}

// Outcome is what HandleMessage did with a message.
type Outcome string

const (
	OutcomeApplied  Outcome = "applied"
	OutcomeRequeued Outcome = "requeued"
	OutcomeRetried  Outcome = "retried"
	OutcomeDead     Outcome = "dead"
)

func backoff(cfg WorkerConfig, attempt int) time.Duration {
	d := cfg.BackoffBase
	for i := 0; i < attempt && d < cfg.BackoffMax; i++ {
		d *= 2
	}
	if d > cfg.BackoffMax {
		d = cfg.BackoffMax
	}
	return d
}

// HandleMessage runs one message through ProcessEvent and routes the result. A non-nil error means
// the routing itself failed (queue unavailable): do not commit, try again.
func (w *Worker) HandleMessage(ctx context.Context, m Message) (Outcome, error) {
	attempt, _ := strconv.Atoi(m.Headers[HdrAttempt])
	ctx = WithAttempt(ctx, attempt)

	var ev InboundEvent
	if err := json.Unmarshal(m.Value, &ev); err != nil {
		// Poison pill: cannot even be decoded, retrying cannot help.
		ev = InboundEvent{DealID: m.Key, IdemKey: m.Headers[HdrIdemKey]}
		return OutcomeDead, w.SendToDLQ(ctx, ev, "undecodable message: "+err.Error())
	}
	err := w.ProcessEvent(ctx, ev)
	if err == nil {
		return OutcomeApplied, nil
	}
	var ve *events.ValidationError
	if errors.As(err, &ve) {
		return OutcomeDead, w.SendToDLQ(ctx, ev, "invalid event: "+ve.Msg)
	}
	if attempt+1 >= w.Cfg.MaxAttempts {
		return OutcomeDead, w.SendToDLQ(ctx, ev, fmt.Sprintf("failed %d times, last error: %v", attempt+1, err))
	}
	var ce *ChainError
	if errors.As(err, &ce) && ce.Code == CodeInvalidTransition {
		// Out of order: the event this one depends on has not landed yet. Park it briefly.
		w.logEv(ev).Info("out-of-order event requeued", "attempt", attempt+1, "delay", w.Cfg.RequeueDelay.String())
		return OutcomeRequeued, w.RequeueWithDelay(ctx, ev, w.Cfg.RequeueDelay)
	}
	d := backoff(w.Cfg, attempt)
	w.logEv(ev).Warn("transient failure; retrying", "attempt", attempt+1, "delay", d.String(), "err", err.Error())
	return OutcomeRetried, w.RequeueWithDelay(ctx, ev, d)
}

// Run starts n goroutines that poll c until ctx ends. Messages on the retry topic carry a
// retryAt header; waitRetry makes the worker honour it.
func (w *Worker) Run(ctx context.Context, c Consumer, n int, waitRetry bool) {
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.loop(ctx, c, waitRetry)
		}()
	}
	wg.Wait()
}

func (w *Worker) loop(ctx context.Context, c Consumer, waitRetry bool) {
	for {
		m, err := c.Poll(ctx)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, ErrClosed) {
				return
			}
			w.Log.Error("poll failed", "msg", "poll failed", "err", err.Error())
			if w.Sleep(ctx, time.Second) != nil {
				return
			}
			continue
		}
		if waitRetry {
			if at, perr := time.Parse(time.RFC3339Nano, m.Headers[HdrRetryAt]); perr == nil {
				if d := at.Sub(w.Now()); d > 0 {
					if w.Sleep(ctx, d) != nil {
						return // shutting down; message stays uncommitted and is redelivered
					}
				}
			}
		}
		for {
			if _, err := w.HandleMessage(ctx, m); err == nil {
				break
			} else {
				w.Log.Error("routing failed; will retry", "msg", "routing failed", "err", err.Error())
				if w.Sleep(ctx, time.Second) != nil {
					return
				}
			}
		}
		if err := c.Commit(ctx, m); err != nil {
			w.Log.Error("commit failed", "msg", "commit failed", "err", err.Error())
		}
	}
}

func (w *Worker) logEv(ev InboundEvent) *slog.Logger {
	return w.Log.With("dealId", ev.DealID, "trancheNo", ev.TrancheNo, "idemKey", ev.IdemKey)
}
