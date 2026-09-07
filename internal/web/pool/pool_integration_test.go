//go:build integration

package pool_test

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
	"recruiting/internal/web/jobs"
	"recruiting/internal/web/layout"
	"recruiting/internal/web/middleware"
	"recruiting/internal/web/pool"
)

const testPassword = "hunter2-long-enough"

type fixture struct {
	srv                         *httptest.Server
	pool                        *service.PoolService
	sys                         *pgxpool.Pool
	orgID, sourceJob, openJob   uuid.UUID
	appID, entryID              uuid.UUID
	recruiterEmail, vetterEmail string
}

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

	f := &fixture{sys: sys, orgID: uuid.New(), sourceJob: uuid.New(), openJob: uuid.New(), appID: uuid.New()}
	f.recruiterEmail = "rec-" + f.orgID.String() + "@example.com"
	f.vetterEmail = "vet-" + f.orgID.String() + "@example.com"
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := sys.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`insert into org (id, name, slug) values ($1, $2, $2)`, f.orgID, f.orgID.String())
	t.Cleanup(func() { _, _ = sys.Exec(context.Background(), `delete from org where id = $1`, f.orgID) })
	hash, _ := service.HashPassword(testPassword)
	for _, seed := range []struct{ email, role string }{
		{f.recruiterEmail, service.RoleRecruiter}, {f.vetterEmail, service.RoleVetter},
	} {
		id := uuid.New()
		exec(`insert into org_user (id, org_id, email, name) values ($1, $2, $3, $3)`, id, f.orgID, seed.email)
		exec(`insert into org_user_role (org_user_id, org_id, role) values ($1, $2, $3)`, id, f.orgID, seed.role)
		exec(`insert into org_user_credential (org_user_id, org_id, password_hash) values ($1, $2, $3)`, id, f.orgID, hash)
	}
	companyID, candID := uuid.New(), uuid.New()
	exec(`insert into client_company (id, org_id, name) values ($1, $2, 'Globex')`, companyID, f.orgID)
	for _, j := range []struct {
		id   uuid.UUID
		slug string
	}{{f.sourceJob, "src-"}, {f.openJob, "open-"}} {
		exec(`insert into job (id, org_id, client_company_id, title, slug, status, skills, seniority, location, remote_policy)
			values ($1, $2, $3, 'Go Engineer', $4, 'open', $5, 'senior', 'Berlin', 'remote')`,
			j.id, f.orgID, companyID, j.slug+f.orgID.String(), []string{"go", "postgres"})
		exec(`insert into stage (org_id, job_id, position, name, kind) values ($1, $2, 1, 'Applied', 'generic')`, f.orgID, j.id)
	}
	exec(`insert into candidate (id, org_id, email, name) values ($1, $2, $3, 'Ada Lovelace')`, candID, f.orgID, "ada-"+f.orgID.String()+"@example.com")
	var stageID uuid.UUID
	if err := sys.QueryRow(ctx, `select id from stage where job_id = $1`, f.sourceJob).Scan(&stageID); err != nil {
		t.Fatal(err)
	}
	exec(`insert into application (id, org_id, job_id, candidate_id, client_company_id, stage_id) values ($1, $2, $3, $4, $5, $6)`,
		f.appID, f.orgID, f.sourceJob, candID, companyID, stageID)

	f.pool = service.NewPoolService(st)
	entry, err := f.pool.Flag(ctx, service.Principal{
		Kind: service.PrincipalOrgUser, OrgID: f.orgID, Roles: []string{service.RoleRecruiter},
	}, f.appID)
	if err != nil {
		t.Fatal(err)
	}
	f.entryID = entry.ID

	mux := chi.NewMux()
	if _, err := auth.Mount(mux, auth.Deps{Auth: service.NewAuthService(st), CookieSecret: []byte("test-secret")}); err != nil {
		t.Fatal(err)
	}
	layout.MountStatic(mux)
	// The suggestions partial lives under the job screens, so the two mounts
	// must coexist the way serve does it.
	jobs.Mount(mux, jobs.Deps{Jobs: service.NewJobService(st), Org: service.NewOrgService(st)})
	pool.Mount(mux, pool.Deps{Pool: f.pool, Org: service.NewOrgService(st)})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

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
	form := url.Values{"email": {email}, "password": {testPassword}, middleware.CSRFField: {s.csrf()}}
	res, err := s.cli.PostForm(s.f.srv.URL+"/app/login", form)
	if err != nil {
		s.t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusSeeOther {
		s.t.Fatalf("login as %s: %d", email, res.StatusCode)
	}
}

func TestPoolScreenBrowsesSearchesEditsAndRemoves(t *testing.T) {
	f := newFixture(t)
	s := f.browser(t)
	s.login(f.recruiterEmail)

	res, body := s.get(pool.Prefix)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "Ada Lovelace") {
		t.Fatalf("pool list = %d, body %q", res.StatusCode, body)
	}
	if res, body := s.get(pool.Prefix + "?q=postgres"); res.StatusCode != http.StatusOK || !strings.Contains(body, "Ada Lovelace") {
		t.Fatalf("tag search = %d, body %q", res.StatusCode, body)
	}
	if res, body := s.get(pool.Prefix + "?q=haskell"); res.StatusCode != http.StatusOK || strings.Contains(body, "Ada Lovelace") {
		t.Fatalf("search for an absent tag still listed the entry: %d %q", res.StatusCode, body)
	}

	res, body = s.post(pool.Prefix+"/"+f.entryID.String(), url.Values{
		"skills": {"go, kubernetes"}, "notes": {"open to relocation"},
		"location": {"Lisbon"}, "remote_ok": {"1"},
	})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("edit = %d, body %q", res.StatusCode, body)
	}
	_, body = s.get(pool.Prefix)
	if !strings.Contains(body, "kubernetes") || !strings.Contains(body, "open to relocation") ||
		!strings.Contains(body, "Lisbon") {
		t.Fatalf("the edit is not on the screen: %q", body)
	}

	if res, body := s.post(pool.Prefix+"/"+f.entryID.String()+"/remove", url.Values{}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("remove = %d, body %q", res.StatusCode, body)
	}
	if _, body := s.get(pool.Prefix); strings.Contains(body, "Ada Lovelace") {
		t.Fatalf("the removed entry is still listed: %q", body)
	}
}

func TestPoolScreenIsRecruitersOnly(t *testing.T) {
	f := newFixture(t)
	s := f.browser(t)
	s.login(f.vetterEmail)
	if res, _ := s.get(pool.Prefix); res.StatusCode != http.StatusForbidden {
		t.Fatalf("pool list as a vetter = %d, want 403", res.StatusCode)
	}
}

func TestSuggestionsPartialRanksAndAdds(t *testing.T) {
	f := newFixture(t)
	s := f.browser(t)
	s.login(f.recruiterEmail)

	path := pool.SuggestionsPath(f.openJob)
	res, body := s.get(path)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "Ada Lovelace") {
		t.Fatalf("suggestions = %d, body %q", res.StatusCode, body)
	}
	for _, want := range []string{"Skills", "Seniority", "Location", "Assessment"} {
		if !strings.Contains(body, want) {
			t.Errorf("the breakdown does not name %s: %q", want, body)
		}
	}

	res, body = s.post(path+"/"+f.entryID.String(), url.Values{})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("add to job = %d, body %q", res.StatusCode, body)
	}
	var n int
	if err := f.sys.QueryRow(context.Background(),
		`select count(*) from application a join stage s on s.id = a.stage_id
		 where a.job_id = $1 and s.position = 1`, f.openJob).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("the job holds %d applications in its first stage, want 1", n)
	}
	// The added candidate drops out of the panel it was added from.
	if strings.Contains(body, "Ada Lovelace") {
		t.Errorf("the panel still suggests the candidate it just added: %q", body)
	}

	// Adding the same candidate again is the recruiter's mistake, not a
	// success: the panel says so in its own voice.
	res, body = s.post(path+"/"+f.entryID.String(), url.Values{})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("second add = %d, body %q", res.StatusCode, body)
	}
	if !strings.Contains(body, "flash-error") || !strings.Contains(body, "already has an application") {
		t.Errorf("the duplicate was not reported as a failure: %q", body)
	}
}

func TestFlagButtonFilesTheApplicationInThePool(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	// The fragment a pipeline screen embeds posts to the route below.
	var button strings.Builder
	if err := pool.FlagButton(f.appID).Render(ctx, &button); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(button.String(), pool.FlagPath(f.appID)) {
		t.Fatalf("the flag button does not post to the flag route: %q", button.String())
	}
	if err := f.pool.Remove(ctx, service.Principal{
		Kind: service.PrincipalOrgUser, OrgID: f.orgID, Roles: []string{service.RoleRecruiter},
	}, f.entryID); err != nil {
		t.Fatal(err)
	}
	s := f.browser(t)
	s.login(f.recruiterEmail)
	if _, body := s.get(pool.Prefix); strings.Contains(body, "Ada Lovelace") {
		t.Fatalf("the removed entry is still listed: %q", body)
	}

	res, body := s.post(pool.FlagPath(f.appID), url.Values{})
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "On the shortlist") {
		t.Fatalf("flag = %d, body %q", res.StatusCode, body)
	}
	var flagged bool
	if err := f.sys.QueryRow(ctx, `select high_quality from application where id = $1`, f.appID).Scan(&flagged); err != nil {
		t.Fatal(err)
	}
	if !flagged {
		t.Error("the application was not marked high quality")
	}
	if _, body := s.get(pool.Prefix); !strings.Contains(body, "Ada Lovelace") {
		t.Fatalf("flagging did not bring the entry back: %q", body)
	}
}
