//go:build integration

package jobs_test

import (
	"context"
	"html"
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
	"recruiting/internal/web/assess"
	"recruiting/internal/web/auth"
	"recruiting/internal/web/jobs"
	"recruiting/internal/web/layout"
	"recruiting/internal/web/middleware"
	"recruiting/internal/web/pool"
	"recruiting/internal/web/scorecards"
)

const testPassword = "hunter2-long-enough"

type fixture struct {
	srv                         *httptest.Server
	sys                         *pgxpool.Pool
	orgID, companyID            uuid.UUID
	recruiterEmail, vetterEmail string
	password                    string
	defaultStageCount           int
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

	f := &fixture{sys: sys, orgID: uuid.New(), companyID: uuid.New(), password: testPassword}
	f.recruiterEmail = "rec-" + f.orgID.String() + "@example.com"
	f.vetterEmail = "vet-" + f.orgID.String() + "@example.com"
	hash, _ := service.HashPassword(f.password)
	if _, err := sys.Exec(ctx, `insert into org (id, name, slug) values ($1, $2, $2)`, f.orgID, f.orgID.String()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = sys.Exec(context.Background(), `delete from org where id = $1`, f.orgID) })
	for _, seed := range []struct {
		email, role string
	}{{f.recruiterEmail, service.RoleRecruiter}, {f.vetterEmail, service.RoleVetter}} {
		id := uuid.New()
		if _, err := sys.Exec(ctx, `insert into org_user (id, org_id, email, name) values ($1, $2, $3, $3)`, id, f.orgID, seed.email); err != nil {
			t.Fatal(err)
		}
		if _, err := sys.Exec(ctx, `insert into org_user_role (org_user_id, org_id, role) values ($1, $2, $3)`, id, f.orgID, seed.role); err != nil {
			t.Fatal(err)
		}
		if _, err := sys.Exec(ctx, `insert into org_user_credential (org_user_id, org_id, password_hash) values ($1, $2, $3)`, id, f.orgID, hash); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := sys.Exec(ctx, `insert into client_company (id, org_id, name) values ($1, $2, 'Globex')`, f.companyID, f.orgID); err != nil {
		t.Fatal(err)
	}
	templateID := uuid.New()
	if _, err := sys.Exec(ctx, `insert into pipeline_template (id, org_id, name, is_default) values ($1, $2, 'Default', true)`, templateID, f.orgID); err != nil {
		t.Fatal(err)
	}
	for i, s := range service.DefaultPipelineStages {
		_, err := sys.Exec(ctx, `insert into pipeline_template_stage (org_id, template_id, position, name, kind, unblind)
			values ($1, $2, $3, $4, $5, $6)`, f.orgID, templateID, i+1, s.Name, s.Kind, s.Unblind)
		if err != nil {
			t.Fatal(err)
		}
	}
	f.defaultStageCount = len(service.DefaultPipelineStages)

	mux := chi.NewMux()
	if _, err := auth.Mount(mux, auth.Deps{Auth: service.NewAuthService(st), CookieSecret: []byte("test-secret")}); err != nil {
		t.Fatal(err)
	}
	layout.MountStatic(mux)
	jobs.Mount(mux, jobs.Deps{Jobs: service.NewJobService(st), Postings: service.NewJobPostingService(st, nil, "https://example.test/"), Org: service.NewOrgService(st)})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
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

// postHTMX posts the way the pipeline editor's Alpine form does, so the
// handler answers with the fragment rather than the whole page.
func (s *session) postHTMX(path string, form url.Values) (*http.Response, string) {
	s.t.Helper()
	form.Set(middleware.CSRFField, s.csrf())
	req, err := http.NewRequest(http.MethodPost, s.f.srv.URL+path, strings.NewReader(form.Encode()))
	if err != nil {
		s.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	res, err := s.cli.Do(req)
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
	res, body := s.post("/app/login", url.Values{"email": {email}, "password": {s.f.password}})
	if res.StatusCode != http.StatusSeeOther {
		s.t.Fatalf("login as %s: %d %s", email, res.StatusCode, body)
	}
}

func (f *fixture) jobForm() url.Values {
	return url.Values{
		"client_company_id": {f.companyID.String()},
		"title":             {"Senior Go Engineer"},
		"description":       {"## About\nWe build things."},
		"skills":            {"go, postgres"},
		"seniority":         {service.SenioritySenior},
		"location":          {"Berlin"},
		"remote_policy":     {service.RemoteHybrid},
		"salary_min":        {"80000"},
		"salary_max":        {"110000"},
		"status":            {service.JobOpen},
	}
}

var stageIDPattern = regexp.MustCompile(`/stages/([0-9a-f-]{36})"`)

func TestRecruiterCreatesAJobAndEditsItsPipeline(t *testing.T) {
	f := newFixture(t)
	b := f.browser(t)
	b.login(f.recruiterEmail)

	if res, body := b.get("/app/jobs"); res.StatusCode != 200 || !strings.Contains(body, "No jobs yet") {
		t.Fatalf("empty job list: %d %s", res.StatusCode, body)
	}
	if res, body := b.get("/app/jobs/new"); res.StatusCode != 200 || !strings.Contains(body, "Globex") {
		t.Fatalf("new job form: %d %s", res.StatusCode, body)
	}

	res, body := b.post("/app/jobs", f.jobForm())
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("create job: %d %s", res.StatusCode, body)
	}
	pipelinePath := res.Header.Get("Location")
	if !strings.HasSuffix(pipelinePath, "/pipeline") {
		t.Fatalf("create redirected to %q, want the pipeline editor", pipelinePath)
	}
	jobPath := strings.TrimSuffix(pipelinePath, "/pipeline")

	// The list shows it, and the form round-trips what was typed.
	if _, body := b.get("/app/jobs"); !strings.Contains(body, "Senior Go Engineer") || !strings.Contains(body, "Globex") {
		t.Errorf("job list: %s", body)
	}
	_, body = b.get(jobPath)
	for _, want := range []string{"senior-go-engineer", "go, postgres", "We build things.", `value="80000"`} {
		if !strings.Contains(body, want) {
			t.Errorf("job form missing %q", want)
		}
	}

	// The pipeline is a copy of the org's default template.
	res, body = b.get(pipelinePath)
	if res.StatusCode != 200 {
		t.Fatalf("pipeline: %d %s", res.StatusCode, body)
	}
	for _, s := range service.DefaultPipelineStages {
		if !strings.Contains(body, s.Name) {
			t.Errorf("pipeline is missing the template's %q stage", s.Name)
		}
	}
	if !strings.Contains(body, "stageOrder(") || !strings.Contains(body, `x-for="(s, i) in stages"`) {
		t.Errorf("no Alpine reorder component on the page: %s", body)
	}

	ids := stageIDPattern.FindAllStringSubmatch(body, -1)
	seen := map[string]bool{}
	order := make([]string, 0, len(ids))
	for _, m := range ids {
		if !seen[m[1]] {
			seen[m[1]] = true
			order = append(order, m[1])
		}
	}
	if len(order) != f.defaultStageCount {
		t.Fatalf("found %d stage ids, want %d", len(order), f.defaultStageCount)
	}

	// Reordering posts the whole order over htmx and comes back as a fragment.
	order[0], order[1] = order[1], order[0]
	form := url.Values{"stage": order}
	res, body = b.postHTMX(jobPath+"/stages/order", form)
	if res.StatusCode != 200 {
		t.Fatalf("reorder: %d %s", res.StatusCode, body)
	}
	if strings.Contains(body, "<html") {
		t.Errorf("htmx reorder returned a whole page, not the fragment")
	}
	_, body = b.get(pipelinePath)
	first := stageIDPattern.FindAllStringSubmatch(body, 1)
	if len(first) == 0 || first[0][1] != order[0] {
		t.Errorf("reorder did not persist: first stage is %v, want %s", first, order[0])
	}

	// A second hired terminal is refused, inline and without changing anything.
	res, body = b.post(jobPath+"/stages", url.Values{
		"name": {"Hired again"}, "kind": {"terminal"}, "terminal_status": {"hired"},
	})
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "hired") {
		t.Fatalf("second hired stage: %d %s", res.StatusCode, body)
	}
	_, body = b.get(pipelinePath)
	if strings.Contains(body, "Hired again") {
		t.Errorf("the rejected stage was written anyway")
	}

	// A new non-terminal stage lands in front of the terminals, not after them.
	res, body = b.post(jobPath+"/stages", url.Values{"name": {"Take-home"}, "kind": {"assessment"}})
	if res.StatusCode != 200 || !strings.Contains(body, "Take-home") {
		t.Fatalf("add stage: %d %s", res.StatusCode, body)
	}
	if strings.Index(body, "Take-home") > strings.Index(body, `value="hired" selected`) {
		t.Errorf("the added stage sits after the terminal stages: %s", body)
	}
	// An unknown job is a 404, not a silent success on an empty pipeline.
	if res, _ := b.post("/app/jobs/"+uuid.NewString()+"/stages", url.Values{"name": {"Ghost"}, "kind": {"generic"}}); res.StatusCode != http.StatusNotFound {
		t.Errorf("add stage to an unknown job: %d, want 404", res.StatusCode)
	}
	if res, _ := b.postHTMX("/app/jobs/"+uuid.NewString()+"/stages/order", url.Values{}); res.StatusCode != http.StatusNotFound {
		t.Errorf("reorder an unknown job: %d, want 404", res.StatusCode)
	}
	// Renaming persists.
	stageID := stageIDPattern.FindStringSubmatch(body)[1]
	res, body = b.post(jobPath+"/stages/"+stageID, url.Values{"name": {"Inbox"}, "kind": {"generic"}})
	if res.StatusCode != 200 || !strings.Contains(body, "Inbox") {
		t.Fatalf("rename stage: %d %s", res.StatusCode, body)
	}

	// The saved job page pulls the talent pool's suggestions for the job.
	jobID := strings.TrimPrefix(jobPath, "/app/jobs/")
	if _, body := b.get(jobPath); !strings.Contains(body, `hx-get="`+pool.SuggestionsPath(uuid.MustParse(jobID))+`"`) {
		t.Errorf("job page does not embed the pool suggestions: %s", body)
	}
}

func TestStageEditorLinksSetupAndSetsTheDefaultInterviewer(t *testing.T) {
	f := newFixture(t)
	b := f.browser(t)
	b.login(f.recruiterEmail)
	res, _ := b.post("/app/jobs", f.jobForm())
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("create job: %d", res.StatusCode)
	}
	pipelinePath := res.Header.Get("Location")
	jobPath := strings.TrimSuffix(pipelinePath, "/pipeline")
	jobID := uuid.MustParse(strings.TrimPrefix(jobPath, "/app/jobs/"))

	var interview, assessment uuid.UUID
	if err := f.sys.QueryRow(context.Background(), `select id from stage where job_id = $1 and kind = 'interview' order by position limit 1`, jobID).Scan(&interview); err != nil {
		t.Fatalf("interview stage: %v", err)
	}
	if err := f.sys.QueryRow(context.Background(), `select id from stage where job_id = $1 and kind = 'assessment' order by position limit 1`, jobID).Scan(&assessment); err != nil {
		t.Fatalf("assessment stage: %v", err)
	}
	var vetterID uuid.UUID
	if err := f.sys.QueryRow(context.Background(), `select id from org_user where email = $1`, f.vetterEmail).Scan(&vetterID); err != nil {
		t.Fatal(err)
	}

	_, body := b.get(pipelinePath)
	for _, want := range []string{
		`href="` + scorecards.RubricPath(jobID, interview) + `"`,
		`href="` + html.EscapeString(assess.AttachStagePath(jobID, assessment)) + `"`,
		`name="default_vetter_id"`, `value="` + vetterID.String() + `"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("stage editor lacks %q", want)
		}
	}
	if strings.Count(body, `name="default_vetter_id"`) != 1 {
		t.Errorf("the default interviewer select should be on the interview stage only: %s", body)
	}

	res, body = b.post(jobPath+"/stages/"+interview.String(), url.Values{
		"name": {"Phone screen"}, "kind": {"interview"}, "default_vetter_id": {vetterID.String()},
	})
	if res.StatusCode != 200 {
		t.Fatalf("set default vetter: %d %s", res.StatusCode, body)
	}
	if !strings.Contains(body, `value="`+vetterID.String()+`" selected`) {
		t.Errorf("editor does not show the saved default: %s", body)
	}
	var saved uuid.NullUUID
	if err := f.sys.QueryRow(context.Background(), `select default_vetter_id from stage where id = $1`, interview).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	if !saved.Valid || saved.UUID != vetterID {
		t.Errorf("stage default_vetter_id = %v, want %s", saved, vetterID)
	}
	// Clearing the select clears the default.
	if res, _ := b.post(jobPath+"/stages/"+interview.String(), url.Values{"name": {"Phone screen"}, "kind": {"interview"}}); res.StatusCode != 200 {
		t.Fatalf("clear default vetter: %d", res.StatusCode)
	}
	if err := f.sys.QueryRow(context.Background(), `select default_vetter_id from stage where id = $1`, interview).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	if saved.Valid {
		t.Errorf("default_vetter_id still set after clearing: %v", saved)
	}
}

func TestJobFormShowsValidationErrorsInline(t *testing.T) {
	f := newFixture(t)
	b := f.browser(t)
	b.login(f.recruiterEmail)

	form := f.jobForm()
	form.Set("title", "  ")
	res, body := b.post("/app/jobs", form)
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "needs a title") {
		t.Fatalf("blank title: %d %s", res.StatusCode, body)
	}
	if !strings.Contains(body, `name="description"`) {
		t.Errorf("the rejected form was not redrawn: %s", body)
	}

	form = f.jobForm()
	form.Set("seniority", "wizard")
	if res, body := b.post("/app/jobs", form); res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "wizard") {
		t.Errorf("unknown seniority: %d %s", res.StatusCode, body)
	}

	form = f.jobForm()
	form.Set("salary_min", "200000")
	if res, body := b.post("/app/jobs", form); res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "salary band") {
		t.Errorf("inverted salary band: %d %s", res.StatusCode, body)
	}

	// The first job takes the slug; a second with the same title is refused.
	if res, body := b.post("/app/jobs", f.jobForm()); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("create job: %d %s", res.StatusCode, body)
	}
	if res, body := b.post("/app/jobs", f.jobForm()); res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "slug") {
		t.Errorf("duplicate slug: %d %s", res.StatusCode, body)
	}
}

func TestJobScreensRequireRecruiterOrAdminToChangeAnything(t *testing.T) {
	f := newFixture(t)
	rec0 := f.browser(t)
	rec0.login(f.recruiterEmail)
	res0, _ := rec0.post("/app/jobs", f.jobForm())
	jobPath := strings.TrimSuffix(res0.Header.Get("Location"), "/pipeline")

	// A vetter reads jobs and pipelines but cannot change them.
	vet := f.browser(t)
	vet.login(f.vetterEmail)
	for _, path := range []string{"/app/jobs", jobPath, jobPath + "/pipeline"} {
		if res, body := vet.get(path); res.StatusCode != http.StatusOK {
			t.Errorf("vetter GET %s: %d %s", path, res.StatusCode, body)
		}
	}
	if res, _ := vet.get("/app/jobs/new"); res.StatusCode != http.StatusForbidden {
		t.Errorf("vetter on the new-job form: %d, want 403", res.StatusCode)
	}
	if res, _ := vet.post(jobPath+"/stages", url.Values{"name": {"X"}, "kind": {"generic"}}); res.StatusCode != http.StatusForbidden {
		t.Errorf("vetter adding a stage: %d, want 403", res.StatusCode)
	}
	anon := f.browser(t)
	if res, _ := anon.get("/app/jobs"); res.StatusCode != http.StatusSeeOther {
		t.Errorf("anonymous: %d, want a redirect to login", res.StatusCode)
	}
	// The app surface's landing page reaches the job list once signed in.
	rec := f.browser(t)
	rec.login(f.recruiterEmail)
	res, _ := rec.get("/app/")
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/app/jobs" {
		t.Errorf("/app/ = %d %s", res.StatusCode, res.Header.Get("Location"))
	}
}

func TestDeletingAStageHoldingApplicationsIsRefusedInline(t *testing.T) {
	f := newFixture(t)
	b := f.browser(t)
	b.login(f.recruiterEmail)
	res, _ := b.post("/app/jobs", f.jobForm())
	jobPath := strings.TrimSuffix(res.Header.Get("Location"), "/pipeline")
	jobID := uuid.MustParse(strings.TrimPrefix(jobPath, "/app/jobs/"))

	_, body := b.get(jobPath + "/pipeline")
	stageID := uuid.MustParse(stageIDPattern.FindStringSubmatch(body)[1])

	ctx := context.Background()
	candidateID := uuid.New()
	_, err := f.sys.Exec(ctx, `insert into candidate (id, org_id, email, name) values ($1, $2, $3, 'Ada')`,
		candidateID, f.orgID, candidateID.String()+"@example.com")
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.sys.Exec(ctx, `insert into application (org_id, job_id, candidate_id, client_company_id, stage_id)
		values ($1, $2, $3, $4, $5)`, f.orgID, jobID, candidateID, f.companyID, stageID)
	if err != nil {
		t.Fatal(err)
	}

	res, body = b.post(jobPath+"/stages/"+stageID.String()+"/delete", url.Values{})
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "still holds applications") {
		t.Fatalf("delete occupied stage: %d %s", res.StatusCode, body)
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

// The postings panel shows the ad the platform wrote for each board and
// queues a placement; the worker's browser does the rest, off the page.
func TestPostingsPanelPreviewsAndQueuesAPlacement(t *testing.T) {
	f := newFixture(t)
	jobID := uuid.New()
	if _, err := f.sys.Exec(context.Background(), `insert into job (id, org_id, client_company_id, title, slug, status, skills, seniority, location, remote_policy, salary_min, salary_max)
		values ($1, $2, $3, 'Backend Engineer (Go)', 'backend-go', 'open', '{go,postgres}', 'senior', 'Berlin', 'hybrid', 85000, 105000)`, jobID, f.orgID, f.companyID); err != nil {
		t.Fatal(err)
	}
	b := f.browser(t)
	b.login(f.recruiterEmail)
	res, body := b.get(jobs.Prefix + "/" + jobID.String() + "/postings")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET postings = %d", res.StatusCode)
	}
	for _, want := range []string{"Post to LinkedIn", "Post to Demo board (local)", "Globex is hiring a senior backend engineer (Go).", "€85,000 to €105,000 a year", "Not posted anywhere yet"} {
		if !strings.Contains(body, want) {
			t.Errorf("the panel lacks %q", want)
		}
	}
	// The post redirects back to the panel so a reload never posts twice.
	res, _ = b.post(jobs.Prefix+"/"+jobID.String()+"/postings", url.Values{"board": {"demo"}})
	if res.StatusCode != http.StatusSeeOther || !strings.Contains(res.Header.Get("Location"), "/postings?done=") {
		t.Fatalf("POST posting = %d to %q, want a redirect to the panel", res.StatusCode, res.Header.Get("Location"))
	}
	res, body = b.get(res.Header.Get("Location"))
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "Queued for Demo board (local)") || !strings.Contains(body, "posting-queued") {
		t.Fatalf("GET after posting = %d, body lacks the queued row", res.StatusCode)
	}
	res, body = b.post(jobs.Prefix+"/"+jobID.String()+"/postings", url.Values{"board": {"craigslist"}})
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "not a board") {
		t.Fatalf("POST to an unknown board = %d", res.StatusCode)
	}
}
