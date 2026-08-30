//go:build integration

package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"recruiting/internal/api"
	"recruiting/internal/service"
	"recruiting/internal/store"
)

// apiFixture is a live API over the real database: one org with an admin and
// a client user, and the token service both authenticate through.
type apiFixture struct {
	ctx      context.Context
	st       *store.Store
	sys      *pgxpool.Pool
	tokens   *service.APITokenService
	srv      *httptest.Server
	orgID    uuid.UUID
	adminID  uuid.UUID
	clientID uuid.UUID
	company  uuid.UUID
}

func lockSchemaShared(t *testing.T, ownerURL string) {
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

func newAPIFixture(t *testing.T) *apiFixture {
	t.Helper()
	ownerURL := os.Getenv("DATABASE_URL")
	if ownerURL == "" {
		t.Fatal("DATABASE_URL is not set; run `make dev-up` and use `make test-integration`")
	}
	lockSchemaShared(t, ownerURL)
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

	f := &apiFixture{
		ctx: ctx, st: st, sys: sys,
		orgID: uuid.New(), adminID: uuid.New(), clientID: uuid.New(), company: uuid.New(),
	}
	f.seedOrg(t, f.orgID, f.adminID)
	exec(t, sys, ctx, `insert into client_company (id, org_id, name) values ($1, $2, 'Client Co')`, f.company, f.orgID)
	exec(t, sys, ctx, `insert into client_user (id, org_id, client_company_id, email, name) values ($1, $2, $3, $4, 'Client User')`,
		f.clientID, f.orgID, f.company, "client-"+f.clientID.String()+"@example.com")

	f.tokens = service.NewAPITokenService(st)
	r := api.NewRouter()
	api.MountAll(r, api.Deps{
		Sessions:  service.NewAuthService(st),
		Tokens:    f.tokens,
		APITokens: f.tokens,
		Jobs:      service.NewJobService(st),
		Portal:    service.NewClientPortalService(st, nil, nil, nil, ""),
	})
	f.srv = httptest.NewServer(r.Mux)
	t.Cleanup(f.srv.Close)
	return f
}

// seedOrg inserts an org with one admin user.
func (f *apiFixture) seedOrg(t *testing.T, orgID, userID uuid.UUID) {
	t.Helper()
	exec(t, f.sys, f.ctx, `insert into org (id, name, slug) values ($1, $2, $2)`, orgID, orgID.String())
	t.Cleanup(func() { _, _ = f.sys.Exec(context.Background(), `delete from org where id = $1`, orgID) })
	exec(t, f.sys, f.ctx, `insert into org_user (id, org_id, email, name) values ($1, $2, $3, 'Admin')`,
		userID, orgID, "admin-"+userID.String()+"@example.com")
	exec(t, f.sys, f.ctx, `insert into org_user_role (org_user_id, org_id, role) values ($1, $2, 'admin')`, userID, orgID)
}

func exec(t *testing.T, pool *pgxpool.Pool, ctx context.Context, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func (f *apiFixture) admin() service.Principal {
	return service.Principal{Kind: service.PrincipalOrgUser, OrgID: f.orgID, UserID: f.adminID, Roles: []string{"admin"}}
}

// call makes a request with the bearer token and returns its status.
func (f *apiFixture) call(t *testing.T, method, path, token string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(f.ctx, method, f.srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := f.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, body
}

func TestClientUserTokenResolvesToItsCompany(t *testing.T) {
	f := newAPIFixture(t)
	_, raw, err := f.tokens.Issue(f.ctx, f.admin(), service.NewAPIToken{
		Name: "portal", UserID: f.clientID, ClientUser: true,
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	p, err := f.tokens.ResolveToken(f.ctx, raw)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if p.Kind != service.PrincipalClientUser {
		t.Fatalf("kind = %v, want client user", p.Kind)
	}
	if p.OrgID != f.orgID || p.UserID != f.clientID || p.ClientCompanyID != f.company {
		t.Fatalf("principal = %+v", p)
	}
}

func TestBearerTokensReachOnlyTheirOwnSurface(t *testing.T) {
	f := newAPIFixture(t)
	_, clientToken, err := f.tokens.Issue(f.ctx, f.admin(), service.NewAPIToken{Name: "portal", UserID: f.clientID, ClientUser: true})
	if err != nil {
		t.Fatalf("issue client token: %v", err)
	}
	_, orgToken, err := f.tokens.Issue(f.ctx, f.admin(), service.NewAPIToken{Name: "ci", UserID: f.adminID})
	if err != nil {
		t.Fatalf("issue org token: %v", err)
	}

	// The org token reaches the org surface and not the portal.
	if got, _ := f.call(t, http.MethodGet, "/api/v1/jobs", orgToken); got != http.StatusOK {
		t.Errorf("org token on /jobs: status = %d, want 200", got)
	}
	if got, _ := f.call(t, http.MethodGet, "/api/v1/portal/me", orgToken); got != http.StatusForbidden {
		t.Errorf("org token on /portal/me: status = %d, want 403", got)
	}

	// The client token reaches the portal read and nothing else.
	status, body := f.call(t, http.MethodGet, "/api/v1/portal/me", clientToken)
	if status != http.StatusOK {
		t.Fatalf("client token on /portal/me: status = %d, body %s", status, body)
	}
	var me struct {
		ClientCompanyID uuid.UUID `json:"client_company_id"`
	}
	if err := json.Unmarshal(body, &me); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if me.ClientCompanyID != f.company {
		t.Errorf("portal me company = %v, want %v", me.ClientCompanyID, f.company)
	}
	for _, path := range []string{"/api/v1/jobs", "/api/v1/pool", "/api/v1/api-tokens"} {
		if got, _ := f.call(t, http.MethodGet, path, clientToken); got != http.StatusForbidden {
			t.Errorf("client token on %s: status = %d, want 403", path, got)
		}
	}

	// No credential at all is refused before any service is reached.
	if got, _ := f.call(t, http.MethodGet, "/api/v1/jobs", ""); got != http.StatusUnauthorized {
		t.Errorf("no credential: status = %d, want 401", got)
	}
}

func TestRevokedTokenStopsWorkingOverHTTP(t *testing.T) {
	f := newAPIFixture(t)
	tok, raw, err := f.tokens.Issue(f.ctx, f.admin(), service.NewAPIToken{Name: "ci", UserID: f.adminID})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if got, _ := f.call(t, http.MethodGet, "/api/v1/jobs", raw); got != http.StatusOK {
		t.Fatalf("live token: status = %d, want 200", got)
	}
	if err := f.tokens.Revoke(f.ctx, f.admin(), tok.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if got, _ := f.call(t, http.MethodGet, "/api/v1/jobs", raw); got != http.StatusUnauthorized {
		t.Fatalf("revoked token: status = %d, want 401", got)
	}
}

func TestAnAdminCannotRevokeAnotherOrgsToken(t *testing.T) {
	f := newAPIFixture(t)
	tok, raw, err := f.tokens.Issue(f.ctx, f.admin(), service.NewAPIToken{Name: "ci", UserID: f.adminID})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	otherOrg, otherAdmin := uuid.New(), uuid.New()
	f.seedOrg(t, otherOrg, otherAdmin)
	stranger := service.Principal{Kind: service.PrincipalOrgUser, OrgID: otherOrg, UserID: otherAdmin, Roles: []string{"admin"}}

	if err := f.tokens.Revoke(f.ctx, stranger, tok.ID); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("cross-org revoke: %v, want not found", err)
	}
	list, err := f.tokens.List(f.ctx, stranger)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("another org's tokens are visible: %+v", list)
	}
	// The token still works, which is what the failed revoke must mean.
	if got, _ := f.call(t, http.MethodGet, "/api/v1/jobs", raw); got != http.StatusOK {
		t.Fatalf("token after a refused cross-org revoke: status = %d, want 200", got)
	}
}
