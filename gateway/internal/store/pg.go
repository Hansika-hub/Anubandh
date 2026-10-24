// Package store is the PostgreSQL read model (BUILD.md §7): connection, migrations and queries.
package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DBTX is satisfied by *pgxpool.Pool and pgx.Tx.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Store wraps the connection pool.
type Store struct {
	Pool *pgxpool.Pool
}

// Open connects to PostgreSQL and verifies the connection.
func Open(ctx context.Context, dsn string) (*Store, error) {
	if dsn == "" {
		return nil, errors.New("store: PG_DSN is empty")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("store: parse PG_DSN: %w", err)
	}
	if cfg.MaxConns < 10 {
		cfg.MaxConns = 10
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("store: connect: %w", err)
	}
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	return &Store{Pool: pool}, nil
}

// Close releases the pool.
func (s *Store) Close() { s.Pool.Close() }

// Ping checks connectivity.
func (s *Store) Ping(ctx context.Context) error { return s.Pool.Ping(ctx) }

// InTx runs fn in a transaction, committing on nil and rolling back otherwise.
func (s *Store) InTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// LockDeal serialises read-model writers of one deal for the rest of the transaction, so the
// listener and request-path reconciles never interleave (and never deadlock) on a deal's rows.
func LockDeal(ctx context.Context, tx pgx.Tx, dealID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 7))`, dealID)
	return err
}

// Migrate applies every *.sql file in dir in lexical order (0001_, 0002_, ...). The files are
// idempotent, so this is safe on every start; an advisory lock stops concurrent migrators.
func (s *Store) Migrate(ctx context.Context, dir string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("store: no migrations found in %s", dir)
	}
	sort.Strings(files)
	applied := make([]string, 0, len(files))
	err = s.InTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(4211)`); err != nil {
			return err
		}
		for _, f := range files {
			b, err := os.ReadFile(f) //nolint:gosec // files of the configured MIGRATIONS_DIR
			if err != nil {
				return err
			}
			if strings.TrimSpace(string(b)) == "" {
				continue
			}
			if _, err := tx.Exec(ctx, string(b)); err != nil {
				return fmt.Errorf("store: migration %s: %w", filepath.Base(f), err)
			}
			applied = append(applied, filepath.Base(f))
		}
		return nil
	})
	return applied, err
}

// IsTransient reports whether err is a connectivity/serialisation failure worth retrying, as
// opposed to a data error (bad JSON, constraint violation) that will fail the same way again.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case strings.HasPrefix(pgErr.Code, "08"), // connection exception
			pgErr.Code == "40001", pgErr.Code == "40P01", // serialization failure, deadlock
			strings.HasPrefix(pgErr.Code, "53"), // insufficient resources
			strings.HasPrefix(pgErr.Code, "57"): // operator intervention (shutdown)
			return true
		}
		return false
	}
	return true // network errors, timeouts, closed pool
}

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC().Truncate(time.Second)
	return &u
}
