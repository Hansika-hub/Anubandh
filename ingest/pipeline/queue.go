// Package pipeline is the ingest core shared by the API, the worker and the burst test:
// queue/store abstractions (with in-memory implementations), the gateway client, the worker
// and the dead-letter queue. It depends on the standard library only; Kafka and Postgres
// adapters live behind the `adapters` build tag.
package pipeline

import (
	"context"
	"errors"
	"hash/fnv"
	"sync"

	"github.com/Hansika-hub/anubandh/ingest/internal/events"
)

// InboundEvent is the unified envelope (alias so callers outside ingest/ can name it).
type InboundEvent = events.InboundEvent

// Frozen topic names (BUILD.md section 9.3).
const (
	TopicInbound = "anubandh.events.inbound"
	TopicRetry   = "anubandh.events.retry"
	TopicDLQ     = "anubandh.events.dlq"
)

// Message headers.
const (
	HdrIdemKey = "idemKey"
	HdrRetryAt = "retryAt" // RFC 3339; the retry consumer holds the message until then
	HdrAttempt = "attempt" // failed attempts so far
	HdrKind    = "kind"    // DLQ topic only: "dead" (default) or "replayed" marker
	HdrReplay  = "replay"
)

// Message is one record on a topic.
type Message struct {
	Topic     string
	Key       string // dealId
	Value     []byte
	Headers   map[string]string
	Partition int
	Offset    int64
}

// Producer publishes a message and returns once it is durably accepted.
type Producer interface {
	Publish(ctx context.Context, m Message) error
}

// Consumer delivers messages at-least-once: a message not committed is redelivered after a restart.
type Consumer interface {
	Poll(ctx context.Context) (Message, error)
	Commit(ctx context.Context, m Message) error
}

// TopicReader reads a whole topic from the beginning (used for the DLQ view).
type TopicReader interface {
	ReadAll(ctx context.Context, topic string) ([]Message, error)
}

// ErrClosed is returned by a consumer whose broker was closed.
var ErrClosed = errors.New("pipeline: closed")

// MemBroker is an in-memory Kafka stand-in for tests and local dev. Messages on a topic are
// handed to competing consumers in publish order, each message to exactly one consumer
// (a consumer group). This is deliberately harsher than Kafka: concurrent workers may hold two
// events of the same deal at once, which the worker must survive.
type MemBroker struct {
	mu     sync.Mutex
	topics map[string]*memTopic
	closed bool
	cond   *sync.Cond
}

type memTopic struct {
	log  []Message
	next int // next index to hand out to the consumer group
}

func NewMemBroker() *MemBroker {
	b := &MemBroker{topics: map[string]*memTopic{}}
	b.cond = sync.NewCond(&b.mu)
	return b
}

func (b *MemBroker) topic(name string) *memTopic {
	t, ok := b.topics[name]
	if !ok {
		t = &memTopic{}
		b.topics[name] = t
	}
	return t
}

func partitionOf(topic, key string) int {
	n := 12
	if topic == TopicDLQ {
		n = 3
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return int(h.Sum32()) % n
}

// Publish appends to the topic log.
func (b *MemBroker) Publish(_ context.Context, m Message) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return ErrClosed
	}
	t := b.topic(m.Topic)
	m.Partition = partitionOf(m.Topic, m.Key)
	m.Offset = int64(len(t.log))
	cp := m
	cp.Value = append([]byte(nil), m.Value...)
	cp.Headers = copyHeaders(m.Headers)
	t.log = append(t.log, cp)
	b.cond.Broadcast()
	return nil
}

// ReadAll implements TopicReader.
func (b *MemBroker) ReadAll(_ context.Context, topic string) ([]Message, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	t := b.topic(topic)
	out := make([]Message, len(t.log))
	copy(out, t.log)
	return out, nil
}

// Len is the number of messages ever published to topic.
func (b *MemBroker) Len(topic string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.topic(topic).log)
}

// Pending is the number of messages on topic not yet handed to the consumer group.
func (b *MemBroker) Pending(topic string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	t := b.topic(topic)
	return len(t.log) - t.next
}

// Close wakes all pollers with ErrClosed.
func (b *MemBroker) Close() {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	b.cond.Broadcast()
}

// Consumer returns a competing consumer on topic. All consumers created for the same topic share one group.
func (b *MemBroker) Consumer(topic string) Consumer { return &memConsumer{b: b, topic: topic} }

type memConsumer struct {
	b     *MemBroker
	topic string
}

func (c *memConsumer) Poll(ctx context.Context) (Message, error) {
	stop := context.AfterFunc(ctx, func() {
		c.b.mu.Lock()
		c.b.mu.Unlock() //nolint:staticcheck // barrier so Broadcast cannot slip between the check and Wait
		c.b.cond.Broadcast()
	})
	defer stop()
	c.b.mu.Lock()
	defer c.b.mu.Unlock()
	for {
		if err := ctx.Err(); err != nil {
			return Message{}, err
		}
		t := c.b.topic(c.topic)
		if t.next < len(t.log) {
			m := t.log[t.next]
			t.next++
			m.Headers = copyHeaders(m.Headers)
			return m, nil
		}
		if c.b.closed {
			return Message{}, ErrClosed
		}
		c.b.cond.Wait()
	}
}

// Commit is a no-op in memory: a message handed out is considered consumed.
func (c *memConsumer) Commit(context.Context, Message) error { return nil }

func copyHeaders(h map[string]string) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		out[k] = v
	}
	return out
}
