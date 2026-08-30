package store

import (
	"context"
	"database/sql"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver for goose
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"recruiting/db/migrations"
)

// MigrateUp applies pending migrations. Must run as the schema owner, which
// is also why migrations create app_rw rather than expecting it to exist.
//
// The job queue owns its own schema and ships it as library migrations, so it
// is applied here too, after the application's own. Its tables are
// infrastructure rather than tenant data: they carry no org_id and no RLS,
// and a job payload names the org whenever its handler needs one.
func MigrateUp(ctx context.Context, databaseURL string) error {
	if err := withGoose(ctx, databaseURL, func(p *goose.Provider) error {
		_, err := p.Up(ctx)
		return err
	}); err != nil {
		return err
	}
	return migrateQueueUp(ctx, databaseURL)
}

// MigrateDownAll rolls every migration back; used to prove down paths work.
// The queue's schema goes first: the last application migration drops app_rw,
// and the grants on the queue tables go with it.
func MigrateDownAll(ctx context.Context, databaseURL string) error {
	if err := migrateQueueDown(ctx, databaseURL); err != nil {
		return err
	}
	return withGoose(ctx, databaseURL, func(p *goose.Provider) error {
		_, err := p.DownTo(ctx, 0)
		return err
	})
}

func withGoose(ctx context.Context, databaseURL string, fn func(*goose.Provider) error) error {
	sqlDB, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("migrate: open: %w", err)
	}
	defer func() { _ = sqlDB.Close() }()
	if err := sqlDB.PingContext(ctx); err != nil {
		return fmt.Errorf("migrate: ping: %w", err)
	}
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return fmt.Errorf("migrate: locker: %w", err)
	}
	p, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS, goose.WithSessionLocker(locker))
	if err != nil {
		return fmt.Errorf("migrate: provider: %w", err)
	}
	if err := fn(p); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}
