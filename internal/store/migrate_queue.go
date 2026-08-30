package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

// queueMigrateLockKey serializes concurrent queue migrations the way goose's
// session locker serializes the application's own.
const queueMigrateLockKey = 7372

// appRole is the unprivileged role serve and worker connect as. The queue's
// tables are created by the owner after the application's migrations have set
// default privileges, so the grants below are belt and braces — they also
// cover a database whose default privileges were changed by hand.
const appRole = "app_rw"

// migrateQueueUp applies the queue library's own migrations and grants the
// application role access to what they create. Queue tables live in `public`
// alongside the domain tables, but hold no tenant data and have no RLS.
func migrateQueueUp(ctx context.Context, databaseURL string) error {
	return withQueueMigrator(ctx, databaseURL, func(ctx context.Context, pool *pgxpool.Pool, m *rivermigrate.Migrator[pgx.Tx]) error {
		if _, err := m.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
			return fmt.Errorf("migrate: queue up: %w", err)
		}
		for _, stmt := range []string{
			`grant select, insert, update, delete on all tables in schema public to ` + appRole,
			`grant usage, select on all sequences in schema public to ` + appRole,
		} {
			if _, err := pool.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("migrate: queue grants: %w", err)
			}
		}
		return nil
	})
}

// migrateQueueDown removes the queue's schema. A database that never had it
// is left alone.
func migrateQueueDown(ctx context.Context, databaseURL string) error {
	return withQueueMigrator(ctx, databaseURL, func(ctx context.Context, pool *pgxpool.Pool, m *rivermigrate.Migrator[pgx.Tx]) error {
		var present bool
		if err := pool.QueryRow(ctx, `select to_regclass('public.river_migration') is not null`).Scan(&present); err != nil {
			return fmt.Errorf("migrate: queue inspect: %w", err)
		}
		if !present {
			return nil
		}
		if _, err := m.Migrate(ctx, rivermigrate.DirectionDown, &rivermigrate.MigrateOpts{TargetVersion: -1}); err != nil {
			return fmt.Errorf("migrate: queue down: %w", err)
		}
		return nil
	})
}

func withQueueMigrator(ctx context.Context, databaseURL string, fn func(context.Context, *pgxpool.Pool, *rivermigrate.Migrator[pgx.Tx]) error) error {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("migrate: queue connect: %w", err)
	}
	defer pool.Close()

	// The lock is session-scoped and the migrator runs several transactions,
	// so it is held on one dedicated connection for the duration.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("migrate: queue lock connection: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `select pg_advisory_lock($1)`, queueMigrateLockKey); err != nil {
		return fmt.Errorf("migrate: queue lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), `select pg_advisory_unlock($1)`, queueMigrateLockKey)
	}()

	m, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return fmt.Errorf("migrate: queue migrator: %w", err)
	}
	return fn(ctx, pool, m)
}
