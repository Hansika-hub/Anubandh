// Package scheduler drives the chaincode's time rules.
package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Hansika-hub/anubandh/gateway/internal/fabric"
	"github.com/Hansika-hub/anubandh/gateway/internal/store"
)

// Submitter is the chain write path (satisfied by *fabric.Submitter).
type Submitter interface {
	Submit(ctx context.Context, req fabric.SubmitRequest) (fabric.SubmitResult, error)
}

// Scheduler calls TickDeadlines once per interval for every active deal (BUILD.md §5.3, §8.1).
// TickDeadlines is idempotent and uses only the transaction timestamp, so overlapping or repeated
// ticks are harmless.
type Scheduler struct {
	store       *store.Store
	chain       Submitter
	id          fabric.Identity
	interval    time.Duration
	concurrency int
	log         *slog.Logger
	running     atomic.Bool
	lastRun     atomic.Pointer[time.Time]
}

// New builds a Scheduler. id signs TickDeadlines.
func New(st *store.Store, chain Submitter, id fabric.Identity, interval time.Duration, log *slog.Logger) *Scheduler {
	if interval <= 0 {
		interval = time.Minute
	}
	if log == nil {
		log = slog.Default()
	}
	return &Scheduler{store: st, chain: chain, id: id, interval: interval, concurrency: 4, log: log}
}

// Run ticks every interval until ctx ends (BUILD.md §8.2).
func (s *Scheduler) Run(ctx context.Context) {
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := s.TickOnce(ctx); err != nil && ctx.Err() == nil {
				s.log.Warn("deadline tick failed", "err", err)
			}
		}
	}
}

// LastRun returns when the last tick pass finished (nil before the first).
func (s *Scheduler) LastRun() *time.Time { return s.lastRun.Load() }

// TickOnce submits TickDeadlines for every active deal and returns how many succeeded.
// A pass already in progress makes a concurrent call a no-op.
func (s *Scheduler) TickOnce(ctx context.Context) (int, error) {
	if !s.running.CompareAndSwap(false, true) {
		return 0, nil
	}
	defer s.running.Store(false)
	ids, err := store.ActiveDealIDs(ctx, s.store.Pool)
	if err != nil {
		return 0, err
	}
	var ok atomic.Int64
	work := make(chan string)
	var wg sync.WaitGroup
	for i := 0; i < s.concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range work {
				tctx, cancel := context.WithTimeout(fabric.WithIdentity(ctx, s.id), 30*time.Second)
				_, err := s.chain.Submit(tctx, fabric.SubmitRequest{Fn: "TickDeadlines", Args: []string{id}})
				cancel()
				if err != nil {
					s.log.Warn("TickDeadlines failed", "dealId", id, "err", err)
					continue
				}
				ok.Add(1)
			}
		}()
	}
	for _, id := range ids {
		select {
		case work <- id:
		case <-ctx.Done():
		}
	}
	close(work)
	wg.Wait()
	now := time.Now().UTC()
	s.lastRun.Store(&now)
	return int(ok.Load()), ctx.Err()
}
