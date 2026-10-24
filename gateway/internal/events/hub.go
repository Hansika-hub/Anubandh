// Package events fans projected chain events out to Server-Sent Events subscribers.
package events

import "sync"

// Message is one SSE message: id = deal_events.id, event = chain event name.
type Message struct {
	ID     int64
	Event  string
	DealID string
	Data   []byte
}

// Hub is a non-blocking broadcaster. A slow subscriber misses messages instead of stalling the
// listener; it can catch up with Last-Event-ID replay from deal_events.
type Hub struct {
	mu   sync.RWMutex
	subs map[chan Message]string
}

// NewHub returns an empty hub.
func NewHub() *Hub { return &Hub{subs: map[chan Message]string{}} }

// Subscribe registers a subscriber, optionally filtered to one deal. Call cancel when done.
func (h *Hub) Subscribe(dealID string) (<-chan Message, func()) {
	ch := make(chan Message, 256)
	h.mu.Lock()
	h.subs[ch] = dealID
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		if _, ok := h.subs[ch]; ok {
			delete(h.subs, ch)
			close(ch)
		}
		h.mu.Unlock()
	}
}

// Publish delivers m to every matching subscriber without blocking.
func (h *Hub) Publish(m Message) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch, filter := range h.subs {
		if filter != "" && filter != m.DealID {
			continue
		}
		select {
		case ch <- m:
		default:
		}
	}
}

// Subscribers returns the number of open subscriptions.
func (h *Hub) Subscribers() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs)
}
