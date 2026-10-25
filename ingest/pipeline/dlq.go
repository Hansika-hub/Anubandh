package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

// DLQ record kinds on the dead-letter topic.
const (
	KindDead     = "dead"
	KindReplayed = "replayed" // marker: the dead entry with ID was re-enqueued
)

var (
	ErrDLQNotFound = errors.New("dlq entry not found")
	ErrDLQReplayed = errors.New("dlq entry already replayed")
)

// DLQRecord is the JSON value stored on anubandh.events.dlq.
type DLQRecord struct {
	Kind     string        `json:"kind"`
	Event    *InboundEvent `json:"event,omitempty"`
	Reason   string        `json:"reason,omitempty"`
	Attempts int           `json:"attempts,omitempty"`
	FailedAt time.Time     `json:"failedAt,omitempty"`
	ID       string        `json:"id,omitempty"` // KindReplayed only: the entry that was replayed
}

// DLQEntry is one dead-lettered event as shown to operators.
type DLQEntry struct {
	ID       string       `json:"id"` // "<partition>-<offset>"
	Event    InboundEvent `json:"event"`
	Reason   string       `json:"reason"`
	Attempts int          `json:"attempts"`
	FailedAt time.Time    `json:"failedAt"`
	Replayed bool         `json:"replayed"`
}

// DLQ lists and replays dead-lettered events. The DLQ topic is the only source of truth, so the
// view survives restarts: replays are recorded as marker records on the same topic.
type DLQ struct {
	Reader TopicReader
	Out    Producer
	Now    func() time.Time
}

func entryID(m Message) string { return fmt.Sprintf("%d-%d", m.Partition, m.Offset) }

func (d *DLQ) load(ctx context.Context) ([]DLQEntry, error) {
	msgs, err := d.Reader.ReadAll(ctx, TopicDLQ)
	if err != nil {
		return nil, err
	}
	replayed := map[string]bool{}
	var out []DLQEntry
	for _, m := range msgs {
		var r DLQRecord
		if json.Unmarshal(m.Value, &r) != nil {
			continue // not ours; never fail the operator view on a foreign record
		}
		switch r.Kind {
		case KindReplayed:
			replayed[r.ID] = true
		case KindDead:
			if r.Event != nil {
				out = append(out, DLQEntry{ID: entryID(m), Event: *r.Event, Reason: r.Reason, Attempts: r.Attempts, FailedAt: r.FailedAt})
			}
		}
	}
	for i := range out {
		out[i].Replayed = replayed[out[i].ID]
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].FailedAt.After(out[j].FailedAt) })
	return out, nil
}

// List returns dead entries, newest first, up to limit (0 = no limit). Replayed entries are hidden
// unless includeReplayed.
func (d *DLQ) List(ctx context.Context, limit int, includeReplayed bool) ([]DLQEntry, error) {
	all, err := d.load(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]DLQEntry, 0, len(all))
	for _, e := range all {
		if e.Replayed && !includeReplayed {
			continue
		}
		out = append(out, e)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, nil
}

// Replay re-enqueues the event on the inbound topic. Safe to repeat: the worker dedupes on idemKey.
func (d *DLQ) Replay(ctx context.Context, id string) error {
	all, err := d.load(ctx)
	if err != nil {
		return err
	}
	for _, e := range all {
		if e.ID != id {
			continue
		}
		if e.Replayed {
			return ErrDLQReplayed
		}
		val, err := json.Marshal(e.Event)
		if err != nil {
			return err
		}
		if err := d.Out.Publish(ctx, Message{Topic: TopicInbound, Key: e.Event.DealID, Value: val,
			Headers: map[string]string{HdrIdemKey: e.Event.IdemKey, HdrReplay: "true"}}); err != nil {
			return err
		}
		marker, _ := json.Marshal(DLQRecord{Kind: KindReplayed, ID: id})
		return d.Out.Publish(ctx, Message{Topic: TopicDLQ, Key: e.Event.DealID, Value: marker,
			Headers: map[string]string{HdrIdemKey: e.Event.IdemKey, HdrKind: KindReplayed}})
	}
	return ErrDLQNotFound
}
