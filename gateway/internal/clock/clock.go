// Package clock provides the gateway's notion of "now".
//
// The chaincode only ever uses the transaction timestamp. The gateway needs a clock for read-model
// figures such as MSE days-left; in mock mode that clock can be advanced so the demo can
// fast-forward 15 days to show deemed acceptance.
package clock

import (
	"sync"
	"time"
)

// Clock returns the current time.
type Clock interface {
	Now() time.Time
}

// System is the wall clock in UTC.
type System struct{}

// Now returns the current UTC time.
func (System) Now() time.Time { return time.Now().UTC() }

// Offset is a clock shifted forward by an adjustable offset.
type Offset struct {
	mu     sync.RWMutex
	base   Clock
	offset time.Duration
}

// NewOffset wraps base. A nil base means the system clock.
func NewOffset(base Clock) *Offset {
	if base == nil {
		base = System{}
	}
	return &Offset{base: base}
}

// Now returns base time plus the current offset.
func (o *Offset) Now() time.Time {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.base.Now().Add(o.offset).UTC()
}

// Advance moves the clock forward by d (d must be positive; time never goes backwards).
func (o *Offset) Advance(d time.Duration) time.Time {
	o.mu.Lock()
	if d > 0 {
		o.offset += d
	}
	o.mu.Unlock()
	return o.Now()
}

// Offset returns the accumulated offset.
func (o *Offset) Offset() time.Duration {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.offset
}

// SetOffset restores a previously persisted offset (never negative).
func (o *Offset) SetOffset(d time.Duration) {
	if d < 0 {
		return
	}
	o.mu.Lock()
	o.offset = d
	o.mu.Unlock()
}
