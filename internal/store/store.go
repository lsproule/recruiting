package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"recruiting/internal/store/db"
)

// Scope is the tenant a transaction runs under; RLS policies read it from
// session settings.
type Scope struct {
	OrgID           uuid.UUID
	IsClient        bool      // client user: must also carry ClientCompanyID
	ClientCompanyID uuid.UUID // non-zero only for client users
}

// Principal is what service.Principal satisfies; store cannot import service.
type Principal interface {
	Scope() Scope
}

// Lookup identifies a credential before its org is known (login, session
// cookie, magic link, API token). Only matching rows become visible.
type Lookup struct {
	TokenHash string
	Email     string
}

var (
	ErrNoOrg           = errors.New("store: principal has no org")
	ErrNoClientCompany = errors.New("store: client principal has no client company")
	ErrNotMigrated     = errors.New("store: schema missing; run migrations first")
	ErrNoLookup        = errors.New("store: lookup has neither token nor email")
	ErrNoPublicJob     = errors.New("store: public job lookup needs an org slug and a job slug")
	ErrOwnerRole       = errors.New("store: connection role owns the schema; RLS would be bypassed, connect as app_rw")
)

// Tx is a tenant-scoped transaction. Q runs generated queries; the embedded
// pgx.Tx runs ad-hoc SQL. Both are subject to RLS.
type Tx struct {
	pgx.Tx
	Q *db.Queries
}

// Store is the tenant-scoped connection pool used by serve and worker.
type Store struct {
	pool *pgxpool.Pool
}

// Open connects and refuses roles that own the tables or bypass RLS.
func Open(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("store: connect: %w", err)
	}
	var migrated bool
	if err := pool.QueryRow(ctx, `select to_regclass('public.org') is not null`).Scan(&migrated); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: inspect schema: %w", err)
	}
	if !migrated {
		pool.Close()
		return nil, ErrNotMigrated
	}
	var bypass bool
	err = pool.QueryRow(ctx, `
		select r.rolbypassrls or r.rolsuper or pg_get_userbyid(c.relowner) = current_user
		from pg_roles r, pg_class c
		where r.rolname = current_user and c.oid = 'public.org'::regclass`).Scan(&bypass)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: inspect role: %w", err)
	}
	if bypass {
		pool.Close()
		return nil, ErrOwnerRole
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

// Pool exposes the underlying pool for components (e.g. the queue) that
// manage their own connections; RLS still applies to it.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// WithTx runs fn in a transaction scoped to p's org (and client company).
// Rolls back on error or panic; commits otherwise.
func (s *Store) WithTx(ctx context.Context, p Principal, fn func(ctx context.Context, tx *Tx) error) error {
	scope := p.Scope()
	if scope.OrgID == uuid.Nil {
		return ErrNoOrg
	}
	if scope.IsClient && scope.ClientCompanyID == uuid.Nil {
		return ErrNoClientCompany
	}
	return runTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select set_config('app.org_id', $1, true)`, scope.OrgID.String()); err != nil {
			return err
		}
		if scope.IsClient {
			if _, err := tx.Exec(ctx, `select set_config('app.client_company_id', $1, true)`, scope.ClientCompanyID.String()); err != nil {
				return err
			}
		}
		return fn(ctx, &Tx{Tx: tx, Q: db.New(tx)})
	})
}

// WithLookupTx runs fn with only the credential rows matching l visible; no
// org is set, so every other table is empty.
func (s *Store) WithLookupTx(ctx context.Context, l Lookup, fn func(ctx context.Context, tx *Tx) error) error {
	if l.TokenHash == "" && l.Email == "" {
		return ErrNoLookup
	}
	return runTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		if l.TokenHash != "" {
			if _, err := tx.Exec(ctx, `select set_config('app.lookup_token', $1, true)`, l.TokenHash); err != nil {
				return err
			}
		}
		if l.Email != "" {
			if _, err := tx.Exec(ctx, `select set_config('app.lookup_email', $1, true)`, l.Email); err != nil {
				return err
			}
		}
		return fn(ctx, &Tx{Tx: tx, Q: db.New(tx)})
	})
}

// WithPublicJobTx runs fn for the unauthenticated apply page, which knows an
// org slug and a job slug and nothing else. No org id is set, so only the org
// answering to orgSlug and its open job answering to jobSlug are visible and
// every other table stays empty; the caller re-enters through WithTx once the
// job names its org. Both halves are required: a job slug is unique within an
// org, so on its own it would address another tenant's job just as well.
func (s *Store) WithPublicJobTx(ctx context.Context, orgSlug, jobSlug string, fn func(ctx context.Context, tx *Tx) error) error {
	if orgSlug == "" || jobSlug == "" {
		return ErrNoPublicJob
	}
	return runTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		for _, setting := range []struct{ name, value string }{
			{"app.public_org_slug", orgSlug},
			{"app.public_job_slug", jobSlug},
		} {
			if _, err := tx.Exec(ctx, `select set_config($1, $2, true)`, setting.name, setting.value); err != nil {
				return err
			}
		}
		return fn(ctx, &Tx{Tx: tx, Q: db.New(tx)})
	})
}

type beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

func runTx(ctx context.Context, b beginner, fn func(ctx context.Context, tx pgx.Tx) error) (err error) {
	tx, err := b.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
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
	if err = fn(ctx, tx); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}
