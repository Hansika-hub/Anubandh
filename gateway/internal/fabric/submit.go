package fabric

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"sync/atomic"
	"time"

	"github.com/Hansika-hub/anubandh/gateway/internal/apperr"
)

// RetryPolicy bounds SubmitWithRetry. BUILD.md §8.1: MVCC retry, 5x, exponential backoff + jitter.
type RetryPolicy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

// DefaultRetryPolicy is 5 attempts, 100ms doubling, capped at 2s.
var DefaultRetryPolicy = RetryPolicy{MaxAttempts: 5, BaseDelay: 100 * time.Millisecond, MaxDelay: 2 * time.Second}

// Submitter wraps a Client with retry, identity resolution and logging.
type Submitter struct {
	client    Client
	policy    RetryPolicy
	defaultID *Identity
	log       *slog.Logger
	sleep     func(ctx context.Context, d time.Duration) error
}

// NewSubmitter builds a Submitter. defaultID (may be nil) signs calls whose context carries no
// identity; nil means such calls fail with ERR_UNAUTHORIZED.
func NewSubmitter(c Client, policy RetryPolicy, defaultID *Identity, log *slog.Logger) *Submitter {
	if policy.MaxAttempts < 1 {
		policy = DefaultRetryPolicy
	}
	if log == nil {
		log = slog.Default()
	}
	return &Submitter{client: c, policy: policy, defaultID: defaultID, log: log, sleep: sleepCtx}
}

// Client returns the underlying chain client.
func (s *Submitter) Client() Client { return s.client }

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (s *Submitter) identity(ctx context.Context) (Identity, error) {
	if id, ok := IdentityFrom(ctx); ok {
		return id, nil
	}
	if s.defaultID != nil {
		return *s.defaultID, nil
	}
	return Identity{}, apperr.New(apperr.ErrUnauthorized, "no chain identity for this call")
}

// backoff returns the delay before retry number attempt (1-based): exponential, capped,
// with "equal jitter" so concurrent conflicting writers spread out.
func (s *Submitter) backoff(attempt int) time.Duration {
	d := s.policy.BaseDelay << (attempt - 1)
	if d > s.policy.MaxDelay || d <= 0 {
		d = s.policy.MaxDelay
	}
	half := d / 2
	return half + time.Duration(rand.Int64N(int64(half)+1)) //nolint:gosec // retry jitter, not a security value
}

// Submit runs req, retrying only failures that are known not to have committed.
func (s *Submitter) Submit(ctx context.Context, req SubmitRequest) (SubmitResult, error) {
	id, err := s.identity(ctx)
	if err != nil {
		return SubmitResult{}, err
	}
	var last error
	for attempt := 1; attempt <= s.policy.MaxAttempts; attempt++ {
		res, err := s.client.SubmitOnce(ctx, id, req)
		if err == nil {
			res.Attempts = attempt
			s.log.Debug("chain submit ok", "fn", req.Fn, "txId", res.TxID, "attempt", attempt, "user", id.UserID)
			return res, nil
		}
		last = err
		ae, ok := apperr.As(err)
		if !ok || !ae.Retryable || attempt == s.policy.MaxAttempts {
			break
		}
		delay := s.backoff(attempt)
		s.log.Warn("chain submit retry", "fn", req.Fn, "attempt", attempt, "code", ae.Code, "delayMs", delay.Milliseconds(), "txId", ae.TxID)
		if err := s.sleep(ctx, delay); err != nil {
			return SubmitResult{}, apperr.Wrap(apperr.ErrInternal, err, "submit cancelled while retrying")
		}
	}
	if ae, ok := apperr.As(last); ok {
		return SubmitResult{TxID: ae.TxID}, ae
	}
	return SubmitResult{}, apperr.Wrap(apperr.ErrInternal, last, "chain submit failed")
}

// Evaluate runs a read-only query with the context's identity.
func (s *Submitter) Evaluate(ctx context.Context, fn string, args ...string) ([]byte, error) {
	id, err := s.identity(ctx)
	if err != nil {
		return nil, err
	}
	return s.client.Evaluate(ctx, id, fn, args...)
}

var defaultSubmitter atomic.Pointer[Submitter]

// SetDefault installs the process-wide Submitter used by SubmitWithRetry and Evaluate.
func SetDefault(s *Submitter) { defaultSubmitter.Store(s) }

// Default returns the process-wide Submitter (nil before SetDefault).
func Default() *Submitter { return defaultSubmitter.Load() }

func mustDefault() (*Submitter, error) {
	s := defaultSubmitter.Load()
	if s == nil {
		return nil, apperr.New(apperr.ErrInternal, "fabric: SetDefault was not called")
	}
	return s, nil
}

// SubmitWithRetry submits fn(args...) signed by the identity in ctx (fabric.WithIdentity),
// retrying MVCC conflicts up to 5 times with exponential backoff and jitter. BUILD.md §8.2.
func SubmitWithRetry(ctx context.Context, fn string, args ...string) ([]byte, error) {
	s, err := mustDefault()
	if err != nil {
		return nil, err
	}
	res, err := s.Submit(ctx, SubmitRequest{Fn: fn, Args: args})
	return res.Payload, err
}

// Evaluate runs a read-only chaincode query signed by the identity in ctx. BUILD.md §8.2.
func Evaluate(ctx context.Context, fn string, args ...string) ([]byte, error) {
	s, err := mustDefault()
	if err != nil {
		return nil, err
	}
	return s.Evaluate(ctx, fn, args...)
}
