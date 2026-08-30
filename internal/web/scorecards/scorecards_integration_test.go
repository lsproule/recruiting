//go:build integration

package scorecards_test

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
	"recruiting/internal/web/layout"
	"recruiting/internal/web/middleware"
	"recruiting/internal/web/pipeline"
	"recruiting/internal/web/scorecards"
)

const testPassword = "hunter2-long-enough"

type fixture struct {
	srv                         *httptest.Server
	orgID, jobID, appID         uuid.UUID
	interview, assess           uuid.UUID
	vetterID, otherVetterID     uuid.UUID
	recruiterEmail, vetterEmail string
	otherVetterEmail            string
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

	f := &fixture{
		orgID: uuid.New(), jobID: uuid.New(), appID: uuid.New(),
		interview: uuid.New(), assess: uuid.New(),
		vetterID: uuid.New(), otherVetterID: uuid.New(),
	}
	f.recruiterEmail = "rec-" + f.orgID.String() + "@example.com"
	f.vetterEmail = "vet-" + f.orgID.String() + "@example.com"
	f.otherVetterEmail = "vet2-" + f.orgID.String() + "@example.com"
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := sys.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`insert into org (id, name, slug) values ($1, $2, $2)`, f.orgID, f.orgID.String())
	t.Cleanup(func() { _, _ = sys.Exec(context.Background(), `delete from org where id = $1`, f.orgID) })
	hash, _ := service.HashPassword(testPassword)
	seeds := []struct {
		id    uuid.UUID
		email string
		role  string
	}{
		{uuid.New(), f.recruiterEmail, service.RoleRecruiter},
		{f.vetterID, f.vetterEmail, service.RoleVetter},
		{f.otherVetterID, f.otherVetterEmail, service.RoleVetter},
	}
	for _, seed := range seeds {
		exec(`insert into org_user (id, org_id, email, name) values ($1, $2, $3, $3)`, seed.id, f.orgID, seed.email)
		exec(`insert into org_user_role (org_user_id, org_id, role) values ($1, $2, $3)`, seed.id, f.orgID, seed.role)
		exec(`insert into org_user_credential (org_user_id, org_id, password_hash) values ($1, $2, $3)`, seed.id, f.orgID, hash)
	}
	companyID := uuid.New()
	exec(`insert into client_company (id, org_id, name) values ($1, $2, 'Globex')`, companyID, f.orgID)
	exec(`insert into job (id, org_id, client_company_id, title, slug, status) values ($1, $2, $3, 'Senior Go Engineer', $4, 'open')`, f.jobID, f.orgID, companyID, "go-"+f.orgID.String())
	exec(`insert into stage (id, org_id, job_id, position, name, kind) values ($1, $2, $3, 1, 'Phone screen', 'interview')`, f.interview, f.orgID, f.jobID)
	exec(`insert into stage (id, org_id, job_id, position, name, kind) values ($1, $2, $3, 2, 'Take-home', 'assessment')`, f.assess, f.orgID, f.jobID)
	exec(`insert into stage (id, org_id, job_id, position, name, kind, terminal_status) values ($1, $2, $3, 3, 'Hired', 'terminal', 'hired')`, uuid.New(), f.orgID, f.jobID)
	exec(`insert into stage (id, org_id, job_id, position, name, kind, terminal_status) values ($1, $2, $3, 4, 'Rejected', 'terminal', 'rejected')`, uuid.New(), f.orgID, f.jobID)
	candID := uuid.New()
	exec(`insert into candidate (id, org_id, email, name) values ($1, $2, $3, 'Ada Lovelace')`, candID, f.orgID, "ada-"+f.orgID.String()+"@example.com")
	exec(`insert into application (id, org_id, job_id, candidate_id, client_company_id, stage_id, vetter_id) values ($1, $2, $3, $4, $5, $6, $7)`,
		f.appID, f.orgID, f.jobID, candID, companyID, f.interview, f.vetterID)

	mux := chi.NewMux()
	if _, err := auth.Mount(mux, auth.Deps{Auth: service.NewAuthService(st), CookieSecret: []byte("test-secret")}); err != nil {
		t.Fatal(err)
	}
	layout.MountStatic(mux)
	// Mounted alongside the pipeline surface, which owns the other routes
	// under /app/applications/{id}.
	pipeline.Mount(mux, pipeline.Deps{Applications: service.NewApplicationService(st, nil, "https://example.test"), Org: service.NewOrgService(st)})
	scorecards.Mount(mux, scorecards.Deps{Scorecards: service.NewScorecardService(st), Org: service.NewOrgService(st)})
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

func (s *session) post(path string, form url.Values) (*http.Response, string) {
	s.t.Helper()
	form.Set(middleware.CSRFField, s.csrf())
	req, _ := http.NewRequest(http.MethodPost, s.f.srv.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := s.cli.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

func (f *fixture) rubricPath() string { return scorecards.RubricPath(f.jobID, f.interview) }
func (f *fixture) formPath() string   { return scorecards.FormPath(f.appID, f.interview) }

// setRubric gives the interview stage two criteria as a recruiter would.
func (f *fixture) setRubric(t *testing.T) {
	t.Helper()
	s := f.browser(t)
	s.login(f.recruiterEmail)
	res, body := s.post(f.rubricPath(), url.Values{
		"name":        {"Phone screen"},
		"name_of":     {"Communication", "Depth"},
		"description": {"Explains their work clearly", "Knows the tools they name"},
	})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("save rubric = %d, body %q", res.StatusCode, body)
	}
}

func TestRecruiterEditsTheStageRubric(t *testing.T) {
	f := newFixture(t)
	f.setRubric(t)
	s := f.browser(t)
	s.login(f.recruiterEmail)
	res, body := s.get(f.rubricPath())
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "Communication") || !strings.Contains(body, "Knows the tools they name") {
		t.Fatalf("rubric page = %d, body %q", res.StatusCode, body)
	}
}

func TestVetterFilesAScorecardAndTheRecruiterSeesIt(t *testing.T) {
	f := newFixture(t)
	f.setRubric(t)

	vet := f.browser(t)
	vet.login(f.vetterEmail)
	res, body := vet.get("/app/scorecards")
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "Ada Lovelace") || !strings.Contains(body, "Not filed") {
		t.Fatalf("assignments = %d, body %q", res.StatusCode, body)
	}
	if res, body = vet.get(f.formPath()); res.StatusCode != http.StatusOK || !strings.Contains(body, "Communication") {
		t.Fatalf("form = %d, body %q", res.StatusCode, body)
	}

	// A score off the scale comes back on the form rather than being stored.
	res, body = vet.post(f.formPath(), url.Values{
		"criterion": {"Communication", "Depth"}, "score": {"9", "4"},
		"criterion_notes": {"clear", "deep"}, "overall": {service.OverallStrongYes}, "notes": {"good call"},
	})
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "score from 1 to 5") {
		t.Fatalf("out-of-range score = %d, body %q", res.StatusCode, body)
	}
	// What they typed comes back with the refusal.
	for _, want := range []string{`value="clear"`, `value="deep"`, "good call"} {
		if !strings.Contains(body, want) {
			t.Errorf("refused form lost %q", want)
		}
	}

	res, body = vet.post(f.formPath(), url.Values{
		"criterion": {"Communication", "Depth"}, "score": {"5", "4"},
		"criterion_notes": {"clear", "deep"}, "overall": {service.OverallStrongYes}, "notes": {"good call"},
	})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("submit = %d, body %q", res.StatusCode, body)
	}
	if _, body = vet.get("/app/scorecards"); !strings.Contains(body, "Strong yes") {
		t.Fatalf("assignments after submit: %q", body)
	}

	rec := f.browser(t)
	rec.login(f.recruiterEmail)
	res, body = rec.get(scorecards.SummaryPath(f.appID))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("summary = %d", res.StatusCode)
	}
	for _, want := range []string{"Strong yes", "Communication", "clear", "good call", f.vetterEmail} {
		if !strings.Contains(body, want) {
			t.Errorf("summary lacks %q; body %q", want, body)
		}
	}

	// The application page's move form still answers on its own routes.
	if res, _ = rec.get("/app/applications/" + f.appID.String()); res.StatusCode != http.StatusOK {
		t.Fatalf("application page = %d", res.StatusCode)
	}
	res, body = rec.post("/app/applications/"+f.appID.String()+"/move", url.Values{"to_stage_id": {f.assess.String()}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("move after the scorecard = %d, body %q", res.StatusCode, body)
	}
}

func TestAnotherVettersScorecardIsNotEditable(t *testing.T) {
	f := newFixture(t)
	f.setRubric(t)
	vet := f.browser(t)
	vet.login(f.vetterEmail)
	if res, body := vet.post(f.formPath(), url.Values{
		"criterion": {"Communication", "Depth"}, "score": {"4", "4"},
		"criterion_notes": {"", ""}, "overall": {service.OverallYes},
	}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("submit = %d, body %q", res.StatusCode, body)
	}
	var cardID uuid.UUID
	{
		s := f.browser(t)
		s.login(f.vetterEmail)
		_, body := s.get(f.formPath())
		const marker = `name="scorecard_id" value="`
		i := strings.Index(body, marker)
		if i < 0 {
			t.Fatalf("form carries no scorecard id: %q", body)
		}
		rest := body[i+len(marker):]
		id, err := uuid.Parse(rest[:strings.Index(rest, `"`)])
		if err != nil {
			t.Fatal(err)
		}
		cardID = id
	}

	other := f.browser(t)
	other.login(f.otherVetterEmail)
	res, _ := other.post(f.formPath(), url.Values{
		"scorecard_id": {cardID.String()},
		"criterion":    {"Communication", "Depth"}, "score": {"1", "1"},
		"criterion_notes": {"", ""}, "overall": {service.OverallStrongNo},
	})
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("edit by another vetter = %d, want 403", res.StatusCode)
	}
}
