// Package system opens the schema-owner connection that bypasses RLS. Only
// cmd/recruiting (admin and migrate modes) may import it; store's
// TestSystemPackageImportedOnlyByCommand enforces that.
package system

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

type Store struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("system store: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("system store: ping: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

// WithSystemTx runs fn without tenant scoping; RLS is bypassed because the
// connection role owns the tables.
func (s *Store) WithSystemTx(ctx context.Context, fn func(ctx context.Context, tx *store.Tx) error) (err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("system store: begin: %w", err)
	}
	defer func() {
		if r := recover(); r != nil {
			_ = tx.Rollback(ctx)
			panic(r)
		}
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()
	if err = fn(ctx, &store.Tx{Tx: tx, Q: db.New(tx)}); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("system store: commit: %w", err)
	}
	return nil
}

var _ pgx.Tx = (*store.Tx)(nil)
