//go:build adapters

// Package pgstore implements pipeline.ProcessedStore on the processed_keys table
// (db/migrations/0002_outbox.sql, created by the gateway/`make migrate`). Needs pgx.
package pgstore

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct{ pool *pgxpool.Pool }

func New(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool}, nil
}

func (s *Store) AlreadyProcessed(ctx context.Context, idemKey string) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM processed_keys WHERE idem_key = $1)`, idemKey).Scan(&ok)
	return ok, err
}

// MarkProcessed is idempotent: a second call for the same key is a no-op.
func (s *Store) MarkProcessed(ctx context.Context, idemKey string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO processed_keys (idem_key) VALUES ($1) ON CONFLICT (idem_key) DO NOTHING`, idemKey)
	return err
}

func (s *Store) Close() { s.pool.Close() }
