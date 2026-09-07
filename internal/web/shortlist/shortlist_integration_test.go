//go:build integration

package shortlist_test

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

	"recruiting/internal/queue"
	"recruiting/internal/service"
	"recruiting/internal/store"
	"recruiting/internal/web/auth"
	"recruiting/internal/web/jobs"
	"recruiting/internal/web/layout"
	"recruiting/internal/web/middleware"
	"recruiting/internal/web/shortlist"
)

const testPassword = "hunter2-long-enough"

const (
	qualifiedName = "Qualified Quinn"
	secondName    = "Second Sam"
	belowName     = "Below Bea"
)

type fixture struct {
	srv                         *httptest.Server
	sys                         *pgxpool.Pool
	orgID, jobID                uuid.UUID
	qualified, second, below    uuid.UUID
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

	f := &fixture{sys: sys, orgID: uuid.New(), jobID: uuid.New()}
	f.recruiterEmail = "rec-" + f.orgID.String() + "@example.com"
	f.vetterEmail = "vet-" + f.orgID.String() + "@example.com"
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := sys.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
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
	companyID, stageID := uuid.New(), uuid.New()
	exec(`insert into client_company (id, org_id, name) values ($1, $2, 'Globex')`, companyID, f.orgID)
	exec(`insert into client_user (id, org_id, client_company_id, email, name) values ($1, $2, $3, $4, 'Carl Client')`,
		uuid.New(), f.orgID, companyID, "client-"+f.orgID.String()+"@example.com")
	exec(`insert into job (id, org_id, client_company_id, title, slug, status) values ($1, $2, $3, 'Go Engineer', $4, 'open')`,
		f.jobID, f.orgID, companyID, "go-"+f.orgID.String())
	exec(`insert into stage (id, org_id, job_id, position, name, kind) values ($1, $2, $3, 1, 'Assessment', 'assessment')`, stageID, f.orgID, f.jobID)

	problemID, assessmentID := uuid.New(), uuid.New()
	exec(`insert into problem (id, org_id, kind, title, statement) values ($1, $2, 'code', 'Adder', 'Add them')`, problemID, f.orgID)
	exec(`insert into assessment (id, org_id, name, duration_minutes) values ($1, $2, 'Screen', 60)`, assessmentID, f.orgID)
	exec(`insert into assessment_problem (assessment_id, org_id, problem_id, position) values ($1, $2, $3, 1)`, assessmentID, f.orgID, problemID)
	scored := func(name string, score float64) uuid.UUID {
		t.Helper()
		candID, appID := uuid.New(), uuid.New()
		exec(`insert into candidate (id, org_id, email, name) values ($1, $2, $3, $4)`, candID, f.orgID, candID.String()+"@example.com", name)
		exec(`insert into application (id, org_id, job_id, candidate_id, client_company_id, stage_id) values ($1, $2, $3, $4, $5, $6)`,
			appID, f.orgID, f.jobID, candID, companyID, stageID)
		exec(`insert into attempt (org_id, application_id, assessment_id, stage_id, status, score, finished_at) values ($1, $2, $3, $4, 'scored', $5, now())`,
			f.orgID, appID, assessmentID, stageID, score)
		return appID
	}
	f.qualified = scored(qualifiedName, 91)
	f.second = scored(secondName, 78)
	f.below = scored(belowName, 41)

	q, err := queue.New(st.Pool(), queue.Config{})
	if err != nil {
		t.Fatal(err)
	}
	shortlists := service.NewShortlistService(st,
		service.NewReleaseService(st, q, "https://example.test"),
		service.NewReviewService(st, nil, nil), service.NewPoolService(st), q, "https://example.test")

	mux := chi.NewMux()
	if _, err := auth.Mount(mux, auth.Deps{Auth: service.NewAuthService(st), CookieSecret: []byte("test-secret")}); err != nil {
		t.Fatal(err)
	}
	layout.MountStatic(mux)
	// The builder lives under the job screens, so the two mounts must coexist
	// the way serve does it.
	jobs.Mount(mux, jobs.Deps{Jobs: service.NewJobService(st), Org: service.NewOrgService(st)})
	shortlist.Mount(mux, shortlist.Deps{Shortlists: shortlists, Org: service.NewOrgService(st)})
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

func (f *fixture) builderPath() string { return "/app/jobs/" + f.jobID.String() + "/shortlist" }

type session struct {
	t   *testing.T
	f   *fixture
	cli *http.Client
}

func (f *fixture) browser(t *testing.T, email string) *session {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	s := &session{t: t, f: f, cli: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	s.get("/app/login")
	form := url.Values{"email": {email}, "password": {testPassword}, middleware.CSRFField: {s.csrf()}}
	res, err := s.cli.PostForm(f.srv.URL+"/app/login", form)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("login as %s: %d", email, res.StatusCode)
	}
	return s
}

func (s *session) get(path string) (*http.Response, string) {
	s.t.Helper()
	res, err := s.cli.Get(s.f.srv.URL + path)
	if err != nil {
		s.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
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
	defer func() { _ = res.Body.Close() }()
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
	s.t.Fatal("no csrf cookie")
	return ""
}

func TestBuilderOffersOnlyTheQualifiedPoolAndSaysWhatTheClientSees(t *testing.T) {
	f := newFixture(t)
	s := f.browser(t, f.recruiterEmail)

	res, body := s.get(f.builderPath())
	if res.StatusCode != http.StatusOK {
		t.Fatalf("builder: %d", res.StatusCode)
	}
	for _, want := range []string{qualifiedName, secondName, "Client sees", "Hidden", "Why these, in your words"} {
		if !strings.Contains(body, want) {
			t.Errorf("the builder is missing %q", want)
		}
	}
	if strings.Contains(body, belowName) {
		t.Error("the builder offers a candidate below the threshold")
	}
}

func TestBuilderSendsTheRankingItWasShowing(t *testing.T) {
	f := newFixture(t)
	s := f.browser(t, f.recruiterEmail)

	form := url.Values{
		"note":            {"Ranked on the concurrency case."},
		"application_ids": {f.second.String(), f.qualified.String()},
	}
	if res, body := s.post(f.builderPath()+"/send", form); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("send: %d — %s", res.StatusCode, body)
	}
	var released int
	if err := f.sys.QueryRow(context.Background(),
		`select count(*) from application where id = any($1) and released_at is not null`,
		[]uuid.UUID{f.second, f.qualified}).Scan(&released); err != nil {
		t.Fatal(err)
	}
	if released != 2 {
		t.Fatalf("released %d of 2 picks", released)
	}
	_, body := s.get(f.builderPath())
	if !strings.Contains(body, "Already sent") {
		t.Error("the sent packet is not shown back to the recruiter")
	}
	first, second := strings.Index(body, "1. "+secondName), strings.Index(body, "2. "+qualifiedName)
	if first < 0 || second < 0 {
		t.Fatalf("the sent packet does not carry the ranking that was posted: %d %d", first, second)
	}
}

func TestBuilderRefusesAVetter(t *testing.T) {
	f := newFixture(t)
	s := f.browser(t, f.vetterEmail)

	if res, _ := s.get(f.builderPath()); res.StatusCode != http.StatusForbidden {
		t.Fatalf("builder as a vetter: %d", res.StatusCode)
	}
}
