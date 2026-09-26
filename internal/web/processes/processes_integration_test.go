//go:build integration

package processes_test

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

	"recruiting/internal/domain"
	"recruiting/internal/service"
	"recruiting/internal/store"
	"recruiting/internal/store/system"
	"recruiting/internal/web/auth"
	"recruiting/internal/web/layout"
	"recruiting/internal/web/middleware"
	"recruiting/internal/web/processes"
)

const testPassword = "hunter2-long-enough"

type fixture struct {
	srv                         *httptest.Server
	sys                         *pgxpool.Pool
	orgID                       uuid.UUID
	recruiterEmail, vetterEmail string
}

// newFixture bootstraps an org the way the admin CLI does, so it carries
// the seeded library, and mounts the process screens behind a login.
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

	f := &fixture{sys: sys}
	owner, err := system.Open(ctx, ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owner.Close)
	var res service.BootstrapResult
	if err := owner.WithSystemTx(ctx, func(ctx context.Context, tx *store.Tx) error {
		var err error
		res, err = service.BootstrapOrg(ctx, tx, service.NewOrg{Name: "Proc " + uuid.NewString(), AdminEmail: "adm-" + uuid.NewString() + "@example.com"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	f.orgID = res.OrgID
	t.Cleanup(func() { _, _ = sys.Exec(context.Background(), `delete from org where id = $1`, f.orgID) })
	f.recruiterEmail = "rec-" + f.orgID.String() + "@example.com"
	f.vetterEmail = "vet-" + f.orgID.String() + "@example.com"
	hash, _ := service.HashPassword(testPassword)
	for _, seed := range []struct{ email, role string }{{f.recruiterEmail, service.RoleRecruiter}, {f.vetterEmail, service.RoleVetter}} {
		id := uuid.New()
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{`insert into org_user (id, org_id, email, name) values ($1, $2, $3, $3)`, []any{id, f.orgID, seed.email}},
			{`insert into org_user_role (org_user_id, org_id, role) values ($1, $2, $3)`, []any{id, f.orgID, seed.role}},
			{`insert into org_user_credential (org_user_id, org_id, password_hash) values ($1, $2, $3)`, []any{id, f.orgID, hash}},
		} {
			if _, err := sys.Exec(ctx, q.sql, q.args...); err != nil {
				t.Fatal(err)
			}
		}
	}

	mux := chi.NewMux()
	if _, err := auth.Mount(mux, auth.Deps{Auth: service.NewAuthService(st), CookieSecret: []byte("test-secret")}); err != nil {
		t.Fatal(err)
	}
	layout.MountStatic(mux)
	processes.Mount(mux, processes.Deps{Processes: service.NewProcessService(st), Org: service.NewOrgService(st)})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func lockSchema(t *testing.T, ownerURL string) {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(context.Background(), `select pg_advisory_lock_shared(4242)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), `select pg_advisory_unlock_shared(4242)`)
		_ = conn.Close(context.Background())
	})
}

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

func (s *session) login(email string) {
	s.t.Helper()
	s.get("/app/login")
	res, body := s.post("/app/login", url.Values{"email": {email}, "password": {testPassword}})
	if res.StatusCode != http.StatusSeeOther {
		s.t.Fatalf("login as %s: %d %s", email, res.StatusCode, body)
	}
}

var idInPath = regexp.MustCompile(`/app/processes/([0-9a-f-]{36})$`)

func TestProcessLibraryIsListedAndEdited(t *testing.T) {
	f := newFixture(t)
	s := f.browser(t)
	s.login(f.recruiterEmail)

	res, body := s.get(processes.Prefix)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list: %d", res.StatusCode)
	}
	for _, want := range []string{"Engineering loop", "Fast track", "Recruiter call", "phone · 20 min", "video · 60 min", "5 min rounds", "Default", "Copy into this org", "Create process"} {
		if !strings.Contains(body, want) {
			t.Errorf("library screen lacks %q", want)
		}
	}
	if n := strings.Count(body, `class="panel process-card`); n != 3+3+1 {
		t.Errorf("screen draws %d cards, want the org's three, the library's three, and the blank form", n)
	}

	// A copy of a built-in process, named for it.
	res, _ = s.post(processes.Prefix, url.Values{"library_key": {domain.ProcessFastTrack}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("copy: %d", res.StatusCode)
	}
	loc := res.Header.Get("Location")
	m := idInPath.FindStringSubmatch(loc)
	if m == nil {
		t.Fatalf("copy redirected to %q", loc)
	}
	id := m[1]
	_, body = s.get(loc)
	for _, want := range []string{"Fast track", "copied from the built-in Fast track", "Screening sprint", `name="round_minutes"`, `value="5"`} {
		if !strings.Contains(body, want) {
			t.Errorf("editor lacks %q", want)
		}
	}

	// A sprint stage entered in minutes is stored in seconds and shown back.
	res, body = s.post(loc+"/stages", url.Values{"name": {"Lightning"}, "kind": {"sprint"}, "round_minutes": {"2.5"}, "break_seconds": {"20"}})
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "2.5 min rounds · 20s break") {
		t.Fatalf("add sprint stage: %d %s", res.StatusCode, snippet(body, "rounds"))
	}
	var seconds int
	if err := f.sys.QueryRow(context.Background(), `select round_seconds from pipeline_template_stage where template_id = $1 and name = 'Lightning'`, id).Scan(&seconds); err != nil || seconds != 150 {
		t.Fatalf("round_seconds = %d %v, want 150", seconds, err)
	}
	// A bad setting comes back as the editor with the reason, not a 500.
	res, body = s.post(loc+"/stages", url.Values{"name": {"Too quick"}, "kind": {"sprint"}, "round_seconds": {"3"}})
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "rounds must last") {
		t.Fatalf("bad round: %d %s", res.StatusCode, snippet(body, "rounds"))
	}

	// The default cannot be deleted; a copy can be made the default and then the old one deleted.
	res, _ = s.post(loc+"/default", url.Values{})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("make default: %d", res.StatusCode)
	}
	res, body = s.post(loc+"/delete", url.Values{})
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "default process cannot be deleted") {
		t.Fatalf("delete the default: %d %s", res.StatusCode, snippet(body, "default"))
	}

	// A vetter reads but cannot write.
	v := f.browser(t)
	v.login(f.vetterEmail)
	if res, body := v.get(loc); res.StatusCode != http.StatusOK || strings.Contains(body, "Add stage") {
		t.Fatalf("vetter view: %d, editable=%v", res.StatusCode, strings.Contains(body, "Add stage"))
	}
	if res, _ := v.post(loc+"/stages", url.Values{"name": {"X"}, "kind": {"generic"}}); res.StatusCode != http.StatusForbidden {
		t.Fatalf("vetter added a stage: %d", res.StatusCode)
	}
}

func snippet(body, around string) string {
	i := strings.Index(body, around)
	if i < 0 {
		return body[:min(len(body), 300)]
	}
	return body[max(0, i-150):min(len(body), i+150)]
}
