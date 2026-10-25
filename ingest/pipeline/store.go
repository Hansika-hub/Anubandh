package pipeline

import (
	"context"
	"sync"
)

// ProcessedStore is the processed_keys table (db/migrations/0002_outbox.sql).
// MarkProcessed MUST be idempotent (INSERT ... ON CONFLICT DO NOTHING).
type ProcessedStore interface {
	AlreadyProcessed(ctx context.Context, idemKey string) (bool, error)
	MarkProcessed(ctx context.Context, idemKey string) error
}

// MemStore is an in-memory ProcessedStore.
type MemStore struct {
	mu   sync.Mutex
	keys map[string]struct{}
}

func NewMemStore() *MemStore { return &MemStore{keys: map[string]struct{}{}} }

func (s *MemStore) AlreadyProcessed(_ context.Context, k string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.keys[k]
	return ok, nil
}

func (s *MemStore) MarkProcessed(_ context.Context, k string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys[k] = struct{}{}
	return nil
}

// Len is the number of recorded keys.
func (s *MemStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.keys)
}
