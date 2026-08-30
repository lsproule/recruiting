//go:build integration

package auth_test

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"recruiting/internal/service"
	"recruiting/internal/store"
	"recruiting/internal/web/auth"
	"recruiting/internal/web/middleware"
)

type webFixture struct {
	srv      *httptest.Server
	client   *http.Client
	email    string
	password string
	orgID    uuid.UUID
	links    *service.MagicLinkService
	auth     *auth.Auth
	admin    service.Principal
	resetURL chan string
}

func newWebFixture(t *testing.T) *webFixture {
	t.Helper()
	ownerURL := os.Getenv("DATABASE_URL")
	if ownerURL == "" {
		t.Fatal("DATABASE_URL is not set; run `make dev-up` and use `make test-integration`")
	}
	lockSchema(t, ownerURL)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	if err := store.MigrateUp(ctx, ownerURL); err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
	}
	t.Cleanup(st.Close)

	f := &webFixture{orgID: uuid.New(), password: "hunter2-long-enough", resetURL: make(chan string, 1)}
	userID := uuid.New()
	f.email = f.orgID.String() + "@example.com"
	hash, _ := service.HashPassword(f.password)
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`insert into org (id, name, slug) values ($1, $2, $2)`, []any{f.orgID, f.orgID.String()}},
		{`insert into org_user (id, org_id, email, name) values ($1, $2, $3, 'Admin')`, []any{userID, f.orgID, f.email}},
		{`insert into org_user_credential (org_user_id, org_id, password_hash) values ($1, $2, $3)`, []any{userID, f.orgID, hash}},
	} {
		if _, err := sys.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _, _ = sys.Exec(context.Background(), `delete from org where id = $1`, f.orgID) })
	f.admin = service.Principal{Kind: service.PrincipalOrgUser, OrgID: f.orgID, UserID: userID, Roles: []string{"admin"}}
	f.links = service.NewMagicLinkService(st)

	mux := chi.NewMux()
	f.auth, err = auth.Mount(mux, auth.Deps{
		Auth:  service.NewAuthService(st),
		Links: f.links,
		SendPasswordReset: func(_ context.Context, _ string, link string) error {
			f.resetURL <- link
			return nil
		},
		CookieSecret: []byte("test-secret"),
	})
	if err != nil {
		t.Fatal(err)
	}
	// A protected page on each surface, as later tasks will register them.
	mux.With(middleware.RequireAuth("/app/login")).Get("/app/home", func(w http.ResponseWriter, r *http.Request) {
		p, _ := middleware.PrincipalFrom(r.Context())
		_, _ = w.Write([]byte("app home " + p.UserID.String()))
	})
	mux.With(middleware.RequireAuth("/client/login")).Get("/client/home", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("client home"))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	jar, _ := cookiejar.New(nil)
	f.client = &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return f
}

func (f *webFixture) get(t *testing.T, path string) (*http.Response, string) {
	t.Helper()
	res, err := f.client.Get(f.srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

func (f *webFixture) post(t *testing.T, path string, form url.Values) (*http.Response, string) {
	t.Helper()
	res, err := f.client.PostForm(f.srv.URL+path, form)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

func (f *webFixture) csrf(t *testing.T) string {
	t.Helper()
	for _, c := range f.client.Jar.Cookies(mustURL(f.srv.URL)) {
		if c.Name == middleware.CSRFCookie {
			return c.Value
		}
	}
	t.Fatal("no csrf cookie")
	return ""
}

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

func TestLoginLogoutFlow(t *testing.T) {
	f := newWebFixture(t)
	res, body := f.get(t, "/app/login")
	if res.StatusCode != 200 || !strings.Contains(body, `name="`+middleware.CSRFField+`"`) {
		t.Fatalf("login page: %d %s", res.StatusCode, body)
	}
	tok := f.csrf(t)

	// Missing CSRF token → 403.
	res, _ = f.post(t, "/app/login", url.Values{"email": {f.email}, "password": {f.password}})
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("missing csrf: %d", res.StatusCode)
	}
	// Wrong password re-renders with an error.
	res, body = f.post(t, "/app/login", url.Values{middleware.CSRFField: {tok}, "email": {f.email}, "password": {"wrong"}})
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "Invalid email or password") {
		t.Fatalf("wrong password: %d %s", res.StatusCode, body)
	}
	res, _ = f.post(t, "/app/login", url.Values{middleware.CSRFField: {tok}, "email": {f.email}, "password": {f.password}})
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/app/" {
		t.Fatalf("login: %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	res, body = f.get(t, "/app/home")
	if res.StatusCode != 200 || !strings.HasPrefix(body, "app home ") {
		t.Fatalf("app home: %d %s", res.StatusCode, body)
	}
	// Org session on the client surface is rejected outright.
	res, _ = f.get(t, "/client/home")
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("org cookie on /client: %d", res.StatusCode)
	}
	res, _ = f.post(t, "/app/logout", url.Values{middleware.CSRFField: {f.csrf(t)}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("logout: %d", res.StatusCode)
	}
	res, _ = f.get(t, "/app/home")
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/app/login" {
		t.Fatalf("after logout: %d %q", res.StatusCode, res.Header.Get("Location"))
	}
}

func TestPasswordResetFlow(t *testing.T) {
	f := newWebFixture(t)
	f.get(t, "/app/reset")
	res, _ := f.post(t, "/app/reset", url.Values{middleware.CSRFField: {f.csrf(t)}, "email": {f.email}})
	if res.StatusCode != 200 {
		t.Fatalf("request reset: %d", res.StatusCode)
	}
	var link string
	select {
	case link = <-f.resetURL:
	case <-time.After(time.Second):
		t.Fatal("no reset link sent")
	}
	path := strings.TrimPrefix(link, f.srv.URL)
	if !strings.HasPrefix(path, "/app/reset/") {
		t.Fatalf("unexpected reset link %q", link)
	}
	res, body := f.get(t, path)
	if res.StatusCode != 200 || !strings.Contains(body, "New password") {
		t.Fatalf("reset form: %d %s", res.StatusCode, body)
	}
	res, _ = f.post(t, path, url.Values{middleware.CSRFField: {f.csrf(t)}, "password": {"brand-new-password"}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("reset: %d", res.StatusCode)
	}
	res, body = f.get(t, path)
	if res.StatusCode != http.StatusGone || !strings.Contains(body, "no longer valid") {
		t.Fatalf("reused reset link: %d %s", res.StatusCode, body)
	}
	f.get(t, "/app/login")
	res, _ = f.post(t, "/app/login", url.Values{middleware.CSRFField: {f.csrf(t)}, "email": {f.email}, "password": {"brand-new-password"}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("login with new password: %d", res.StatusCode)
	}
}

func TestMagicLinkPages(t *testing.T) {
	f := newWebFixture(t)
	ctx := context.Background()
	subject := uuid.New()
	mux := f.srv.Config.Handler.(*chi.Mux)
	mux.With(auth.MagicLink(f.links, service.LinkBook)).Get("/book/{token}", func(w http.ResponseWriter, r *http.Request) {
		p, _ := middleware.PrincipalFrom(r.Context())
		_, _ = w.Write([]byte("book " + p.SubjectID.String()))
	})
	mux.With(auth.MagicLink(f.links, service.LinkApply)).Get("/apply/{token}", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("apply ok"))
	})
	mux.With(auth.MagicLink(f.links, service.LinkApply)).Post("/apply/{token}", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("applied"))
	})

	// apply: GET only resolves (mail scanners prefetch); POST consumes once.
	apply, _, err := f.links.Issue(ctx, f.admin, service.LinkApply, subject, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if res, body := f.get(t, "/apply/"+apply); res.StatusCode != 200 || body != "apply ok" {
			t.Fatalf("apply GET %d: %d %s", i, res.StatusCode, body)
		}
	}
	if res, body := f.post(t, "/apply/"+apply, url.Values{middleware.CSRFField: {f.csrf(t)}}); res.StatusCode != 200 || body != "applied" {
		t.Fatalf("apply POST: %d %s", res.StatusCode, body)
	}
	if res, body := f.get(t, "/apply/"+apply); res.StatusCode != http.StatusGone || !strings.Contains(body, "already been used") {
		t.Fatalf("apply after consume: %d %s", res.StatusCode, body)
	}

	book, _, err := f.links.Issue(ctx, f.admin, service.LinkBook, subject, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		res, body := f.get(t, "/book/"+book)
		if res.StatusCode != 200 || body != "book "+subject.String() {
			t.Fatalf("book %d: %d %s", i, res.StatusCode, body)
		}
	}
	// Wrong purpose → error page, not a principal.
	res, body := f.get(t, "/apply/"+book)
	if res.StatusCode != http.StatusGone || !strings.Contains(body, "link") {
		t.Fatalf("purpose mismatch: %d %s", res.StatusCode, body)
	}

	expired, _, _ := f.links.Issue(ctx, f.admin, service.LinkBook, subject, -time.Minute)
	res, body = f.get(t, "/book/"+expired)
	if res.StatusCode != http.StatusGone || !strings.Contains(body, "expired") {
		t.Fatalf("expired: %d %s", res.StatusCode, body)
	}
	revoked, id, _ := f.links.Issue(ctx, f.admin, service.LinkBook, subject, time.Hour)
	_ = f.links.Revoke(ctx, f.admin, id)
	res, body = f.get(t, "/book/"+revoked)
	if res.StatusCode != http.StatusGone || !strings.Contains(body, "revoked") {
		t.Fatalf("revoked: %d %s", res.StatusCode, body)
	}

	// Assessment links swap the token for a short-lived cookie.
	assess, _, _ := f.links.Issue(ctx, f.admin, service.LinkAssessment, subject, time.Hour)
	res, _ = f.get(t, "/assess/"+assess)
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/assess/" {
		t.Fatalf("assessment entry: %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	var seen bool
	for _, c := range f.client.Jar.Cookies(mustURL(f.srv.URL + "/assess/")) {
		if c.Name == auth.AssessmentCookie {
			seen = true
			if c.Value == assess {
				t.Fatal("assessment cookie stores the raw link token")
			}
		}
	}
	if !seen {
		t.Fatal("no assessment cookie set")
	}
	mux.With(f.auth.Assessment()).Get("/assess/", func(w http.ResponseWriter, r *http.Request) {
		p, _ := middleware.PrincipalFrom(r.Context())
		_, _ = w.Write([]byte("assess " + p.SubjectID.String()))
	})
	res, body = f.get(t, "/assess/")
	if res.StatusCode != 200 || body != "assess "+subject.String() {
		t.Fatalf("assessment page: %d %s", res.StatusCode, body)
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

func TestBareLoginRedirectsToTheOrgSurface(t *testing.T) {
	f := newWebFixture(t)
	res, _ := f.get(t, "/login")
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/app/login" {
		t.Fatalf("/login = %d %q, want 303 to /app/login", res.StatusCode, res.Header.Get("Location"))
	}
}
