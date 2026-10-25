//go:build adapters

// Package kafkaq implements pipeline.Producer/Consumer/TopicReader on franz-go.
// It compiles only with `-tags adapters` (needs github.com/twmb/franz-go, see ingest/Dockerfile).
package kafkaq

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/Hansika-hub/anubandh/ingest/internal/offsets"
	"github.com/Hansika-hub/anubandh/ingest/pipeline"
)

// Brokers splits KAFKA_BROKERS ("host:9092,host2:9092").
func Brokers(s string) []string {
	var out []string
	for _, b := range strings.Split(s, ",") {
		if b = strings.TrimSpace(b); b != "" {
			out = append(out, b)
		}
	}
	return out
}

func toHeaders(h map[string]string) []kgo.RecordHeader {
	out := make([]kgo.RecordHeader, 0, len(h))
	for k, v := range h {
		out = append(out, kgo.RecordHeader{Key: k, Value: []byte(v)})
	}
	return out
}

func fromRecord(r *kgo.Record) pipeline.Message {
	h := make(map[string]string, len(r.Headers))
	for _, x := range r.Headers {
		h[x.Key] = string(x.Value)
	}
	return pipeline.Message{Topic: r.Topic, Key: string(r.Key), Value: r.Value, Headers: h, Partition: int(r.Partition), Offset: r.Offset}
}

// Producer publishes with acks=all and the client's idempotent-producer default.
type Producer struct{ cl *kgo.Client }

func NewProducer(brokers []string) (*Producer, error) {
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.RequiredAcks(kgo.AllISRAcks()), kgo.ProduceRequestTimeout(10*time.Second))
	if err != nil {
		return nil, err
	}
	return &Producer{cl: cl}, nil
}

func (p *Producer) Publish(ctx context.Context, m pipeline.Message) error {
	rec := &kgo.Record{Topic: m.Topic, Key: []byte(m.Key), Value: m.Value, Headers: toHeaders(m.Headers)}
	return p.cl.ProduceSync(ctx, rec).FirstErr()
}

func (p *Producer) Close() { p.cl.Close() }

// Reader reads a whole topic from the start up to the end offsets present when the call starts.
type Reader struct{ brokers []string }

func NewReader(brokers []string) *Reader { return &Reader{brokers: brokers} }

func (r *Reader) ReadAll(ctx context.Context, topic string) ([]pipeline.Message, error) {
	cl, err := kgo.NewClient(kgo.SeedBrokers(r.brokers...), kgo.ConsumeTopics(topic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	if err != nil {
		return nil, err
	}
	defer cl.Close()
	ends, err := kadm.NewClient(cl).ListEndOffsets(ctx, topic)
	if err != nil {
		return nil, err
	}
	remaining := map[int32]int64{} // partition -> end offset still to reach
	ends.Each(func(o kadm.ListedOffset) {
		if o.Err == nil && o.Offset > 0 {
			remaining[o.Partition] = o.Offset
		}
	})
	var out []pipeline.Message
	for len(remaining) > 0 {
		pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		fetches := cl.PollFetches(pctx)
		cancel()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errs := fetches.Errors(); len(errs) > 0 && !errors.Is(errs[0].Err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("read %s: %w", topic, errs[0].Err)
		}
		if fetches.NumRecords() == 0 {
			return nil, fmt.Errorf("read %s: timed out waiting for %d partition(s)", topic, len(remaining))
		}
		fetches.EachRecord(func(rec *kgo.Record) {
			end, ok := remaining[rec.Partition]
			if !ok {
				return
			}
			out = append(out, fromRecord(rec))
			if rec.Offset+1 >= end {
				delete(remaining, rec.Partition)
			}
		})
	}
	return out, nil
}

// Consumer is a consumer-group member with manual, in-order-safe commits.
type Consumer struct {
	cl      *kgo.Client
	ch      chan *kgo.Record
	cancel  context.CancelFunc
	tracker *offsets.Tracker

	mu   sync.Mutex
	recs map[recKey]*kgo.Record // polled but not yet committed
}

type recKey struct {
	topic string
	part  int32
	off   int64
}

func NewConsumer(brokers []string, group, topic string) (*Consumer, error) {
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.ConsumerGroup(group), kgo.ConsumeTopics(topic),
		kgo.DisableAutoCommit(), kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &Consumer{cl: cl, ch: make(chan *kgo.Record, 256), cancel: cancel, tracker: offsets.NewTracker(), recs: map[recKey]*kgo.Record{}}
	go c.pump(ctx)
	return c, nil
}

// pump is the only goroutine that calls PollFetches.
func (c *Consumer) pump(ctx context.Context) {
	defer close(c.ch)
	for {
		f := c.cl.PollFetches(ctx)
		if f.IsClientClosed() || ctx.Err() != nil {
			return
		}
		f.EachRecord(func(r *kgo.Record) {
			c.tracker.Seen(r.Topic, r.Partition, r.Offset)
			c.mu.Lock()
			c.recs[recKey{r.Topic, r.Partition, r.Offset}] = r
			c.mu.Unlock()
			select {
			case c.ch <- r:
			case <-ctx.Done():
			}
		})
	}
}

func (c *Consumer) Poll(ctx context.Context) (pipeline.Message, error) {
	select {
	case r, ok := <-c.ch:
		if !ok {
			return pipeline.Message{}, pipeline.ErrClosed
		}
		return fromRecord(r), nil
	case <-ctx.Done():
		return pipeline.Message{}, ctx.Err()
	}
}

// Commit marks m finished and commits the contiguous low-water mark of its partition.
func (c *Consumer) Commit(ctx context.Context, m pipeline.Message) error {
	p := int32(m.Partition)
	from, to, advanced := c.tracker.Done(m.Topic, p, m.Offset)
	if !advanced {
		return nil
	}
	c.mu.Lock()
	last := c.recs[recKey{m.Topic, p, to}]
	for o := from; o <= to; o++ {
		delete(c.recs, recKey{m.Topic, p, o})
	}
	c.mu.Unlock()
	if last == nil {
		return fmt.Errorf("commit: record %s/%d@%d not tracked", m.Topic, p, to)
	}
	return c.cl.CommitRecords(ctx, last)
}

func (c *Consumer) Close() { c.cancel(); c.cl.Close() }
