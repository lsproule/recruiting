//go:build integration

package store_test

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"recruiting/internal/service"
	"recruiting/internal/store"
)

const uniqueViolation = "23505"

type harness struct {
	ctx context.Context
	sys *pgxpool.Pool // owner connection; bypasses RLS like the admin CLI
	app *store.Store
}

// asOwner runs fn in an owner transaction (no tenant scoping).
func (h *harness) asOwner(ctx context.Context, fn func(ctx context.Context, tx *store.Tx) error) error {
	tx, err := h.sys.Begin(ctx)
	if err != nil {
		return err
	}
	if err := fn(ctx, &store.Tx{Tx: tx}); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

// newHarness migrates the Compose Postgres as owner and opens the tenant
// store as app_rw. Owner URL comes from DATABASE_URL; app_rw shares host/db.
func newHarness(t *testing.T) *harness {
	t.Helper()
	ownerURL := os.Getenv("DATABASE_URL")
	if ownerURL == "" {
		t.Fatal("DATABASE_URL is not set; run `make dev-up` and use `make test-integration`")
	}
	lockSchema(t, ownerURL, false)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)

	if err := store.MigrateUp(ctx, ownerURL); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	sys, err := pgxpool.New(ctx, ownerURL)
	if err != nil {
		t.Fatalf("open owner pool: %v", err)
	}
	t.Cleanup(sys.Close)

	app, err := store.Open(ctx, appRWURL(t, ownerURL))
	if err != nil {
		t.Fatalf("open app store: %v", err)
	}
	t.Cleanup(app.Close)
	return &harness{ctx: ctx, sys: sys, app: app}
}

func appRWURL(t *testing.T, ownerURL string) string {
	t.Helper()
	u, err := url.Parse(ownerURL)
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	u.User = url.UserPassword("app_rw", "app_rw")
	return u.String()
}

func (h *harness) createOrg(t *testing.T) uuid.UUID {
	t.Helper()
	id := uuid.New()
	err := h.asOwner(h.ctx, func(ctx context.Context, tx *store.Tx) error {
		_, err := tx.Exec(ctx, `insert into org (id, name, slug) values ($1, $2, $3)`, id, "org "+id.String(), id.String())
		return err
	})
	if err != nil {
		t.Fatalf("create org: %v", err)
	}
	t.Cleanup(func() {
		_ = h.asOwner(context.Background(), func(ctx context.Context, tx *store.Tx) error {
			_, err := tx.Exec(ctx, `delete from org where id = $1`, id)
			return err
		})
	})
	return id
}

func orgPrincipal(orgID uuid.UUID) service.Principal {
	return service.Principal{Kind: service.PrincipalOrgUser, OrgID: orgID, UserID: uuid.New(), Roles: []string{"admin"}}
}

func (h *harness) exec(t *testing.T, p service.Principal, sql string, args ...any) {
	t.Helper()
	err := h.app.WithTx(h.ctx, p, func(ctx context.Context, tx *store.Tx) error {
		_, err := tx.Exec(ctx, sql, args...)
		return err
	})
	if err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func (h *harness) count(t *testing.T, p service.Principal, sql string) int {
	t.Helper()
	var n int
	err := h.app.WithTx(h.ctx, p, func(ctx context.Context, tx *store.Tx) error {
		return tx.QueryRow(ctx, sql).Scan(&n)
	})
	if err != nil {
		t.Fatalf("count %q: %v", sql, err)
	}
	return n
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation
}

func TestMigrationsRoundTrip(t *testing.T) {
	ownerURL := os.Getenv("DATABASE_URL")
	if ownerURL == "" {
		t.Skip("DATABASE_URL is not set")
	}
	lockSchema(t, ownerURL, true)
	ctx := context.Background()
	if err := store.MigrateUp(ctx, ownerURL); err != nil {
		t.Fatalf("up: %v", err)
	}
	if err := store.MigrateDownAll(ctx, ownerURL); err != nil {
		t.Fatalf("down: %v", err)
	}
	if err := store.MigrateUp(ctx, ownerURL); err != nil {
		t.Fatalf("up again: %v", err)
	}
}

func TestRLSIsolatesOrgsWithoutWhereFilter(t *testing.T) {
	h := newHarness(t)
	orgA, orgB := h.createOrg(t), h.createOrg(t)
	pA, pB := orgPrincipal(orgA), orgPrincipal(orgB)

	h.exec(t, pA, `insert into candidate (org_id, email, name) values ($1, 'a@example.com', 'A')`, orgA)
	h.exec(t, pB, `insert into candidate (org_id, email, name) values ($1, 'b@example.com', 'B')`, orgB)

	if n := h.count(t, pA, `select count(*) from candidate`); n != 1 {
		t.Fatalf("org A sees %d candidates, want 1", n)
	}
	if n := h.count(t, pB, `select count(*) from candidate where email = 'a@example.com'`); n != 0 {
		t.Fatalf("org B sees org A candidate")
	}
	// Writing another org's row is rejected by WITH CHECK.
	err := h.app.WithTx(h.ctx, pA, func(ctx context.Context, tx *store.Tx) error {
		_, err := tx.Exec(ctx, `insert into candidate (org_id, email, name) values ($1, 'x@example.com', 'X')`, orgB)
		return err
	})
	if err == nil {
		t.Fatal("insert into other org succeeded")
	}
}

func TestTxWithoutPrincipalSeesNothing(t *testing.T) {
	h := newHarness(t)
	orgA := h.createOrg(t)
	h.exec(t, orgPrincipal(orgA), `insert into candidate (org_id, email, name) values ($1, 'a@example.com', 'A')`, orgA)

	// A tx opened for a principal with no org must be refused outright.
	err := h.app.WithTx(h.ctx, service.Principal{Kind: service.PrincipalOrgUser}, func(context.Context, *store.Tx) error { return nil })
	if err == nil {
		t.Fatal("tx with zero OrgID was allowed")
	}
}

type clientFixture struct {
	org, company, otherCompany, job, otherJob, candidate, stage uuid.UUID
}

func (h *harness) seedClient(t *testing.T) clientFixture {
	t.Helper()
	f := clientFixture{org: h.createOrg(t), company: uuid.New(), otherCompany: uuid.New(), job: uuid.New(), otherJob: uuid.New(), candidate: uuid.New(), stage: uuid.New()}
	p := orgPrincipal(f.org)
	h.exec(t, p, `insert into client_company (id, org_id, name) values ($1, $2, 'Acme'), ($3, $2, 'Other')`, f.company, f.org, f.otherCompany)
	h.exec(t, p, `insert into job (id, org_id, client_company_id, title, slug) values ($1, $2, $3, 'Eng', 'eng'), ($4, $2, $5, 'Eng2', 'eng2')`, f.job, f.org, f.company, f.otherJob, f.otherCompany)
	h.exec(t, p, `insert into stage (id, org_id, job_id, position, name, kind) values ($1, $2, $3, 1, 'Applied', 'generic')`, f.stage, f.org, f.job)
	h.exec(t, p, `insert into candidate (id, org_id, email, name) values ($1, $2, 'c@example.com', 'C')`, f.candidate, f.org)
	return f
}

func TestClientUserSeesOnlyReleasedApplicationsOfOwnCompany(t *testing.T) {
	h := newHarness(t)
	f := h.seedClient(t)
	recruiter := orgPrincipal(f.org)
	client := service.Principal{Kind: service.PrincipalClientUser, OrgID: f.org, UserID: uuid.New(), ClientCompanyID: f.company}

	h.exec(t, recruiter, `insert into application (org_id, job_id, candidate_id, client_company_id, stage_id) values ($1, $2, $3, $4, $5)`, f.org, f.job, f.candidate, f.company, f.stage)
	if n := h.count(t, client, `select count(*) from application`); n != 0 {
		t.Fatalf("client sees %d unreleased applications, want 0", n)
	}
	if n := h.count(t, client, `select count(*) from job`); n != 1 {
		t.Fatalf("client sees %d jobs, want 1 (own company only)", n)
	}

	h.exec(t, recruiter, `update application set released_at = now() where job_id = $1`, f.job)
	if n := h.count(t, client, `select count(*) from application`); n != 1 {
		t.Fatalf("client sees %d released applications, want 1", n)
	}

	// Released application for another company stays invisible.
	other := service.Principal{Kind: service.PrincipalClientUser, OrgID: f.org, UserID: uuid.New(), ClientCompanyID: f.otherCompany}
	if n := h.count(t, other, `select count(*) from application`); n != 0 {
		t.Fatalf("other client sees %d applications, want 0", n)
	}
	// Integrity data is never visible to clients.
	if n := h.count(t, client, `select count(*) from integrity_signal`); n != 0 {
		t.Fatalf("client sees integrity signals")
	}
}

func TestAppRoleIsNotOwnerAndRLSIsForced(t *testing.T) {
	h := newHarness(t)
	err := h.asOwner(h.ctx, func(ctx context.Context, tx *store.Tx) error {
		var bypass bool
		if err := tx.QueryRow(ctx, `select rolbypassrls from pg_roles where rolname = 'app_rw'`).Scan(&bypass); err != nil {
			return err
		}
		if bypass {
			t.Error("app_rw has BYPASSRLS")
		}
		rows, err := tx.Query(ctx, `
			select c.relname, pg_get_userbyid(c.relowner), c.relrowsecurity, c.relforcerowsecurity
			from pg_class c join pg_namespace n on n.oid = c.relnamespace
			where n.nspname = 'public' and c.relkind = 'r'
			  and c.relname <> 'goose_db_version'
			  -- The job queue's own tables are infrastructure, not tenant
			  -- data: no org_id to scope by, so no RLS to force. A payload
			  -- names the org whenever its handler needs one.
			  and c.relname not like 'river\_%'`)
		if err != nil {
			return err
		}
		defer rows.Close()
		seen := 0
		for rows.Next() {
			var name, owner string
			var enabled, forced bool
			if err := rows.Scan(&name, &owner, &enabled, &forced); err != nil {
				return err
			}
			seen++
			if owner == "app_rw" {
				t.Errorf("table %s is owned by app_rw", name)
			}
			if !enabled || !forced {
				t.Errorf("table %s: rls enabled=%v forced=%v", name, enabled, forced)
			}
		}
		if seen == 0 {
			t.Error("no tables found")
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAllSpecTablesExist(t *testing.T) {
	h := newHarness(t)
	want := strings.Fields(`org org_user org_user_role client_company client_user session magic_link api_token
		candidate resume job pipeline_template pipeline_template_stage stage application application_event
		scorecard_rubric scorecard availability_rule availability_exception interview_slot problem test_case
		assessment assessment_problem attempt submission attempt_event integrity_signal review
		talent_pool_entry email_log org_setting`)
	for _, tbl := range want {
		var ok bool
		err := h.asOwner(h.ctx, func(ctx context.Context, tx *store.Tx) error {
			return tx.QueryRow(ctx, `select to_regclass($1) is not null`, "public."+tbl).Scan(&ok)
		})
		if err != nil || !ok {
			t.Errorf("table %s missing (err=%v)", tbl, err)
		}
	}
}

func TestUniqueConstraints(t *testing.T) {
	h := newHarness(t)
	f := h.seedClient(t)
	p := orgPrincipal(f.org)
	vetter := uuid.New()
	h.exec(t, p, `insert into org_user (id, org_id, email, name) values ($1, $2, 'v@example.com', 'V')`, vetter, f.org)
	h.exec(t, p, `insert into application (org_id, job_id, candidate_id, client_company_id, stage_id) values ($1, $2, $3, $4, $5)`, f.org, f.job, f.candidate, f.company, f.stage)
	starts := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	h.exec(t, p, `insert into interview_slot (org_id, vetter_id, starts_at, ends_at) values ($1, $2, $3, $4)`, f.org, vetter, starts, starts.Add(30*time.Minute))

	dup := func(name, sql string, args ...any) {
		err := h.app.WithTx(h.ctx, p, func(ctx context.Context, tx *store.Tx) error {
			_, err := tx.Exec(ctx, sql, args...)
			return err
		})
		if !isUniqueViolation(err) {
			t.Errorf("%s: want unique violation, got %v", name, err)
		}
	}
	dup("application(job,candidate)", `insert into application (org_id, job_id, candidate_id, client_company_id, stage_id) values ($1, $2, $3, $4, $5)`, f.org, f.job, f.candidate, f.company, f.stage)
	dup("interview_slot(vetter,starts_at)", `insert into interview_slot (org_id, vetter_id, starts_at, ends_at) values ($1, $2, $3, $4)`, f.org, vetter, starts, starts.Add(30*time.Minute))
	dup("candidate(org,lower(email))", `insert into candidate (org_id, email, name) values ($1, 'C@EXAMPLE.COM', 'dup')`, f.org)
	// Two attempt_events with the same seq are rejected; second uses a different seq to prove the first insert works.
	h.exec(t, p, `insert into problem (id, org_id, kind, title, statement) values ('10000000-0000-0000-0000-000000000001', $1, 'code', 'p', 's')`, f.org)
	h.exec(t, p, `insert into assessment (id, org_id, name, duration_minutes) values ('20000000-0000-0000-0000-000000000001', $1, 'a', 60)`, f.org)
	h.exec(t, p, `insert into attempt (id, org_id, application_id, assessment_id, stage_id)
		select '30000000-0000-0000-0000-000000000001', $1, id, '20000000-0000-0000-0000-000000000001', stage_id from application limit 1`, f.org)
	h.exec(t, p, `insert into attempt_event (org_id, attempt_id, seq, kind, payload) values ($1, '30000000-0000-0000-0000-000000000001', 1, 'focus', '{}')`, f.org)
	dup("attempt_event(attempt,seq)", `insert into attempt_event (org_id, attempt_id, seq, kind, payload) values ($1, '30000000-0000-0000-0000-000000000001', 1, 'focus', '{}')`, f.org)
}

func TestPlatformProblemsVisibleButReadOnly(t *testing.T) {
	h := newHarness(t)
	orgA := h.createOrg(t)
	p := orgPrincipal(orgA)
	pid := uuid.New()
	err := h.asOwner(h.ctx, func(ctx context.Context, tx *store.Tx) error {
		_, err := tx.Exec(ctx, `insert into problem (id, org_id, kind, title, statement) values ($1, platform_org_id(), 'code', 'seed', 's')`, pid)
		return err
	})
	if err != nil {
		t.Fatalf("seed platform problem: %v", err)
	}
	t.Cleanup(func() {
		_ = h.asOwner(context.Background(), func(ctx context.Context, tx *store.Tx) error {
			_, err := tx.Exec(ctx, `delete from problem where id = $1`, pid)
			return err
		})
	})
	if n := h.count(t, p, `select count(*) from problem`); n != 1 {
		t.Fatalf("org sees %d platform problems, want 1", n)
	}
	err = h.app.WithTx(h.ctx, p, func(ctx context.Context, tx *store.Tx) error {
		tag, err := tx.Exec(ctx, `update problem set title = 'hacked' where id = $1`, pid)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 0 {
			return errors.New("tenant updated a platform problem")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestOpenRefusesOwnerConnection(t *testing.T) {
	ownerURL := os.Getenv("DATABASE_URL")
	if ownerURL == "" {
		t.Skip("DATABASE_URL is not set")
	}
	ctx := context.Background()
	if err := store.MigrateUp(ctx, ownerURL); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	if _, err := store.Open(ctx, ownerURL); err == nil {
		t.Fatal("store.Open accepted a connection that owns the tables (RLS would be bypassed)")
	}
}

func clientPrincipal(f clientFixture) service.Principal {
	return service.Principal{Kind: service.PrincipalClientUser, OrgID: f.org, UserID: uuid.New(), ClientCompanyID: f.company}
}

// rowsAffected runs sql as p and returns rows affected; an error is returned as-is.
func (h *harness) rowsAffected(t *testing.T, p service.Principal, sql string, args ...any) (int64, error) {
	t.Helper()
	var n int64
	err := h.app.WithTx(h.ctx, p, func(ctx context.Context, tx *store.Tx) error {
		tag, err := tx.Exec(ctx, sql, args...)
		n = tag.RowsAffected()
		return err
	})
	return n, err
}

func TestClientCannotWriteTenantOrAuthRows(t *testing.T) {
	h := newHarness(t)
	f := h.seedClient(t)
	recruiter, client := orgPrincipal(f.org), clientPrincipal(f)
	admin := uuid.New()
	h.exec(t, recruiter, `insert into org_user (id, org_id, email, name) values ($1, $2, 'admin@example.com', 'Admin')`, admin, f.org)

	if n, err := h.rowsAffected(t, client, `delete from org`); err != nil || n != 0 {
		t.Fatalf("client delete from org: rows=%d err=%v", n, err)
	}
	if n := h.count(t, recruiter, `select count(*) from org`); n != 1 {
		t.Fatalf("org gone after client delete")
	}
	if _, err := h.rowsAffected(t, client, `update org set name = 'x'`); err == nil {
		if n := h.count(t, recruiter, `select count(*) from org where name = 'x'`); n != 0 {
			t.Fatal("client renamed org")
		}
	}
	for _, tbl := range []string{"job", "candidate", "client_company", "org_user", "stage"} {
		if n, err := h.rowsAffected(t, client, "delete from "+tbl); err != nil || n != 0 {
			t.Errorf("client delete from %s: rows=%d err=%v", tbl, n, err)
		}
	}
	if _, err := h.rowsAffected(t, client, `insert into session (org_id, org_user_id, token_hash, expires_at) values ($1, $2, 'forged', now() + interval '1 day')`, f.org, admin); err == nil {
		t.Fatal("client inserted a session for an org user")
	}
	if _, err := h.rowsAffected(t, client, `insert into magic_link (org_id, token_hash, purpose, subject_id, expires_at) values ($1, 'forged', 'apply', $2, now() + interval '1 day')`, f.org, f.job); err == nil {
		t.Fatal("client inserted a magic link")
	}
}

func TestClientCannotReadCredentials(t *testing.T) {
	h := newHarness(t)
	f := h.seedClient(t)
	recruiter, client := orgPrincipal(f.org), clientPrincipal(f)
	uid := uuid.New()
	h.exec(t, recruiter, `insert into org_user (id, org_id, email, name) values ($1, $2, 'u@example.com', 'U')`, uid, f.org)
	h.exec(t, recruiter, `insert into org_user_credential (org_user_id, org_id, password_hash) values ($1, $2, 'secret')`, uid, f.org)

	if n := h.count(t, recruiter, `select count(*) from information_schema.columns where table_name in ('org_user', 'client_user') and column_name = 'password_hash'`); n != 0 {
		t.Fatalf("password_hash still on user tables")
	}
	if n := h.count(t, client, `select count(*) from org_user_credential`); n != 0 {
		t.Fatalf("client reads %d org_user_credential rows", n)
	}
	if n := h.count(t, client, `select count(*) from client_user_credential`); n != 0 {
		t.Fatalf("client reads %d client_user_credential rows", n)
	}
	// Login lookup by email sees exactly the matching credential.
	var seen int
	err := h.app.WithLookupTx(h.ctx, store.Lookup{Email: "U@example.com"}, func(ctx context.Context, tx *store.Tx) error {
		return tx.QueryRow(ctx, `select count(*) from org_user_credential c join org_user u on u.id = c.org_user_id`).Scan(&seen)
	})
	if err != nil || seen != 1 {
		t.Fatalf("lookup by email: seen=%d err=%v", seen, err)
	}
}

func TestClientPrincipalWithoutCompanyIsRefused(t *testing.T) {
	h := newHarness(t)
	orgA := h.createOrg(t)
	p := service.Principal{Kind: service.PrincipalClientUser, OrgID: orgA, UserID: uuid.New()}
	err := h.app.WithTx(h.ctx, p, func(context.Context, *store.Tx) error { return nil })
	if !errors.Is(err, store.ErrNoClientCompany) {
		t.Fatalf("want ErrNoClientCompany, got %v", err)
	}
}

func TestClientCannotMutateApplications(t *testing.T) {
	h := newHarness(t)
	f := h.seedClient(t)
	recruiter, client := orgPrincipal(f.org), clientPrincipal(f)
	appID := uuid.New()
	h.exec(t, recruiter, `insert into application (id, org_id, job_id, candidate_id, client_company_id, stage_id, released_at) values ($1, $2, $3, $4, $5, $6, now())`, appID, f.org, f.job, f.candidate, f.company, f.stage)

	if n := h.count(t, client, `select count(*) from application`); n != 1 {
		t.Fatalf("client sees %d released applications, want 1", n)
	}
	if n, err := h.rowsAffected(t, client, `update application set status = 'hired'`); err != nil || n != 0 {
		t.Fatalf("client update application: rows=%d err=%v", n, err)
	}
	if n, err := h.rowsAffected(t, client, `delete from application`); err != nil || n != 0 {
		t.Fatalf("client delete application: rows=%d err=%v", n, err)
	}
	other := uuid.New()
	h.exec(t, recruiter, `insert into candidate (id, org_id, email, name) values ($1, $2, 'o@example.com', 'O')`, other, f.org)
	if _, err := h.rowsAffected(t, client, `insert into application (org_id, job_id, candidate_id, client_company_id, stage_id, released_at) values ($1, $2, $3, $4, $5, now())`, f.org, f.job, other, f.company, f.stage); err == nil {
		t.Fatal("client inserted an application")
	}
	// Events: clients may append (audited actions) and read, never edit.
	h.exec(t, client, `insert into application_event (org_id, application_id, actor_kind, actor_id, kind, reason) values ($1, $2, 'client_user', $3, 'client_reject', 'no')`, f.org, appID, client.UserID)
	if n := h.count(t, client, `select count(*) from application_event`); n != 1 {
		t.Fatalf("client sees %d events, want 1", n)
	}
	if n, err := h.rowsAffected(t, client, `delete from application_event`); err != nil || n != 0 {
		t.Fatalf("client delete event: rows=%d err=%v", n, err)
	}
	if n, err := h.rowsAffected(t, client, `update application_event set reason = 'edited'`); err != nil || n != 0 {
		t.Fatalf("client update event: rows=%d err=%v", n, err)
	}
}
