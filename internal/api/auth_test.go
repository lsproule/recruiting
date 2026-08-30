package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"recruiting/internal/api"
	"recruiting/internal/service"
	"recruiting/internal/web/middleware"
)

// stubTokens answers the two bearer tokens the scoping test uses.
type stubTokens struct{}

func (stubTokens) ResolveToken(_ context.Context, raw string) (service.Principal, error) {
	switch raw {
	case "org":
		return service.Principal{Kind: service.PrincipalOrgUser, OrgID: uuid.New(), UserID: uuid.New(), Roles: []string{"recruiter"}}, nil
	case "client":
		return service.Principal{Kind: service.PrincipalClientUser, OrgID: uuid.New(), UserID: uuid.New(), ClientCompanyID: uuid.New()}, nil
	}
	return service.Principal{}, service.ErrNoSession
}

// stubSessions refuses every cookie, so the bearer path is what answers.
type stubSessions struct{}

func (stubSessions) ResolveSession(context.Context, string) (service.Principal, error) {
	return service.Principal{}, service.ErrNoSession
}

func scopingServer(t *testing.T) *httptest.Server {
	t.Helper()
	r := api.NewRouter()
	api.MountAll(r, api.Deps{Sessions: stubSessions{}, Tokens: stubTokens{}})
	srv := httptest.NewServer(r.Mux)
	t.Cleanup(srv.Close)
	return srv
}

func do(t *testing.T, srv *httptest.Server, path, bearer string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

func TestOperationsRefuseAnUnauthenticatedRequest(t *testing.T) {
	srv := scopingServer(t)
	for _, path := range []string{"/api/v1/jobs", "/api/v1/portal/jobs", "/api/v1/api-tokens"} {
		if got := do(t, srv, path, ""); got != http.StatusUnauthorized {
			t.Errorf("%s without a credential: status = %d, want 401", path, got)
		}
	}
	if got := do(t, srv, "/api/v1/jobs", "nonsense"); got != http.StatusUnauthorized {
		t.Errorf("unknown bearer token: status = %d, want 401", got)
	}
}

func TestClientTokensReachOnlyThePortalReads(t *testing.T) {
	srv := scopingServer(t)
	for _, path := range []string{"/api/v1/jobs", "/api/v1/pool", "/api/v1/problems", "/api/v1/api-tokens"} {
		if got := do(t, srv, path, "client"); got != http.StatusForbidden {
			t.Errorf("client token on %s: status = %d, want 403", path, got)
		}
	}
}

func TestOrgTokensDoNotReachThePortal(t *testing.T) {
	srv := scopingServer(t)
	for _, path := range []string{"/api/v1/portal/jobs", "/api/v1/portal/me"} {
		if got := do(t, srv, path, "org"); got != http.StatusForbidden {
			t.Errorf("org token on %s: status = %d, want 403", path, got)
		}
	}
}

func TestAdminOnlyOperationsRefuseANonAdmin(t *testing.T) {
	srv := scopingServer(t)
	if got := do(t, srv, "/api/v1/api-tokens", "org"); got != http.StatusForbidden {
		t.Errorf("recruiter token on /api-tokens: status = %d, want 403", got)
	}
}

// TestAnUnguardedOperationIsRefused pins the default-deny rule: an operation
// registered without going through the guard is answered 401, not served.
func TestAnUnguardedOperationIsRefused(t *testing.T) {
	r := api.NewRouter()
	api.MountAll(r, api.Deps{Sessions: stubSessions{}, Tokens: stubTokens{}})
	huma.Register(r.API, huma.Operation{
		OperationID: "forgotten-operation", Method: http.MethodGet, Path: "/forgotten",
	}, func(context.Context, *struct{}) (*struct{}, error) {
		t.Error("an unguarded operation reached its handler")
		return nil, nil
	})
	srv := httptest.NewServer(r.Mux)
	t.Cleanup(srv.Close)

	if got := do(t, srv, "/api/v1/forgotten", "org"); got != http.StatusUnauthorized {
		t.Errorf("unguarded operation: status = %d, want 401", got)
	}
}

// cookieSessions resolves the one session cookie the CSRF test sets, as an
// admin so the operation's access check is not what refuses it.
type cookieSessions struct{}

func (cookieSessions) ResolveSession(_ context.Context, token string) (service.Principal, error) {
	if token == "sess" {
		return service.Principal{Kind: service.PrincipalOrgUser, OrgID: uuid.New(), UserID: uuid.New(), Roles: []string{"admin"}}, nil
	}
	return service.Principal{}, service.ErrNoSession
}

func TestASessionCookieNeedsTheCSRFTokenOnUnsafeRequests(t *testing.T) {
	r := api.NewRouter()
	api.MountAll(r, api.Deps{Sessions: cookieSessions{}, Tokens: stubTokens{}})
	srv := httptest.NewServer(r.Mux)
	t.Cleanup(srv.Close)

	post := func(withHeader bool) int {
		t.Helper()
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/api/v1/problems/import", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "sess"})
		req.AddCookie(&http.Cookie{Name: middleware.CSRFCookie, Value: "tok"})
		if withHeader {
			req.Header.Set(middleware.CSRFHeader, "tok")
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode
	}
	if got := post(false); got != http.StatusForbidden {
		t.Errorf("cookie-only POST = %d, want 403", got)
	}
	if got := post(true); got == http.StatusForbidden || got == http.StatusUnauthorized {
		t.Errorf("POST with the CSRF header = %d, want it past authentication", got)
	}
	// A safe request needs no token.
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/api/v1/jobs", nil)
	req.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "sess"})
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
		t.Errorf("cookie-only GET = %d, want it past authentication", resp.StatusCode)
	}
}
