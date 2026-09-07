//go:build integration

package assess_test

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
	"recruiting/internal/web/assess"
	"recruiting/internal/web/auth"
	"recruiting/internal/web/layout"
	"recruiting/internal/web/middleware"
)

const invitePassword = "hunter2-long-enough"

type invitesFixture struct {
	srv            *httptest.Server
	recruiterEmail string
	waiting        uuid.UUID
	scored         uuid.UUID
}

// newInvitesFixture seeds one org with two sittings on two candidates: one
// still waiting to be started, one already scored. The Assessments screen is
// meant to tell them apart.
func newInvitesFixture(t *testing.T) *invitesFixture {
	t.Helper()
	ownerURL := os.Getenv("DATABASE_URL")
	if ownerURL == "" {
		t.Fatal("DATABASE_URL is not set; run `make dev-up` and use `make test-integration`")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	lock, err := pgx.Connect(ctx, ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lock.Exec(ctx, "select pg_advisory_lock_shared($1)", 7371); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close(context.Background()) })
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

	orgID := uuid.New()
	f := &invitesFixture{waiting: uuid.New(), scored: uuid.New(), recruiterEmail: "rec-" + orgID.String() + "@example.com"}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := sys.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`insert into org (id, name, slug) values ($1, $2, $2)`, orgID, orgID.String())
	t.Cleanup(func() { _, _ = sys.Exec(context.Background(), `delete from org where id = $1`, orgID) })
	hash, _ := service.HashPassword(invitePassword)
	recruiterID := uuid.New()
	exec(`insert into org_user (id, org_id, email, name) values ($1, $2, $3, 'Rita Recruiter')`, recruiterID, orgID, f.recruiterEmail)
	exec(`insert into org_user_role (org_user_id, org_id, role) values ($1, $2, $3)`, recruiterID, orgID, service.RoleRecruiter)
	exec(`insert into org_user_credential (org_user_id, org_id, password_hash) values ($1, $2, $3)`, recruiterID, orgID, hash)

	companyID, jobID, stageID, assessmentID, problemID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	exec(`insert into client_company (id, org_id, name) values ($1, $2, 'Globex')`, companyID, orgID)
	exec(`insert into job (id, org_id, client_company_id, title, slug, status) values ($1, $2, $3, 'Senior Go Engineer', $4, 'open')`,
		jobID, orgID, companyID, "go-"+orgID.String())
	exec(`insert into assessment (id, org_id, name, duration_minutes) values ($1, $2, 'Take-home', 60)`, assessmentID, orgID)
	exec(`insert into problem (id, org_id, kind, title, statement, allowed_languages) values ($1, $2, 'code', 'Adder', 'Add them.', '{python}')`, problemID, orgID)
	exec(`insert into assessment_problem (assessment_id, org_id, problem_id, position) values ($1, $2, $3, 1)`, assessmentID, orgID, problemID)
	exec(`insert into stage (id, org_id, job_id, position, name, kind, assessment_id) values ($1, $2, $3, 1, 'Take-home', 'assessment', $4)`,
		stageID, orgID, jobID, assessmentID)

	seed := func(attemptID uuid.UUID, name, status string) {
		candID, appID := uuid.New(), uuid.New()
		exec(`insert into candidate (id, org_id, email, name) values ($1, $2, $3, $4)`, candID, orgID, name+"-"+orgID.String()+"@example.com", name)
		exec(`insert into application (id, org_id, job_id, candidate_id, client_company_id, stage_id) values ($1, $2, $3, $4, $5, $6)`,
			appID, orgID, jobID, candID, companyID, stageID)
		exec(`insert into attempt (id, org_id, application_id, assessment_id, stage_id, status, invite_expires_at)
		      values ($1, $2, $3, $4, $5, $6, now() + interval '3 days')`,
			attemptID, orgID, appID, assessmentID, stageID, status)
	}
	seed(f.waiting, "Ada Lovelace", service.AttemptInvited)
	seed(f.scored, "Grace Hopper", service.AttemptScored)
	// One submitted problem on the scored sitting, so the progress column has
	// arithmetic to do rather than a constant to print.
	var scoredApp uuid.UUID
	if err := sys.QueryRow(ctx, `select application_id from attempt where id = $1`, f.scored).Scan(&scoredApp); err != nil {
		t.Fatal(err)
	}
	exec(`insert into submission (id, org_id, attempt_id, problem_id, kind, language, source, status)
	      values ($1, $2, $3, $4, 'submit', 'python', 'print(3)', 'done')`, uuid.New(), orgID, f.scored, problemID)

	mux := chi.NewMux()
	if _, err := auth.Mount(mux, auth.Deps{Auth: service.NewAuthService(st), CookieSecret: []byte("test-secret")}); err != nil {
		t.Fatal(err)
	}
	layout.MountStatic(mux)
	assess.MountRecruiter(mux, assess.RecruiterDeps{
		Assessments: service.NewAssessmentService(st),
		Problems:    service.NewProblemService(st, passRunner{}),
		Jobs:        service.NewJobService(st),
		Attempts:    service.NewAttemptService(st, nil, "https://example.test"),
		Org:         service.NewOrgService(st),
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *invitesFixture) login(t *testing.T) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	cli := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := cli.Get(f.srv.URL + "/app/login")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	u, _ := url.Parse(f.srv.URL)
	csrf := ""
	for _, c := range jar.Cookies(u) {
		if c.Name == middleware.CSRFCookie {
			csrf = c.Value
		}
	}
	res, err = cli.PostForm(f.srv.URL+"/app/login", url.Values{
		"email": {f.recruiterEmail}, "password": {invitePassword}, middleware.CSRFField: {csrf},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("login = %d", res.StatusCode)
	}
	return cli
}

func get(t *testing.T, cli *http.Client, url string) (int, string) {
	t.Helper()
	res, err := cli.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func TestAssessmentsScreenListsInvitesAndFiltersByStatus(t *testing.T) {
	f := newInvitesFixture(t)
	cli := f.login(t)

	code, body := get(t, cli, f.srv.URL+assess.Prefix)
	if code != http.StatusOK {
		t.Fatalf("assessments screen = %d", code)
	}
	for _, want := range []string{"Ada Lovelace", "Grace Hopper", "Invites in flight", "Take-home", "0 of 1", "1 of 1"} {
		if !strings.Contains(body, want) {
			t.Errorf("assessments screen lacks %q", want)
		}
	}

	code, body = get(t, cli, f.srv.URL+assess.Prefix+"?status="+service.AttemptInvited)
	if code != http.StatusOK || !strings.Contains(body, "Ada Lovelace") {
		t.Fatalf("invited filter = %d, missing the waiting sitting", code)
	}
	if strings.Contains(body, "Grace Hopper") {
		t.Error("the invited filter still lists a scored sitting")
	}

	// The problem sets moved one level in; the screen itself is the invites.
	if code, body := get(t, cli, f.srv.URL+assess.SetsPath); code != http.StatusOK || !strings.Contains(body, "Problem sets") {
		t.Fatalf("problem sets = %d: %s", code, body)
	}
}
