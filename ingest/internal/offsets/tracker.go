// Package offsets computes safe Kafka commit points when messages finish out of order.
//
// Several workers process one partition concurrently, so message 12 can finish before message 10.
// Committing 13 at that moment would lose 10 if the process died. The Tracker only advances the
// commit point over a contiguous run of finished offsets (the low-water mark).
package offsets

import "sync"

type partKey struct {
	topic string
	part  int32
}

type part struct {
	next int64          // lowest offset not yet finished (everything below is committable)
	done map[int64]bool // finished offsets >= next
}

// Tracker is safe for concurrent use.
type Tracker struct {
	mu    sync.Mutex
	parts map[partKey]*part
}

func NewTracker() *Tracker { return &Tracker{parts: map[partKey]*part{}} }

// Seen must be called for every message when it is polled. The first offset seen on a partition
// defines where tracking starts.
func (t *Tracker) Seen(topic string, partition int32, offset int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	k := partKey{topic, partition}
	if _, ok := t.parts[k]; !ok {
		t.parts[k] = &part{next: offset, done: map[int64]bool{}}
	}
}

// Done marks offset finished. If the commit point moved it returns the finished range
// [from, to] (inclusive); the Kafka commit offset is to+1. Offsets below the tracked start, or
// already committed (redelivery after a rebalance), are ignored.
func (t *Tracker) Done(topic string, partition int32, offset int64) (from, to int64, advanced bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	p, ok := t.parts[partKey{topic, partition}]
	if !ok || offset < p.next {
		return 0, 0, false
	}
	p.done[offset] = true
	if !p.done[p.next] {
		return 0, 0, false
	}
	from = p.next
	for p.done[p.next] {
		delete(p.done, p.next)
		p.next++
	}
	return from, p.next - 1, true
}
