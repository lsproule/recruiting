//go:build integration

package intake_test

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
	"recruiting/internal/web/intake"
	"recruiting/internal/web/layout"
	"recruiting/internal/web/middleware"
)

const testPassword = "hunter2-long-enough"

type fixture struct {
	srv            *httptest.Server
	sys            *pgxpool.Pool
	orgID          uuid.UUID
	recruiterEmail string
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

	f := &fixture{sys: sys, orgID: uuid.New()}
	f.recruiterEmail = "rec-" + f.orgID.String() + "@example.com"
	hash, _ := service.HashPassword(testPassword)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := sys.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	exec(`insert into org (id, name, slug) values ($1, $2, $2)`, f.orgID, f.orgID.String())
	t.Cleanup(func() { _, _ = sys.Exec(context.Background(), `delete from org where id = $1`, f.orgID) })
	userID := uuid.New()
	exec(`insert into org_user (id, org_id, email, name) values ($1, $2, $3, 'Rae')`, userID, f.orgID, f.recruiterEmail)
	exec(`insert into org_user_role (org_user_id, org_id, role) values ($1, $2, $3)`, userID, f.orgID, service.RoleRecruiter)
	exec(`insert into org_user_credential (org_user_id, org_id, password_hash) values ($1, $2, $3)`, userID, f.orgID, hash)

	templateID := uuid.New()
	exec(`insert into pipeline_template (id, org_id, name, is_default) values ($1, $2, 'Standard', true)`, templateID, f.orgID)
	for i, s := range service.DefaultPipelineStages {
		exec(`insert into pipeline_template_stage (org_id, template_id, position, name, kind, unblind)
		      values ($1, $2, $3, $4, $5, $6)`, f.orgID, templateID, i+1, s.Name, s.Kind, s.Unblind)
	}
	for _, p := range []struct{ title, difficulty, tags string }{
		{"Warm up", "easy", `{go}`},
		{"Rate limiter", "medium", `{concurrency}`},
		{"Backpressure queue", "hard", `{concurrency}`},
	} {
		exec(`insert into problem (id, org_id, kind, title, statement, difficulty, tags, allowed_languages, quality, proven_languages)
		      values ($1, $2, 'code', $3, 'Solve it.', $4, $5, '{python}', 80, '{python}')`,
			uuid.New(), f.orgID, p.title, p.difficulty, p.tags)
	}

	q, err := queue.New(st.Pool(), queue.Config{})
	if err != nil {
		t.Fatal(err)
	}
	mux := chi.NewMux()
	if _, err := auth.Mount(mux, auth.Deps{Auth: service.NewAuthService(st), CookieSecret: []byte("test-secret")}); err != nil {
		t.Fatal(err)
	}
	layout.MountStatic(mux)
	intake.Mount(mux, intake.Deps{
		Intake:   service.NewIntakeService(st, q, "https://cadre.example"),
		Problems: service.NewProblemService(st, nil),
		Org:      service.NewOrgService(st),
	})
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
	s := &session{t: t, f: f, cli: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	s.login()
	return s
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

func (s *session) login() {
	s.t.Helper()
	s.get("/app/login")
	res, body := s.post("/app/login", url.Values{"email": {s.f.recruiterEmail}, "password": {testPassword}})
	if res.StatusCode != http.StatusSeeOther {
		s.t.Fatalf("login: %d %s", res.StatusCode, body)
	}
}

// start opens the intake and returns the draft's path.
func (s *session) start() string {
	s.t.Helper()
	res, body := s.get(intake.StartPath)
	if res.StatusCode != http.StatusSeeOther {
		s.t.Fatalf("start intake: %d %s", res.StatusCode, body)
	}
	return res.Header.Get("Location")
}

func clientStep() url.Values {
	return url.Values{
		"company": {"Northwind Systems"}, "industry": {"Payments infrastructure"},
		"contact_name": {"Priya Raman"}, "contact_email": {"priya@northwind.example"},
		"shortlist_sla_days": {"5"}, "brief": {"Only people who can reason about backpressure."},
		"goto": {"2"},
	}
}

func jobStep() url.Values {
	return url.Values{
		"title": {"Staff Backend Engineer"}, "seniority": {service.SeniorityStaff},
		"location": {"New York"}, "remote_policy": {service.RemoteHybrid},
		"salary_min": {"210000"}, "salary_max": {"255000"}, "goto": {"3"},
	}
}

func TestIntakeStartsAndResumesAtTheSavedStep(t *testing.T) {
	f := newFixture(t)
	s := f.browser(t)
	path := s.start()
	if !strings.HasPrefix(path, intake.Prefix+"/") {
		t.Fatalf("start redirected to %q", path)
	}

	res, body := s.post(path+"/1", clientStep())
	if res.StatusCode != http.StatusOK {
		t.Fatalf("client step: %d %s", res.StatusCode, body)
	}
	// A second visit resumes where the recruiter left off, with what they
	// typed still on the page.
	if again := s.start(); again != path {
		t.Errorf("start opened %q, want the draft already in flight at %q", again, path)
	}
	res, body = s.get(path)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("resume: %d", res.StatusCode)
	}
	if !strings.Contains(body, "Staff Backend Engineer") && !strings.Contains(body, "Job title") {
		t.Errorf("resumed page is not the job step:\n%s", body)
	}
	if !strings.Contains(body, "Northwind Systems") {
		t.Errorf("the client's name did not survive the step")
	}
}

func TestIntakeStepShowsWhatIsMissing(t *testing.T) {
	f := newFixture(t)
	s := f.browser(t)
	path := s.start()
	form := clientStep()
	form.Set("company", "")
	res, body := s.post(path+"/1", form)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", res.StatusCode)
	}
	if !strings.Contains(body, "needs a name") {
		t.Errorf("no message naming the missing company:\n%s", body)
	}
	// What was typed is still there to correct.
	if !strings.Contains(body, "priya@northwind.example") {
		t.Errorf("the step was redrawn empty")
	}
}

func TestIntakeCreateOpensTheClientAndTheJob(t *testing.T) {
	f := newFixture(t)
	s := f.browser(t)
	path := s.start()
	if res, body := s.post(path+"/1", clientStep()); res.StatusCode != http.StatusOK {
		t.Fatalf("client step: %d %s", res.StatusCode, body)
	}
	if res, body := s.post(path+"/2", jobStep()); res.StatusCode != http.StatusOK {
		t.Fatalf("job step: %d %s", res.StatusCode, body)
	}
	if res, body := s.post(path+"/3", url.Values{"skills": {"concurrency, go"}, "goto": {"4"}}); res.StatusCode != http.StatusOK {
		t.Fatalf("skills step: %d %s", res.StatusCode, body)
	}
	res, body := s.post(path+"/4", url.Values{"source": {service.IntakeSourceDefault}, "goto": {"5"}})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("question step: %d %s", res.StatusCode, body)
	}
	// The recommended set is on the page, so the recruiter reviews what they
	// are about to send.
	if !strings.Contains(body, "Rate limiter") || !strings.Contains(body, "Backpressure queue") {
		t.Errorf("the staff default set is not shown:\n%s", body)
	}

	res, body = s.post(path+"/create", url.Values{})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("create: %d %s", res.StatusCode, body)
	}
	if to := res.Header.Get("Location"); !strings.HasPrefix(to, "/app/jobs/") {
		t.Errorf("create redirected to %q, want the new job", to)
	}
	ctx := context.Background()
	for _, table := range []string{"client_company", "client_user", "job", "assessment"} {
		var n int
		if err := f.sys.QueryRow(ctx, `select count(*) from `+table+` where org_id = $1`, f.orgID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("%s holds %d rows after the intake, want 1", table, n)
		}
	}
	var attached int
	err := f.sys.QueryRow(ctx, `select count(*) from stage where org_id = $1 and kind = 'assessment' and assessment_id is not null`, f.orgID).Scan(&attached)
	if err != nil {
		t.Fatal(err)
	}
	if attached != 1 {
		t.Errorf("%d assessment stages carry an assessment, want 1", attached)
	}
	// The draft is gone, so the next start is a fresh intake.
	var drafts int
	if err := f.sys.QueryRow(ctx, `select count(*) from intake_draft where org_id = $1`, f.orgID).Scan(&drafts); err != nil {
		t.Fatal(err)
	}
	if drafts != 0 {
		t.Errorf("%d drafts survived the create", drafts)
	}
}

func TestIntakeRefusesCreateBeforeTheStepsAreAnswered(t *testing.T) {
	f := newFixture(t)
	s := f.browser(t)
	path := s.start()
	res, body := s.post(path+"/create", url.Values{})
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", res.StatusCode, body)
	}
	var n int
	if err := f.sys.QueryRow(context.Background(), `select count(*) from client_company where org_id = $1`, f.orgID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("an unanswered intake created %d client companies", n)
	}
}
