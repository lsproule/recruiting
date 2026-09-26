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
	"strings"
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
		Talent:    service.NewTalentService(st, nil, nil, nil, ""),
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

// The try endpoint runs code, so it is an org-user operation: a client
// credential is refused before any problem is loaded.
func TestTryEndpointRefusesAClientCredential(t *testing.T) {
	f := newAPIFixture(t)
	_, clientToken, err := f.tokens.Issue(f.ctx, f.admin(), service.NewAPIToken{Name: "portal", UserID: f.clientID, ClientUser: true})
	if err != nil {
		t.Fatalf("issue client token: %v", err)
	}
	path := "/api/v1/problems/" + uuid.New().String() + "/try"
	if got, body := f.call(t, http.MethodPost, path, clientToken); got != http.StatusForbidden {
		t.Errorf("client token on %s: status = %d, want 403; body %s", path, got, body)
	}
	if got, _ := f.call(t, http.MethodPost, path, ""); got != http.StatusUnauthorized {
		t.Errorf("no credential on %s: status = %d, want 401", path, got)
	}
}

// postJSON makes a request with a JSON body and the bearer token.
func (f *apiFixture) postJSON(t *testing.T, method, path, token, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(f.ctx, method, f.srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

// TestCompanySurfaceIssuesTokensAndReadsItsFeed walks the integration path a
// company follows: one token issued by the admin bootstraps the rest.
func TestCompanySurfaceIssuesTokensAndReadsItsFeed(t *testing.T) {
	f := newAPIFixture(t)
	_, first, err := f.tokens.Issue(f.ctx, f.admin(), service.NewAPIToken{Name: "bootstrap", UserID: f.clientID, ClientUser: true})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	status, body := f.postJSON(t, http.MethodPost, "/api/v1/portal/tokens", first, `{"name":"ats sync"}`)
	if status != http.StatusCreated {
		t.Fatalf("create own token: %d %s", status, body)
	}
	var created struct {
		Token  struct{ ID uuid.UUID } `json:"token"`
		Secret string                 `json:"secret"`
	}
	if err := json.Unmarshal(body, &created); err != nil || created.Secret == "" {
		t.Fatalf("decode: %v %s", err, body)
	}
	// The new token reaches the company surface like the first.
	if got, b := f.call(t, http.MethodGet, "/api/v1/portal/tokens", created.Secret); got != http.StatusOK || !strings.Contains(string(b), "ats sync") {
		t.Fatalf("list own tokens: %d %s", got, b)
	}
	if got, _ := f.call(t, http.MethodGet, "/api/v1/jobs", created.Secret); got != http.StatusForbidden {
		t.Fatalf("company token on the org surface: %d, want 403", got)
	}
	status, body = f.call(t, http.MethodGet, "/api/v1/portal/events?since=0", created.Secret)
	if status != http.StatusOK {
		t.Fatalf("events: %d %s", status, body)
	}
	var feed struct {
		Events    []json.RawMessage `json:"events"`
		NextSince int64             `json:"next_since"`
	}
	if err := json.Unmarshal(body, &feed); err != nil || len(feed.Events) != 0 || feed.NextSince != 0 {
		t.Fatalf("empty feed = %s (%v)", body, err)
	}
	if status, body = f.call(t, http.MethodGet, "/api/v1/portal/applications?limit=5", created.Secret); status != http.StatusOK || !strings.Contains(string(body), `"applications":[]`) {
		t.Fatalf("applications: %d %s", status, body)
	}
	// Revoking through the API stops the token at once; the first still works.
	if got, _ := f.call(t, http.MethodDelete, "/api/v1/portal/tokens/"+created.Token.ID.String(), first); got != http.StatusNoContent {
		t.Fatalf("revoke own: %d", got)
	}
	if got, _ := f.call(t, http.MethodGet, "/api/v1/portal/me", created.Secret); got != http.StatusUnauthorized {
		t.Fatalf("revoked token: %d, want 401", got)
	}
	if got, _ := f.call(t, http.MethodDelete, "/api/v1/portal/tokens/"+created.Token.ID.String(), first); got != http.StatusNotFound {
		t.Fatalf("revoking twice: %d, want 404", got)
	}
	// The org's admin token cannot use the company's own token operations.
	_, orgToken, _ := f.tokens.Issue(f.ctx, f.admin(), service.NewAPIToken{Name: "ci", UserID: f.adminID})
	if got, _ := f.postJSON(t, http.MethodPost, "/api/v1/portal/tokens", orgToken, `{"name":"x"}`); got != http.StatusForbidden {
		t.Fatalf("org token issuing a portal token: %d, want 403", got)
	}
}

// TestCompanyFilesATalentRequestAndReadsMatches covers the talent operations
// over HTTP: a request with a bad vocabulary is refused with a 422, a good
// one is created, and the matches read is anonymised and empty for a fresh
// org.
func TestCompanyFilesATalentRequestAndReadsMatches(t *testing.T) {
	f := newAPIFixture(t)
	_, token, err := f.tokens.Issue(f.ctx, f.admin(), service.NewAPIToken{Name: "portal", UserID: f.clientID, ClientUser: true})
	if err != nil {
		t.Fatal(err)
	}
	if got, body := f.postJSON(t, http.MethodPost, "/api/v1/portal/talent-requests", token, `{"title":"x","skills":["go"],"seniority":"wizard"}`); got != http.StatusUnprocessableEntity {
		t.Fatalf("bad seniority: %d %s", got, body)
	}
	status, body := f.postJSON(t, http.MethodPost, "/api/v1/portal/talent-requests", token, `{"title":"Senior Go engineer","skills":["go","postgres"],"remote_policy":"remote"}`)
	if status != http.StatusCreated {
		t.Fatalf("create: %d %s", status, body)
	}
	var created struct {
		Request struct {
			ID     uuid.UUID `json:"id"`
			Status string    `json:"status"`
			Skills []string  `json:"skills"`
		} `json:"request"`
	}
	if err := json.Unmarshal(body, &created); err != nil || created.Request.Status != "open" || len(created.Request.Skills) != 2 {
		t.Fatalf("created = %s (%v)", body, err)
	}
	path := "/api/v1/portal/talent-requests/" + created.Request.ID.String()
	if got, b := f.call(t, http.MethodGet, path+"/matches", token); got != http.StatusOK || !strings.Contains(string(b), `"matches":[]`) {
		t.Fatalf("matches: %d %s", got, b)
	}
	if got, _ := f.postJSON(t, http.MethodPost, path+"/matches/"+uuid.New().String()+"/introduce", token, ""); got != http.StatusNotFound {
		t.Fatalf("introduce to nobody: %d, want 404", got)
	}
	if got, _ := f.postJSON(t, http.MethodPost, path+"/close", token, ""); got != http.StatusNoContent {
		t.Fatalf("close: %d", got)
	}
	if got, _ := f.postJSON(t, http.MethodPost, path+"/close", token, ""); got != http.StatusConflict {
		t.Fatalf("close twice: %d, want 409", got)
	}
	// The recruiter's side lists it; a company token cannot.
	_, orgToken, _ := f.tokens.Issue(f.ctx, f.admin(), service.NewAPIToken{Name: "ci", UserID: f.adminID})
	if got, b := f.call(t, http.MethodGet, "/api/v1/talent-requests", orgToken); got != http.StatusOK || !strings.Contains(string(b), "Senior Go engineer") {
		t.Fatalf("recruiter list: %d %s", got, b)
	}
	if got, _ := f.call(t, http.MethodGet, "/api/v1/talent-requests", token); got != http.StatusForbidden {
		t.Fatalf("company token on the recruiter list: %d, want 403", got)
	}
}
