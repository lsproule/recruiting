//go:build integration

package admin_test

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"recruiting/internal/service"
	"recruiting/internal/store"
	"recruiting/internal/web/admin"
	"recruiting/internal/web/auth"
	"recruiting/internal/web/layout"
	"recruiting/internal/web/middleware"
)

type fixture struct {
	srv   *httptest.Server
	sys   *pgxpool.Pool
	orgID uuid.UUID
	// admin and recruiter credentials seeded directly.
	adminEmail, recruiterEmail, password string
}

const testPassword = "hunter2-long-enough"

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

	f := &fixture{sys: sys, orgID: uuid.New(), password: testPassword}
	f.adminEmail = "admin-" + f.orgID.String() + "@example.com"
	f.recruiterEmail = "rec-" + f.orgID.String() + "@example.com"
	hash, _ := service.HashPassword(f.password)
	if _, err := sys.Exec(ctx, `insert into org (id, name, slug) values ($1, $2, $2)`, f.orgID, f.orgID.String()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = sys.Exec(context.Background(), `delete from org where id = $1`, f.orgID) })
	for _, seed := range []struct {
		id    uuid.UUID
		email string
		role  string
	}{
		{uuid.New(), f.adminEmail, service.RoleAdmin},
		{uuid.New(), f.recruiterEmail, service.RoleRecruiter},
	} {
		if _, err := sys.Exec(ctx, `insert into org_user (id, org_id, email, name) values ($1, $2, $3, $3)`, seed.id, f.orgID, seed.email); err != nil {
			t.Fatal(err)
		}
		if _, err := sys.Exec(ctx, `insert into org_user_role (org_user_id, org_id, role) values ($1, $2, $3)`, seed.id, f.orgID, seed.role); err != nil {
			t.Fatal(err)
		}
		if _, err := sys.Exec(ctx, `insert into org_user_credential (org_user_id, org_id, password_hash) values ($1, $2, $3)`, seed.id, f.orgID, hash); err != nil {
			t.Fatal(err)
		}
	}

	mux := chi.NewMux()
	if _, err := auth.Mount(mux, auth.Deps{Auth: service.NewAuthService(st), CookieSecret: []byte("test-secret")}); err != nil {
		t.Fatal(err)
	}
	layout.MountStatic(mux)
	// The client portal lands in a later task; this stands in for its job list.
	mux.With(middleware.RequireAuth("/client/login")).Get("/client/", func(w http.ResponseWriter, r *http.Request) {
		page := layout.Page{Title: "Jobs", Surface: layout.SurfaceClient, CSRF: middleware.CSRFToken(r), UserName: "Client"}
		_ = layout.Base(page).Render(r.Context(), w)
		_, _ = w.Write([]byte("<p>No jobs yet.</p>"))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	// Mounted once the server has an address: password-set links are built
	// from BaseURL, which is required configuration in every real process.
	admin.Mount(mux, admin.Deps{Org: service.NewOrgService(st), BaseURL: f.srv.URL})
	return f
}

// session is one logged-in browser.
type session struct {
	t   *testing.T
	f   *fixture
	cli *http.Client
}

func (f *fixture) browser(t *testing.T) *session {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	return &session{t: t, f: f, cli: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (s *session) get(path string) (*http.Response, string) {
	s.t.Helper()
	res, err := s.cli.Get(s.f.srv.URL + path)
	if err != nil {
		s.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

func (s *session) post(path string, form url.Values) (*http.Response, string) {
	s.t.Helper()
	form.Set(middleware.CSRFField, s.csrf())
	res, err := s.cli.PostForm(s.f.srv.URL+path, form)
	if err != nil {
		s.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

func (s *session) csrf() string {
	s.t.Helper()
	u, _ := url.Parse(s.f.srv.URL)
	for _, c := range s.cli.Jar.Cookies(u) {
		if c.Name == middleware.CSRFCookie {
			return c.Value
		}
	}
	s.get("/app/login")
	for _, c := range s.cli.Jar.Cookies(u) {
		if c.Name == middleware.CSRFCookie {
			return c.Value
		}
	}
	s.t.Fatal("no csrf cookie")
	return ""
}

func (s *session) login(prefix, email, password string) {
	s.t.Helper()
	s.get(prefix + "/login")
	res, body := s.post(prefix+"/login", url.Values{"email": {email}, "password": {password}})
	if res.StatusCode != http.StatusSeeOther {
		s.t.Fatalf("login %s as %s: %d %s", prefix, email, res.StatusCode, body)
	}
}

func TestNonAdminIsDeniedOnAdminRoutes(t *testing.T) {
	f := newFixture(t)
	b := f.browser(t)
	b.login("/app", f.recruiterEmail, f.password)
	for _, path := range []string{"/app/admin/users", "/app/admin/clients", "/app/admin/settings"} {
		if res, _ := b.get(path); res.StatusCode != http.StatusForbidden {
			t.Errorf("GET %s as recruiter: %d, want 403", path, res.StatusCode)
		}
	}
	res, _ := b.post("/app/admin/users", url.Values{"name": {"X"}, "email": {"x@example.com"}})
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("POST users as recruiter: %d, want 403", res.StatusCode)
	}
	// Anonymous requests go to the login page rather than the admin screens.
	anon := f.browser(t)
	if res, _ := anon.get("/app/admin/users"); res.StatusCode != http.StatusSeeOther {
		t.Errorf("anonymous: %d, want a redirect to login", res.StatusCode)
	}
}

var linkPattern = regexp.MustCompile(`https?://[^\s"<]+/app/reset/[A-Za-z0-9_-]+`)

func TestAdminManagesUsersCompaniesAndClientLogin(t *testing.T) {
	f := newFixture(t)
	b := f.browser(t)
	b.login("/app", f.adminEmail, f.password)

	res, body := b.get("/app/admin/users")
	if res.StatusCode != 200 || !strings.Contains(body, f.recruiterEmail) {
		t.Fatalf("users page: %d %s", res.StatusCode, body)
	}
	// The chrome greets the user by name, never by raw id.
	if !strings.Contains(body, `<span class="who">`+f.adminEmail+`</span>`) {
		t.Errorf("signed-in user is not named in the chrome: %s", body)
	}
	// The layout serves its assets locally, never from a CDN.
	if !strings.Contains(body, `/static/htmx.min.js`) || !strings.Contains(body, `/static/alpine.min.js`) {
		t.Errorf("layout does not reference the local htmx/Alpine assets")
	}
	if strings.Contains(body, "//unpkg.com") || strings.Contains(body, "//cdn.") {
		t.Errorf("layout loads assets from a CDN")
	}

	vetterEmail := "vet-" + f.orgID.String() + "@example.com"
	res, body = b.post("/app/admin/users", url.Values{"name": {"Vic Vetter"}, "email": {vetterEmail}, "roles": {service.RoleVetter, service.RoleRecruiter}})
	if res.StatusCode != 200 || !strings.Contains(body, vetterEmail) {
		t.Fatalf("create user: %d %s", res.StatusCode, body)
	}
	if !strings.Contains(body, service.RoleVetter) || !strings.Contains(body, service.RoleRecruiter) {
		t.Errorf("created user's roles are not shown: %s", body)
	}
	if !linkPattern.MatchString(body) {
		t.Errorf("no password-set link shown for the new user: %s", body)
	}
	// A duplicate email is refused, not silently ignored.
	res, body = b.post("/app/admin/users", url.Values{"name": {"Dup"}, "email": {vetterEmail}, "roles": {service.RoleVetter}})
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("duplicate email: %d %s", res.StatusCode, body)
	}
	// An unknown role is refused.
	res, _ = b.post("/app/admin/users", url.Values{"name": {"Bad"}, "email": {"bad-" + vetterEmail}, "roles": {"wizard"}})
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("unknown role: %d", res.StatusCode)
	}

	// A client user needs a client company.
	res, _ = b.post("/app/admin/clients/users", url.Values{"name": {"Cara"}, "email": {"cara@example.com"}, "client_company_id": {""}})
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("client user without a company: %d", res.StatusCode)
	}

	res, body = b.post("/app/admin/clients", url.Values{"name": {"Globex"}})
	if res.StatusCode != 200 || !strings.Contains(body, "Globex") {
		t.Fatalf("create client company: %d %s", res.StatusCode, body)
	}
	companyID := ""
	for _, m := range regexp.MustCompile(`value="([0-9a-f-]{36})"`).FindAllStringSubmatch(body, -1) {
		companyID = m[1]
	}
	if companyID == "" {
		t.Fatalf("no client company id on the page: %s", body)
	}

	clientEmail := "client-" + f.orgID.String() + "@example.com"
	res, body = b.post("/app/admin/clients/users", url.Values{"name": {"Cara Client"}, "email": {clientEmail}, "client_company_id": {companyID}})
	if res.StatusCode != 200 || !strings.Contains(body, clientEmail) {
		t.Fatalf("create client user: %d %s", res.StatusCode, body)
	}
	link := linkPattern.FindString(body)
	if link == "" || !strings.HasPrefix(link, f.srv.URL) {
		t.Fatalf("password-set link %q is not built from BaseURL: %s", link, body)
	}
	link = strings.TrimPrefix(link, f.srv.URL)

	// The client user sets a password through the link and reaches the portal.
	cb := f.browser(t)
	if res, _ := cb.get(link); res.StatusCode != 200 {
		t.Fatalf("password-set page: %d", res.StatusCode)
	}
	if res, body := cb.post(link, url.Values{"password": {"client-password-1"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("set password: %d %s", res.StatusCode, body)
	}
	cb.login("/client", clientEmail, "client-password-1")
	res, body = cb.get("/client/")
	if res.StatusCode != 200 || !strings.Contains(body, "No jobs yet") {
		t.Fatalf("client portal: %d %s", res.StatusCode, body)
	}
	// The client session must not reach the admin surface.
	if res, _ := cb.get("/app/admin/users"); res.StatusCode != http.StatusForbidden {
		t.Errorf("client session on /app/admin: %d, want 403", res.StatusCode)
	}
}

func TestAdminSettingsRoundTripAndValidation(t *testing.T) {
	f := newFixture(t)
	b := f.browser(t)
	b.login("/app", f.adminEmail, f.password)

	res, body := b.get("/app/admin/settings")
	if res.StatusCode != 200 {
		t.Fatalf("settings page: %d %s", res.StatusCode, body)
	}
	for _, want := range []string{service.SettingPoolScoreThreshold, service.SettingAssessmentInviteDays, "paste_ratio", `value="80"`, `value="7"`} {
		if !strings.Contains(body, want) {
			t.Errorf("settings page missing %q", want)
		}
	}

	form := url.Values{
		service.SettingPoolScoreThreshold:    {"70"},
		service.SettingAssessmentInviteDays:  {"14"},
		service.SettingSnapshotRetentionDays: {"30"},
	}
	for _, name := range service.IntegritySignalNames {
		form.Set("weight."+name, "1")
	}
	form.Set("weight.paste_ratio", "-2")
	res, body = b.post("/app/admin/settings", form)
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "paste_ratio") {
		t.Fatalf("negative weight accepted: %d %s", res.StatusCode, body)
	}
	// Nothing was written.
	if _, body := b.get("/app/admin/settings"); !strings.Contains(body, `value="80"`) {
		t.Errorf("rejected settings were persisted anyway")
	}

	form.Set("weight.paste_ratio", "30")
	res, body = b.post("/app/admin/settings", form)
	if res.StatusCode != 200 {
		t.Fatalf("save settings: %d %s", res.StatusCode, body)
	}
	_, body = b.get("/app/admin/settings")
	if !strings.Contains(body, `value="70"`) || !strings.Contains(body, `value="14"`) || !strings.Contains(body, `value="30"`) ||
		!strings.Contains(body, service.SettingSnapshotRetentionDays) {
		t.Errorf("settings did not round-trip: %s", body)
	}
}

func TestSettingsRejectsMalformedStoredValues(t *testing.T) {
	f := newFixture(t)
	b := f.browser(t)
	b.login("/app", f.adminEmail, f.password)

	ctx := context.Background()
	for _, row := range []struct{ key, value string }{
		{service.SettingPoolScoreThreshold, `"not-a-number"`},
		{service.SettingIntegrityWeights, `"not-an-object"`},
	} {
		_, err := f.sys.Exec(ctx, `insert into org_setting (org_id, key, value) values ($1, $2, $3)
			on conflict (org_id, key) do update set value = excluded.value`, f.orgID, row.key, row.value)
		if err != nil {
			t.Fatal(err)
		}
		res, body := b.get("/app/admin/settings")
		if res.StatusCode == http.StatusOK {
			t.Errorf("%s = %s was silently replaced by a default: %s", row.key, row.value, body)
		}
		if _, err := f.sys.Exec(ctx, `delete from org_setting where org_id = $1 and key = $2`, f.orgID, row.key); err != nil {
			t.Fatal(err)
		}
	}
}

// A weight for a signal the worker no longer computes must not resurface.
func TestSettingsDropsStaleIntegrityWeights(t *testing.T) {
	f := newFixture(t)
	b := f.browser(t)
	b.login("/app", f.adminEmail, f.password)

	_, err := f.sys.Exec(context.Background(), `insert into org_setting (org_id, key, value) values ($1, $2, $3)
		on conflict (org_id, key) do update set value = excluded.value`,
		f.orgID, service.SettingIntegrityWeights, `{"paste_ratio": 42, "retired_signal": 99}`)
	if err != nil {
		t.Fatal(err)
	}
	res, body := b.get("/app/admin/settings")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("settings page: %d %s", res.StatusCode, body)
	}
	if !strings.Contains(body, `value="42"`) {
		t.Errorf("stored weight not shown: %s", body)
	}
	if strings.Contains(body, "retired_signal") || strings.Contains(body, `value="99"`) {
		t.Errorf("stale weight resurfaced: %s", body)
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
