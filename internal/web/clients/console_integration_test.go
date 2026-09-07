//go:build integration

package clients_test

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
	"recruiting/internal/web/clients"
	"recruiting/internal/web/layout"
	"recruiting/internal/web/middleware"
	"recruiting/internal/web/workqueue"
)

const testPassword = "hunter2-long-enough"

// fixture is one client with two jobs, a scored sitting waiting on a review,
// and the console's two new screens mounted the way serve mounts them.
type fixture struct {
	srv            *httptest.Server
	sys            *pgxpool.Pool
	orgID          uuid.UUID
	companyID      uuid.UUID
	goJob, rustJob uuid.UUID
	scoredAttempt  uuid.UUID
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

	f := &fixture{sys: sys, orgID: uuid.New(), companyID: uuid.New(), goJob: uuid.New(), rustJob: uuid.New()}
	f.recruiterEmail = "rec-" + f.orgID.String() + "@example.com"
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := sys.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`insert into org (id, name, slug) values ($1, $2, $2)`, f.orgID, f.orgID.String())
	t.Cleanup(func() { _, _ = sys.Exec(context.Background(), `delete from org where id = $1`, f.orgID) })
	hash, _ := service.HashPassword(testPassword)
	userID := uuid.New()
	exec(`insert into org_user (id, org_id, email, name) values ($1, $2, $3, 'Lucas Ruiz')`, userID, f.orgID, f.recruiterEmail)
	exec(`insert into org_user_role (org_user_id, org_id, role) values ($1, $2, $3)`, userID, f.orgID, service.RoleRecruiter)
	exec(`insert into org_user_credential (org_user_id, org_id, password_hash) values ($1, $2, $3)`, userID, f.orgID, hash)

	exec(`insert into client_company (id, org_id, name, industry) values ($1, $2, 'Globex', 'Fintech')`, f.companyID, f.orgID)
	assessmentID := uuid.New()
	exec(`insert into assessment (id, org_id, name, duration_minutes) values ($1, $2, 'Screen', 60)`, assessmentID, f.orgID)

	// Two jobs of one client running the same stage names: what the board
	// has to merge.
	for _, job := range []struct {
		id    uuid.UUID
		title string
	}{{f.goJob, "Go Engineer"}, {f.rustJob, "Rust Engineer"}} {
		exec(`insert into job (id, org_id, client_company_id, title, slug, status, created_by) values ($1, $2, $3, $4, $5, 'open', $6)`,
			job.id, f.orgID, f.companyID, job.title, job.id.String(), userID)
		exec(`insert into stage (id, org_id, job_id, position, name, kind) values ($1, $2, $3, 1, 'Screening', 'generic')`, uuid.New(), f.orgID, job.id)
	}
	var goStage uuid.UUID
	if err := sys.QueryRow(ctx, `select id from stage where job_id = $1`, f.goJob).Scan(&goStage); err != nil {
		t.Fatal(err)
	}
	var rustStage uuid.UUID
	if err := sys.QueryRow(ctx, `select id from stage where job_id = $1`, f.rustJob).Scan(&rustStage); err != nil {
		t.Fatal(err)
	}
	appID := uuid.New()
	candID := uuid.New()
	exec(`insert into candidate (id, org_id, email, name) values ($1, $2, $3, 'Ada Lovelace')`, candID, f.orgID, candID.String()+"@example.com")
	exec(`insert into application (id, org_id, job_id, candidate_id, client_company_id, stage_id) values ($1, $2, $3, $4, $5, $6)`,
		appID, f.orgID, f.goJob, candID, f.companyID, goStage)
	otherApp, otherCand := uuid.New(), uuid.New()
	exec(`insert into candidate (id, org_id, email, name) values ($1, $2, $3, 'Bo Second')`, otherCand, f.orgID, otherCand.String()+"@example.com")
	exec(`insert into application (id, org_id, job_id, candidate_id, client_company_id, stage_id) values ($1, $2, $3, $4, $5, $6)`,
		otherApp, f.orgID, f.rustJob, otherCand, f.companyID, rustStage)
	f.scoredAttempt = uuid.New()
	exec(`insert into attempt (id, org_id, application_id, assessment_id, stage_id, status, score, finished_at)
		values ($1, $2, $3, $4, $5, 'scored', 88, now())`, f.scoredAttempt, f.orgID, appID, assessmentID, goStage)

	q, err := queue.New(st.Pool(), queue.Config{})
	if err != nil {
		t.Fatal(err)
	}
	applications := service.NewApplicationService(st, q, "https://example.test")
	workQueue := service.NewWorkQueueService(st)
	org := service.NewOrgService(st)

	mux := chi.NewMux()
	if _, err := auth.Mount(mux, auth.Deps{Auth: service.NewAuthService(st), CookieSecret: []byte("test-secret")}); err != nil {
		t.Fatal(err)
	}
	layout.MountStatic(mux)
	app := chi.NewMux()
	app.Use(layout.WithCounts(workQueue.NavCounts, nil))
	workqueue.Mount(app, workqueue.Deps{Queue: workQueue, Org: org})
	clients.Mount(app, clients.Deps{Clients: service.NewClientService(st, applications), Org: org})
	mux.Mount("/", app)
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
	s.get("/app/login")
	form := url.Values{"email": {f.recruiterEmail}, "password": {testPassword}, middleware.CSRFField: {s.csrf()}}
	res, err := s.cli.PostForm(f.srv.URL+"/app/login", form)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("login: %d", res.StatusCode)
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

// csrf is the token the double-submit cookie carries.
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

func mustContain(t *testing.T, body string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Errorf("the page is missing %q", want)
		}
	}
}

func TestWorkQueueScreenListsTheWorkWithItsAction(t *testing.T) {
	f := newFixture(t)
	s := f.browser(t)
	res, body := s.get(workqueue.Prefix)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d", workqueue.Prefix, res.StatusCode)
	}
	mustContain(t, body,
		"Ada Lovelace", "Go Engineer", "Globex",
		"/app/reviews/"+f.scoredAttempt.String(),
		"Snooze 24h",
	)
	// The sidebar badge comes from the same read, through the middleware.
	mustContain(t, body, `<span class="nav-count">1</span>`)
}

func TestWorkQueueSnoozeTakesTheRowOffTheScreen(t *testing.T) {
	f := newFixture(t)
	s := f.browser(t)
	res, _ := s.post(workqueue.SnoozePath, url.Values{
		"kind": {string(service.QueueReview)}, "subject_id": {f.scoredAttempt.String()},
	})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("snooze = %d", res.StatusCode)
	}
	_, body := s.get(workqueue.Prefix)
	if strings.Contains(body, f.scoredAttempt.String()) {
		t.Fatal("the snoozed row is still on the screen")
	}
	mustContain(t, body, "Nothing is waiting on you")
}

func TestClientsScreenListsTheAccount(t *testing.T) {
	f := newFixture(t)
	s := f.browser(t)
	res, body := s.get(clients.Prefix)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d", clients.Prefix, res.StatusCode)
	}
	mustContain(t, body, "Globex", "Fintech", "Lucas Ruiz", "Median days to shortlist", clients.Path(f.companyID))
}

func TestClientDetailTabsRenderJobsBoardAndShortlists(t *testing.T) {
	f := newFixture(t)
	s := f.browser(t)

	_, jobs := s.get(clients.Path(f.companyID))
	mustContain(t, jobs, "Go Engineer", "Rust Engineer", "Applicants")

	_, board := s.get(clients.TabPath(f.companyID, clients.TabBoard))
	mustContain(t, board, "Screening", "Ada Lovelace", "Bo Second", "All jobs")
	if n := strings.Count(board, ">Screening "); n != 1 {
		t.Fatalf("the board draws %d Screening columns, want the jobs merged into one", n)
	}

	_, scoped := s.get(clients.BoardPath(f.companyID, f.rustJob))
	if strings.Contains(scoped, "Ada Lovelace") {
		t.Fatal("scoping the board to one job still shows the other job's cards")
	}
	mustContain(t, scoped, "Bo Second")

	_, packets := s.get(clients.TabPath(f.companyID, clients.TabShortlists))
	mustContain(t, packets, "No shortlist has been built")
}

func TestClientDetailIsNotFoundForAnotherOrgsCompany(t *testing.T) {
	f := newFixture(t)
	s := f.browser(t)
	res, _ := s.get(clients.Path(uuid.New()))
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("GET an unknown client = %d, want 404", res.StatusCode)
	}
}
