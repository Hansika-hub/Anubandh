package main

import (
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// rateLimiter is a token bucket per source: `rate` tokens/second, up to `burst` stored.
// BUILD.md: 50 rps per source, burst 100 -> 429 ERR_RATE_LIMITED with Retry-After.
type rateLimiter struct {
	rate  float64
	burst float64
	now   func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newRateLimiter(rate float64, burst int, now func() time.Time) *rateLimiter {
	if now == nil {
		now = time.Now
	}
	return &rateLimiter{rate: rate, burst: float64(burst), now: now, buckets: map[string]*bucket{}}
}

// allow takes one token for key. When it refuses, wait is how long until a token is available.
func (l *rateLimiter) allow(key string) (ok bool, wait time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	t := l.now()
	b := l.buckets[key]
	if b == nil {
		b = &bucket{tokens: l.burst, last: t}
		l.buckets[key] = b
	}
	if el := t.Sub(b.last).Seconds(); el > 0 {
		b.tokens = math.Min(l.burst, b.tokens+el*l.rate)
		b.last = t
	}
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) / l.rate * float64(time.Second))
}

// RateLimit must run AFTER signature verification so unauthenticated traffic cannot drain a
// legitimate source's bucket.
func (l *rateLimiter) RateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		src, _ := r.Context().Value(ctxSource).(string)
		if ok, wait := l.allow(src); !ok {
			secs := int(math.Ceil(wait.Seconds()))
			if secs < 1 {
				secs = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(secs))
			writeErr(w, http.StatusTooManyRequests, "ERR_RATE_LIMITED", "rate limit exceeded for source "+src, map[string]any{"retryAfterSeconds": secs})
			return
		}
		next.ServeHTTP(w, r)
	})
}
