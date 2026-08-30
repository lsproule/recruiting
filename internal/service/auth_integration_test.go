//go:build integration

package service_test

import (
	"context"
	"errors"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"recruiting/internal/service"
	"recruiting/internal/store"
)

type fixture struct {
	ctx      context.Context
	st       *store.Store
	orgID    uuid.UUID
	userID   uuid.UUID
	email    string
	password string
	auth     *service.AuthService
	links    *service.MagicLinkService
}

// newFixture migrates as owner, seeds an org with one admin user, and opens
// the tenant store as app_rw.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	ownerURL := os.Getenv("DATABASE_URL")
	if ownerURL == "" {
		t.Fatal("DATABASE_URL is not set; run `make dev-up` and use `make test-integration`")
	}
	lockSchema(t, ownerURL)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	if err := store.MigrateUp(ctx, ownerURL); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	sys, err := pgxpool.New(ctx, ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sys.Close)

	u, _ := url.Parse(ownerURL)
	u.User = url.UserPassword("app_rw", "app_rw")
	st, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(st.Close)

	f := &fixture{ctx: ctx, st: st, orgID: uuid.New(), userID: uuid.New(), password: "hunter2-long-enough"}
	f.email = f.orgID.String() + "@example.com"
	hash, err := service.HashPassword(f.password)
	if err != nil {
		t.Fatal(err)
	}
	_, err = sys.Exec(ctx, `insert into org (id, name, slug) values ($1, $2, $2)`, f.orgID, f.orgID.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = sys.Exec(context.Background(), `delete from org where id = $1`, f.orgID) })
	if _, err := sys.Exec(ctx, `insert into org_user (id, org_id, email, name) values ($1, $2, $3, 'Admin')`, f.userID, f.orgID, f.email); err != nil {
		t.Fatal(err)
	}
	if _, err := sys.Exec(ctx, `insert into org_user_role (org_user_id, org_id, role) values ($1, $2, 'admin')`, f.userID, f.orgID); err != nil {
		t.Fatal(err)
	}
	if _, err := sys.Exec(ctx, `insert into org_user_credential (org_user_id, org_id, password_hash) values ($1, $2, $3)`, f.userID, f.orgID, hash); err != nil {
		t.Fatal(err)
	}
	f.auth = service.NewAuthService(st)
	f.links = service.NewMagicLinkService(st)
	return f
}

func (f *fixture) admin() service.Principal {
	return service.Principal{Kind: service.PrincipalOrgUser, OrgID: f.orgID, UserID: f.userID, Roles: []string{"admin"}}
}

func TestLoginPasswordCheck(t *testing.T) {
	f := newFixture(t)
	if _, err := f.auth.Login(f.ctx, service.SurfaceApp, f.email, "nope", ""); !errors.Is(err, service.ErrInvalidCredentials) {
		t.Fatalf("wrong password: got %v", err)
	}
	if _, err := f.auth.Login(f.ctx, service.SurfaceApp, "nobody@example.com", f.password, ""); !errors.Is(err, service.ErrInvalidCredentials) {
		t.Fatalf("unknown email: got %v", err)
	}
	// An org user must not be able to log in on the client surface.
	if _, err := f.auth.Login(f.ctx, service.SurfaceClient, f.email, f.password, ""); !errors.Is(err, service.ErrInvalidCredentials) {
		t.Fatalf("org user on client surface: got %v", err)
	}
	res, err := f.auth.Login(f.ctx, service.SurfaceApp, f.email, f.password, "")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if res.Token == "" || res.Principal.Kind != service.PrincipalOrgUser || res.Principal.OrgID != f.orgID || res.Principal.UserID != f.userID || !res.Principal.HasRole("admin") {
		t.Fatalf("bad result: %+v", res)
	}
	p, err := f.auth.ResolveSession(f.ctx, res.Token)
	if err != nil || p.UserID != f.userID {
		t.Fatalf("resolve: %+v %v", p, err)
	}
	if err := f.auth.Logout(f.ctx, res.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := f.auth.ResolveSession(f.ctx, res.Token); !errors.Is(err, service.ErrNoSession) {
		t.Fatalf("after logout: %v", err)
	}
}

func TestLoginRotatesSession(t *testing.T) {
	f := newFixture(t)
	first, err := f.auth.Login(f.ctx, service.SurfaceApp, f.email, f.password, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.auth.Login(f.ctx, service.SurfaceApp, f.email, f.password, first.Token)
	if err != nil {
		t.Fatal(err)
	}
	if first.Token == second.Token {
		t.Fatal("session token not rotated")
	}
	if _, err := f.auth.ResolveSession(f.ctx, first.Token); !errors.Is(err, service.ErrNoSession) {
		t.Fatalf("old session still valid: %v", err)
	}
	if _, err := f.auth.ResolveSession(f.ctx, second.Token); err != nil {
		t.Fatalf("new session invalid: %v", err)
	}
}

func TestSessionExpirySlides(t *testing.T) {
	f := newFixture(t)
	res, err := f.auth.Login(f.ctx, service.SurfaceApp, f.email, f.password, "")
	if err != nil {
		t.Fatal(err)
	}
	// Pull the expiry back so a resolve must extend it.
	old := time.Now().Add(time.Minute)
	err = f.st.WithTx(f.ctx, f.admin(), func(ctx context.Context, tx *store.Tx) error {
		_, err := tx.Exec(ctx, `update session set expires_at = $1 where org_user_id = $2`, old, f.userID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.auth.ResolveSession(f.ctx, res.Token); err != nil {
		t.Fatal(err)
	}
	var exp time.Time
	err = f.st.WithTx(f.ctx, f.admin(), func(ctx context.Context, tx *store.Tx) error {
		return tx.QueryRow(ctx, `select expires_at from session where org_user_id = $1`, f.userID).Scan(&exp)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !exp.After(old.Add(time.Hour)) {
		t.Fatalf("expiry did not slide: %v", exp)
	}
}

func TestPasswordReset(t *testing.T) {
	f := newFixture(t)
	tok, err := f.auth.RequestPasswordReset(f.ctx, service.SurfaceApp, f.email)
	if err != nil || tok == "" {
		t.Fatalf("request reset: %q %v", tok, err)
	}
	if tok2, err := f.auth.RequestPasswordReset(f.ctx, service.SurfaceApp, "nobody@example.com"); err != nil || tok2 != "" {
		t.Fatalf("unknown email should yield no token silently: %q %v", tok2, err)
	}
	if err := f.auth.ResetPassword(f.ctx, "bogus", "new-password-123"); !errors.Is(err, service.ErrResetInvalid) {
		t.Fatalf("bogus token: %v", err)
	}
	live, err := f.auth.Login(f.ctx, service.SurfaceApp, f.email, f.password, "")
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.auth.RequestPasswordReset(f.ctx, service.SurfaceApp, f.email)
	if err != nil || other == "" {
		t.Fatal(err)
	}
	if err := f.auth.ResetPassword(f.ctx, tok, "new-password-123"); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if _, err := f.auth.ResolveSession(f.ctx, live.Token); !errors.Is(err, service.ErrNoSession) {
		t.Fatalf("session survived password reset: %v", err)
	}
	if err := f.auth.ResetPassword(f.ctx, other, "another-password-123"); !errors.Is(err, service.ErrResetInvalid) {
		t.Fatalf("other outstanding reset token still valid: %v", err)
	}
	if err := f.auth.ResetPassword(f.ctx, tok, "again-password-123"); !errors.Is(err, service.ErrResetInvalid) {
		t.Fatalf("reset token reusable: %v", err)
	}
	if _, err := f.auth.Login(f.ctx, service.SurfaceApp, f.email, f.password, ""); !errors.Is(err, service.ErrInvalidCredentials) {
		t.Fatalf("old password still works: %v", err)
	}
	if _, err := f.auth.Login(f.ctx, service.SurfaceApp, f.email, "new-password-123", ""); err != nil {
		t.Fatalf("new password: %v", err)
	}
}

func TestMagicLinks(t *testing.T) {
	f := newFixture(t)
	subject := uuid.New()
	apply, _, err := f.links.Issue(f.ctx, f.admin(), service.LinkApply, subject, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.links.Resolve(f.ctx, apply, service.LinkBook); !errors.Is(err, service.ErrLinkPurpose) {
		t.Fatalf("purpose mismatch: %v", err)
	}
	p, err := f.links.Consume(f.ctx, apply, service.LinkApply)
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != service.PrincipalMagicLink || p.OrgID != f.orgID || p.SubjectID != subject || p.MagicPurpose != service.LinkApply {
		t.Fatalf("bad principal: %+v", p)
	}
	if _, err := f.links.Consume(f.ctx, apply, service.LinkApply); !errors.Is(err, service.ErrLinkUsed) {
		t.Fatalf("apply reuse: %v", err)
	}
	if _, err := f.links.Resolve(f.ctx, apply, service.LinkApply); !errors.Is(err, service.ErrLinkUsed) {
		t.Fatalf("apply reuse via resolve: %v", err)
	}

	book, bookID, err := f.links.Issue(f.ctx, f.admin(), service.LinkBook, subject, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := f.links.Consume(f.ctx, book, service.LinkBook); err != nil {
			t.Fatalf("book reuse %d: %v", i, err)
		}
	}
	if err := f.links.Revoke(f.ctx, f.admin(), bookID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.links.Resolve(f.ctx, book, service.LinkBook); !errors.Is(err, service.ErrLinkRevoked) {
		t.Fatalf("revoked: %v", err)
	}

	expired, _, err := f.links.Issue(f.ctx, f.admin(), service.LinkAssessment, subject, -time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.links.Resolve(f.ctx, expired, service.LinkAssessment); !errors.Is(err, service.ErrLinkExpired) {
		t.Fatalf("expired: %v", err)
	}
	if _, err := f.links.Resolve(f.ctx, "not-a-token", service.LinkAssessment); !errors.Is(err, service.ErrLinkInvalid) {
		t.Fatalf("unknown: %v", err)
	}
}

// lockSchema shares the cross-package advisory lock that guards the schema
// while the store package's migration round-trip test rebuilds it.
func lockSchema(t *testing.T, ownerURL string) {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), ownerURL)
	if err != nil {
		t.Fatalf("schema lock connect: %v", err)
	}
	if _, err := conn.Exec(context.Background(), "select pg_advisory_lock_shared($1)", 7371); err != nil {
		t.Fatalf("schema lock: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
}
