//go:build integration

package pipeline_test

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
)

const testPassword = "hunter2-long-enough"

type fixture struct {
	srv                         *httptest.Server
	orgID, jobID, appID         uuid.UUID
	generic, interview, assess  uuid.UUID
	rejected                    uuid.UUID
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

	f := &fixture{orgID: uuid.New(), jobID: uuid.New(), appID: uuid.New(), generic: uuid.New(), interview: uuid.New(), assess: uuid.New(), rejected: uuid.New()}
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
	for _, seed := range []struct{ email, role string }{{f.recruiterEmail, service.RoleRecruiter}, {f.vetterEmail, service.RoleVetter}} {
		id := uuid.New()
		exec(`insert into org_user (id, org_id, email, name) values ($1, $2, $3, $3)`, id, f.orgID, seed.email)
		exec(`insert into org_user_role (org_user_id, org_id, role) values ($1, $2, $3)`, id, f.orgID, seed.role)
		exec(`insert into org_user_credential (org_user_id, org_id, password_hash) values ($1, $2, $3)`, id, f.orgID, hash)
	}
	companyID := uuid.New()
	exec(`insert into client_company (id, org_id, name) values ($1, $2, 'Globex')`, companyID, f.orgID)
	exec(`insert into job (id, org_id, client_company_id, title, slug, status) values ($1, $2, $3, 'Senior Go Engineer', $4, 'open')`, f.jobID, f.orgID, companyID, "go-"+f.orgID.String())
	exec(`insert into stage (id, org_id, job_id, position, name, kind) values ($1, $2, $3, 1, 'Applied', 'generic')`, f.generic, f.orgID, f.jobID)
	exec(`insert into stage (id, org_id, job_id, position, name, kind) values ($1, $2, $3, 2, 'Phone screen', 'interview')`, f.interview, f.orgID, f.jobID)
	exec(`insert into stage (id, org_id, job_id, position, name, kind) values ($1, $2, $3, 3, 'Take-home', 'assessment')`, f.assess, f.orgID, f.jobID)
	exec(`insert into stage (id, org_id, job_id, position, name, kind, terminal_status) values ($1, $2, $3, 4, 'Hired', 'terminal', 'hired')`, uuid.New(), f.orgID, f.jobID)
	exec(`insert into stage (id, org_id, job_id, position, name, kind, terminal_status) values ($1, $2, $3, 5, 'Rejected', 'terminal', 'rejected')`, f.rejected, f.orgID, f.jobID)
	candID := uuid.New()
	exec(`insert into candidate (id, org_id, email, name) values ($1, $2, $3, 'Ada Lovelace')`, candID, f.orgID, "ada-"+f.orgID.String()+"@example.com")
	exec(`insert into application (id, org_id, job_id, candidate_id, client_company_id, stage_id) values ($1, $2, $3, $4, $5, $6)`, f.appID, f.orgID, f.jobID, candID, companyID, f.generic)

	mux := chi.NewMux()
	if _, err := auth.Mount(mux, auth.Deps{Auth: service.NewAuthService(st), CookieSecret: []byte("test-secret")}); err != nil {
		t.Fatal(err)
	}
	layout.MountStatic(mux)
	// No queue: the screens are under test, not the side effects.
	pipeline.Mount(mux, pipeline.Deps{Applications: service.NewApplicationService(st, nil, "https://example.test"), Org: service.NewOrgService(st)})
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

// post sends a form; htmx marks the request the way the board's drag does.
func (s *session) post(path string, form url.Values, htmx bool) (*http.Response, string) {
	s.t.Helper()
	form.Set(middleware.CSRFField, s.csrf())
	req, _ := http.NewRequest(http.MethodPost, s.f.srv.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	res, err := s.cli.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

func (f *fixture) movePath() string { return "/app/applications/" + f.appID.String() + "/move" }

func TestBoardDragMovesAndPromptsForAnOverride(t *testing.T) {
	f := newFixture(t)
	s := f.browser(t)
	s.login(f.recruiterEmail)

	res, body := s.get("/app/pipeline/" + f.jobID.String())
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "Ada Lovelace") || !strings.Contains(body, `draggable="true"`) {
		t.Fatalf("board = %d, body %q", res.StatusCode, body)
	}

	res, body = s.post(f.movePath(), url.Values{"to_stage_id": {f.interview.String()}, "view": {"board"}}, true)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, `id="board"`) || strings.Contains(body, "<html") {
		t.Fatalf("htmx move = %d, want the board fragment; body %q", res.StatusCode, body)
	}

	// The next move needs a scorecard the stage does not have.
	res, body = s.post(f.movePath(), url.Values{"to_stage_id": {f.assess.String()}, "view": {"board"}}, true)
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, `name="override"`) || !strings.Contains(body, `name="reason"`) {
		t.Fatalf("move without scorecard = %d, want 422 with the override prompt; body %q", res.StatusCode, body)
	}
	res, body = s.post(f.movePath(), url.Values{"to_stage_id": {f.assess.String()}, "view": {"board"}, "override": {"1"}, "reason": {"screened on the phone"}}, true)
	if res.StatusCode != http.StatusOK || strings.Contains(body, `name="override"`) {
		t.Fatalf("override move = %d, body %q", res.StatusCode, body)
	}

	res, body = s.get("/app/applications/" + f.appID.String())
	if res.StatusCode != http.StatusOK {
		t.Fatalf("application page = %d", res.StatusCode)
	}
	for _, want := range []string{"Moved from Applied to Phone screen", "Moved from Phone screen to Take-home (prerequisite overridden)", "screened on the phone", "Take-home"} {
		if !strings.Contains(body, want) {
			t.Errorf("application page lacks %q", want)
		}
	}
}

func TestApplicationPageMoveFormRejectsWithoutAReason(t *testing.T) {
	f := newFixture(t)
	s := f.browser(t)
	s.login(f.recruiterEmail)
	s.get("/app/applications/" + f.appID.String())

	res, body := s.post(f.movePath(), url.Values{"to_stage_id": {f.rejected.String()}}, false)
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "a reason is required") {
		t.Fatalf("reject without reason = %d, body %q", res.StatusCode, body)
	}
	res, _ = s.post(f.movePath(), url.Values{"to_stage_id": {f.rejected.String()}, "reason": {"not a fit"}}, false)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("reject = %d, want a redirect", res.StatusCode)
	}
	_, body = s.get("/app/applications/" + f.appID.String())
	if !strings.Contains(body, "rejected") || !strings.Contains(body, "not a fit") || strings.Contains(body, `id="move"`) {
		t.Fatalf("rejected application page: %q", body)
	}
}

func TestVetterCannotMoveOutOfAGenericStage(t *testing.T) {
	f := newFixture(t)
	s := f.browser(t)
	s.login(f.vetterEmail)
	if res, _ := s.get("/app/pipeline/" + f.jobID.String()); res.StatusCode != http.StatusOK {
		t.Errorf("vetter board = %d, want 200", res.StatusCode)
	}
	res, _ := s.post(f.movePath(), url.Values{"to_stage_id": {f.interview.String()}}, false)
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("vetter move = %d, want 403", res.StatusCode)
	}
}

func TestListFiltersAndReleaseEvents(t *testing.T) {
	f := newFixture(t)
	s := f.browser(t)
	s.login(f.recruiterEmail)

	res, body := s.get("/app/pipeline/" + f.jobID.String() + "/list?q=ada")
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "Ada Lovelace") {
		t.Fatalf("list = %d, body %q", res.StatusCode, body)
	}
	if _, body := s.get("/app/pipeline/" + f.jobID.String() + "/list?stage=" + f.interview.String()); strings.Contains(body, "Ada Lovelace") {
		t.Error("stage filter still listed a candidate in another stage")
	}
	if _, body := s.get("/app/pipeline/" + f.jobID.String() + "/list?status=hired"); strings.Contains(body, "Ada Lovelace") {
		t.Error("status filter still listed an active candidate")
	}

	if res, _ := s.post("/app/applications/"+f.appID.String()+"/release", url.Values{}, false); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("release = %d", res.StatusCode)
	}
	_, body = s.get("/app/applications/" + f.appID.String())
	if !strings.Contains(body, "Released to the client") || !strings.Contains(body, "Hide from client") {
		t.Errorf("released page: %q", body)
	}
	if res, _ := s.post("/app/applications/"+f.appID.String()+"/unrelease", url.Values{}, false); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("unrelease = %d", res.StatusCode)
	}
	_, body = s.get("/app/applications/" + f.appID.String())
	if !strings.Contains(body, "Hidden from the client") {
		t.Errorf("unreleased page lacks the event: %q", body)
	}
}

func TestPipelineScreensNeedASession(t *testing.T) {
	f := newFixture(t)
	s := f.browser(t)
	res, _ := s.get("/app/pipeline/" + f.jobID.String())
	if res.StatusCode != http.StatusSeeOther || !strings.Contains(res.Header.Get("Location"), "/app/login") {
		t.Fatalf("anonymous board = %d %s, want a login redirect", res.StatusCode, res.Header.Get("Location"))
	}
}

func TestBoardDragShowsARefusedMove(t *testing.T) {
	f := newFixture(t)
	s := f.browser(t)
	s.login(f.vetterEmail)
	s.get("/app/pipeline/" + f.jobID.String())
	res, body := s.post(f.movePath(), url.Values{"to_stage_id": {f.interview.String()}, "view": {"board"}}, true)
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "not permitted") || strings.Contains(body, `name="reason"`) {
		t.Fatalf("refused drag = %d, want 422 with the message and no retry form; body %q", res.StatusCode, body)
	}
}
