package middleware_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	"recruiting/internal/service"
	"recruiting/internal/web/middleware"
)

type fakeResolver map[string]service.Principal

func (f fakeResolver) ResolveSession(_ context.Context, token string) (service.Principal, error) {
	p, ok := f[token]
	if !ok {
		return service.Principal{}, service.ErrNoSession
	}
	return p, nil
}

func echoKind() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := middleware.PrincipalFrom(r.Context())
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(p.OrgID.String()))
	})
}

func TestAuthenticateRejectsWrongSurfaceCookie(t *testing.T) {
	org := uuid.New()
	res := fakeResolver{
		"orgtok":    {Kind: service.PrincipalOrgUser, OrgID: org, UserID: uuid.New()},
		"clienttok": {Kind: service.PrincipalClientUser, OrgID: org, UserID: uuid.New(), ClientCompanyID: uuid.New()},
	}
	h := middleware.Authenticate(res)(echoKind())
	cases := []struct {
		path, token string
		want        int
	}{
		{"/app/jobs", "orgtok", http.StatusOK},
		{"/client/", "clienttok", http.StatusOK},
		{"/app/jobs", "clienttok", http.StatusForbidden},
		{"/client/", "orgtok", http.StatusForbidden},
		{"/app/jobs", "", http.StatusUnauthorized},
		{"/app/jobs", "bogus", http.StatusUnauthorized},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, c.path, nil)
		if c.token != "" {
			req.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: c.token})
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s with %q: got %d want %d", c.path, c.token, rec.Code, c.want)
		}
		if c.want == http.StatusOK && rec.Body.String() != org.String() {
			t.Errorf("principal not in context: %q", rec.Body.String())
		}
	}
}

func TestRequireAuthRedirectsAnonymous(t *testing.T) {
	h := middleware.RequireAuth("/app/login")(echoKind())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/app/jobs", nil))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/app/login" {
		t.Fatalf("got %d %q", rec.Code, rec.Header().Get("Location"))
	}
	req := httptest.NewRequest(http.MethodGet, "/app/jobs", nil)
	req = req.WithContext(middleware.WithPrincipal(req.Context(), service.Principal{Kind: service.PrincipalOrgUser, OrgID: uuid.New()}))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authenticated got %d", rec.Code)
	}
}

func TestCSRF(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(middleware.CSRFToken(r)))
	})
	h := middleware.CSRF(false)(ok)

	// GET issues the cookie and exposes the token to the page.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/app/login", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET got %d", rec.Code)
	}
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == middleware.CSRFCookie {
			cookie = c
		}
	}
	if cookie == nil || cookie.Value == "" {
		t.Fatal("GET did not set csrf cookie")
	}
	if rec.Body.String() != cookie.Value {
		t.Fatalf("CSRFToken %q != cookie %q", rec.Body.String(), cookie.Value)
	}

	post := func(cookieVal, field, header string) int {
		form := url.Values{}
		if field != "" {
			form.Set(middleware.CSRFField, field)
		}
		req := httptest.NewRequest(http.MethodPost, "/app/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if cookieVal != "" {
			req.AddCookie(&http.Cookie{Name: middleware.CSRFCookie, Value: cookieVal})
		}
		if header != "" {
			req.Header.Set(middleware.CSRFHeader, header)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	tok := cookie.Value
	if got := post(tok, "", ""); got != http.StatusForbidden {
		t.Errorf("missing token: got %d", got)
	}
	if got := post("", tok, ""); got != http.StatusForbidden {
		t.Errorf("missing cookie: got %d", got)
	}
	if got := post(tok, "other", ""); got != http.StatusForbidden {
		t.Errorf("mismatch: got %d", got)
	}
	if got := post(tok, tok, ""); got != http.StatusOK {
		t.Errorf("form token: got %d", got)
	}
	if got := post(tok, "", tok); got != http.StatusOK {
		t.Errorf("header token (htmx): got %d", got)
	}
}

var _ = errors.New

func TestCSRFSecureUsesHostCookie(t *testing.T) {
	h := middleware.CSRF(true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/app/login", nil))
	cs := rec.Result().Cookies()
	if len(cs) != 1 || cs[0].Name != middleware.CSRFCookieSecure || !cs[0].Secure || cs[0].Path != "/" || cs[0].Domain != "" {
		t.Fatalf("unexpected cookies: %+v", cs)
	}
}
