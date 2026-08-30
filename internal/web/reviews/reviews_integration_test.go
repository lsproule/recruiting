//go:build integration

package reviews_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"recruiting/internal/api"
	"recruiting/internal/service"
	"recruiting/internal/store"
	"recruiting/internal/web/auth"
	"recruiting/internal/web/layout"
	"recruiting/internal/web/middleware"
	"recruiting/internal/web/reviews"
)

const testPassword = "hunter2-long-enough"

type fixture struct {
	srv                     *httptest.Server
	orgID, jobID, appID     uuid.UUID
	assessStage, attemptID  uuid.UUID
	problemID               uuid.UUID
	vetterID, otherVetterID uuid.UUID
	vetterEmail             string
	otherVetterEmail        string
	recruiterEmail          string
	sys                     *pgxpool.Pool
}

// hiddenExpected is the expected output of a hidden case. It must never
// appear in anything the review screens or the manifest serve.
const hiddenExpected = "hidden-case-expected-output-42"

// newFixture seeds an org whose application sits in an assessment stage with
// one scored attempt, its recording, and one integrity signal, and serves the
// review screens beside the JSON API the replay island reads.
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
		sys: sys, orgID: uuid.New(), jobID: uuid.New(), appID: uuid.New(),
		assessStage: uuid.New(), attemptID: uuid.New(), problemID: uuid.New(),
		vetterID: uuid.New(), otherVetterID: uuid.New(),
	}
	f.vetterEmail = "vet-" + f.orgID.String() + "@example.com"
	f.otherVetterEmail = "vet2-" + f.orgID.String() + "@example.com"
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
	for _, seed := range []struct {
		id    uuid.UUID
		email string
		role  string
	}{
		{f.vetterID, f.vetterEmail, service.RoleVetter},
		{f.otherVetterID, f.otherVetterEmail, service.RoleVetter},
		{uuid.New(), f.recruiterEmail, service.RoleRecruiter},
	} {
		exec(`insert into org_user (id, org_id, email, name) values ($1, $2, $3, $3)`, seed.id, f.orgID, seed.email)
		exec(`insert into org_user_role (org_user_id, org_id, role) values ($1, $2, $3)`, seed.id, f.orgID, seed.role)
		exec(`insert into org_user_credential (org_user_id, org_id, password_hash) values ($1, $2, $3)`, seed.id, f.orgID, hash)
	}
	companyID := uuid.New()
	exec(`insert into client_company (id, org_id, name) values ($1, $2, 'Globex')`, companyID, f.orgID)
	exec(`insert into job (id, org_id, client_company_id, title, slug, status) values ($1, $2, $3, 'Senior Go Engineer', $4, 'open')`,
		f.jobID, f.orgID, companyID, "go-"+f.orgID.String())
	assessmentID := uuid.New()
	exec(`insert into assessment (id, org_id, name, duration_minutes) values ($1, $2, 'Take-home', 60)`, assessmentID, f.orgID)
	exec(`insert into problem (id, org_id, kind, title, statement, allowed_languages) values ($1, $2, 'code', 'Adder', 'Add them.', '{python}')`,
		f.problemID, f.orgID)
	exec(`insert into test_case (id, org_id, problem_id, position, input, expected_output, visibility, weight) values ($1, $2, $3, 1, '1 2', '3', 'public', 1)`,
		uuid.New(), f.orgID, f.problemID)
	exec(`insert into test_case (id, org_id, problem_id, position, input, expected_output, visibility, weight) values ($1, $2, $3, 2, '2 2', $4, 'hidden', 2)`,
		uuid.New(), f.orgID, f.problemID, hiddenExpected)
	exec(`insert into assessment_problem (assessment_id, org_id, problem_id, position) values ($1, $2, $3, 1)`, assessmentID, f.orgID, f.problemID)
	exec(`insert into stage (id, org_id, job_id, position, name, kind, assessment_id) values ($1, $2, $3, 1, 'Take-home', 'assessment', $4)`,
		f.assessStage, f.orgID, f.jobID, assessmentID)
	exec(`insert into stage (id, org_id, job_id, position, name, kind, terminal_status) values ($1, $2, $3, 2, 'Rejected', 'terminal', 'rejected')`,
		uuid.New(), f.orgID, f.jobID)
	candID := uuid.New()
	exec(`insert into candidate (id, org_id, email, name) values ($1, $2, $3, 'Ada Lovelace')`, candID, f.orgID, "ada-"+f.orgID.String()+"@example.com")
	exec(`insert into application (id, org_id, job_id, candidate_id, client_company_id, stage_id, vetter_id) values ($1, $2, $3, $4, $5, $6, $7)`,
		f.appID, f.orgID, f.jobID, candID, companyID, f.assessStage, f.vetterID)

	scores := `[{"problem_id":"` + f.problemID.String() + `","score":0.75,"passed_weight":3,"total_weight":4,"status":"ok"}]`
	exec(`insert into attempt (id, org_id, application_id, assessment_id, stage_id, status, started_at, finished_at, score, risk_score,
	          problem_scores, error_count, recording_status, last_event_seq)
	      values ($1, $2, $3, $4, $5, 'scored', now() - interval '1 hour', now(), 75, 30, $6, 0, 'complete', 3)`,
		f.attemptID, f.orgID, f.appID, assessmentID, f.assessStage, scores)
	exec(`insert into attempt_source (attempt_id, org_id, problem_id, language, source) values ($1, $2, $3, 'python', 'print(3)')`,
		f.attemptID, f.orgID, f.problemID)
	exec(`insert into submission (id, org_id, attempt_id, problem_id, kind, language, source, status, score)
	      values ($1, $2, $3, $4, 'submit', 'python', 'print(3)', 'done', 0.75)`,
		uuid.New(), f.orgID, f.attemptID, f.problemID)
	for _, ev := range []struct {
		seq     int
		kind    string
		payload string
	}{
		{1, "focus", `{}`},
		{2, "edit", `{"changes":[[0,"print(3)"]]}`},
		{3, "paste", `{"len":8,"sha256":"a","internal":false}`},
	} {
		exec(`insert into attempt_event (id, org_id, attempt_id, seq, kind, payload, problem_id, client_ts) values ($1, $2, $3, $4, $5, $6, $7, now())`,
			uuid.New(), f.orgID, f.attemptID, ev.seq, ev.kind, ev.payload, f.problemID)
	}
	evidence := `[{"problem_id":"` + f.problemID.String() + `","seq":3,"note":"pasted 8 characters from outside the page"}]`
	exec(`insert into integrity_signal (id, org_id, attempt_id, name, value, weight, confidence, evidence) values ($1, $2, $3, 'paste_ratio', 0.8, 25, 'normal', $4)`,
		uuid.New(), f.orgID, f.attemptID, evidence)

	authService := service.NewAuthService(st)
	mux := chi.NewMux()
	r := api.NewRouter()
	api.MountReplay(r.API, api.ReplayDeps{
		Reviews: service.NewReviewService(st, service.NewPoolService(st), nil),
		Resolve: api.ResolveWith(middleware.Authenticate(authService)),
	})
	if _, err := auth.Mount(mux, auth.Deps{Auth: authService, CookieSecret: []byte("test-secret")}); err != nil {
		t.Fatal(err)
	}
	layout.MountStatic(mux)
	reviews.Mount(mux, reviews.Deps{Reviews: service.NewReviewService(st, service.NewPoolService(st), nil), Org: service.NewOrgService(st)})
	r.Mux.Mount("/", mux)
	f.srv = httptest.NewServer(r.Mux)
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

func TestTheVetterReviewsAnAttemptEndToEnd(t *testing.T) {
	f := newFixture(t)
	s := f.browser(t)
	s.login(f.vetterEmail)

	res, body := s.get(reviews.Prefix)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "Ada Lovelace") {
		t.Fatalf("review queue = %d: %s", res.StatusCode, body)
	}

	path := reviews.ReviewPath(f.attemptID)
	res, body = s.get(path)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("review page = %d: %s", res.StatusCode, body)
	}
	for _, want := range []string{"Pasted text", `data-replay-seq="3"`, `id="replay-config"`, "File verdict"} {
		if !strings.Contains(body, want) {
			t.Errorf("review page does not carry %q", want)
		}
	}
	if !strings.Contains(body, reviews.ManifestPath(f.attemptID)) {
		t.Error("the boot config does not carry the manifest URL")
	}
	if strings.Contains(body, "<script id=\"replay-config\" type=\"application/json\">{\"attempt_id\"") == false {
		t.Error("the boot config is not inlined as a JSON script element")
	}
	if strings.Contains(body, hiddenExpected) {
		t.Error("the review page carries a hidden case's expected output")
	}

	// The island's manifest, fetched with the same session cookie.
	res, body = s.get(reviews.ManifestPath(f.attemptID))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("manifest = %d: %s", res.StatusCode, body)
	}
	if strings.Contains(body, hiddenExpected) {
		t.Error("the manifest carries a hidden case's expected output")
	}
	var manifest api.ReplayManifest
	if err := json.Unmarshal([]byte(body), &manifest); err != nil {
		t.Fatalf("manifest %s: %v", body, err)
	}
	if manifest.NextAfterSeq != 0 {
		t.Errorf("next after seq = %d, want the end of a 3-event stream", manifest.NextAfterSeq)
	}
	if manifest.SnapshotEvery != service.ReplaySnapshotEvery || len(manifest.Events) != 3 {
		t.Errorf("manifest = %+v, want 3 events and a snapshot interval", manifest)
	}
	if len(manifest.Problems) != 1 || manifest.Problems[0].FinalSource != "print(3)" {
		t.Errorf("manifest problems = %+v, want the final source", manifest.Problems)
	}
	if len(manifest.Markers) != 1 || manifest.Markers[0].Seq != 3 {
		t.Errorf("manifest markers = %+v, want the paste at seq 3", manifest.Markers)
	}

	res, body = s.post(path, url.Values{"verdict": {service.VerdictPass}, "notes": {"clean work"}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("file verdict = %d: %s", res.StatusCode, body)
	}
	var verdict, status string
	if err := f.sys.QueryRow(context.Background(),
		`select r.verdict, t.status from review r join attempt t on t.id = r.attempt_id where r.attempt_id = $1`,
		f.attemptID).Scan(&verdict, &status); err != nil {
		t.Fatalf("review row: %v", err)
	}
	if verdict != service.VerdictPass || status != service.AttemptReviewed {
		t.Errorf("stored %q on a %q attempt, want pass and reviewed", verdict, status)
	}
}

// The stream is paged: a manifest asked for a page at a time carries only
// that page, and names where the next one starts.
func TestTheManifestPagesTheStream(t *testing.T) {
	f := newFixture(t)
	s := f.browser(t)
	s.login(f.vetterEmail)

	var seqs []int64
	after := int64(0)
	for pages := 0; pages < 10; pages++ {
		url := reviews.ManifestPath(f.attemptID) + "?limit=2"
		if after > 0 {
			url += "&after_seq=" + strconv.FormatInt(after, 10)
		}
		res, body := s.get(url)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("manifest page after %d = %d: %s", after, res.StatusCode, body)
		}
		var page api.ReplayManifest
		if err := json.Unmarshal([]byte(body), &page); err != nil {
			t.Fatalf("manifest %s: %v", body, err)
		}
		if len(page.Events) > 2 {
			t.Fatalf("page carries %d events, want at most 2", len(page.Events))
		}
		if (after == 0) != (len(page.Problems) > 0) {
			t.Errorf("page after %d carries %d problems; only the first carries the editors", after, len(page.Problems))
		}
		for _, ev := range page.Events {
			seqs = append(seqs, ev.Seq)
		}
		if page.NextAfterSeq == 0 {
			break
		}
		after = page.NextAfterSeq
	}
	if len(seqs) != 3 || seqs[0] != 1 || seqs[2] != 3 {
		t.Errorf("paged seqs = %v, want the three recorded events in order", seqs)
	}
}

// A recruiter oversees the attempt but does not judge it: they read the
// screen with no verdict form, and a posted verdict is refused.
func TestARecruiterReadsTheReviewWithoutTheForm(t *testing.T) {
	f := newFixture(t)
	s := f.browser(t)
	s.login(f.recruiterEmail)

	path := reviews.ReviewPath(f.attemptID)
	res, body := s.get(path)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("review page for a recruiter = %d: %s", res.StatusCode, body)
	}
	for _, unwanted := range []string{"File verdict", "Update verdict", `name="verdict"`} {
		if strings.Contains(body, unwanted) {
			t.Errorf("a recruiter is offered %q", unwanted)
		}
	}
	if !strings.Contains(body, "Only the vetter this attempt is assigned to files its verdict.") {
		t.Error("the recruiter is not told why there is no form")
	}
	if res, _ := s.post(path, url.Values{"verdict": {service.VerdictPass}}); res.StatusCode != http.StatusForbidden {
		t.Errorf("a recruiter filing a verdict = %d, want 403", res.StatusCode)
	}
	var filed int
	if err := f.sys.QueryRow(context.Background(), `select count(*) from review where attempt_id = $1`, f.attemptID).Scan(&filed); err != nil {
		t.Fatal(err)
	}
	if filed != 0 {
		t.Errorf("%d reviews filed by a recruiter, want none", filed)
	}
}

func TestAnUnassignedVetterCannotOpenTheReview(t *testing.T) {
	f := newFixture(t)
	s := f.browser(t)
	s.login(f.otherVetterEmail)

	if res, _ := s.get(reviews.ReviewPath(f.attemptID)); res.StatusCode != http.StatusForbidden {
		t.Errorf("review page for an unassigned vetter = %d, want 403", res.StatusCode)
	}
	if res, _ := s.get(reviews.ManifestPath(f.attemptID)); res.StatusCode != http.StatusForbidden {
		t.Errorf("manifest for an unassigned vetter = %d, want 403", res.StatusCode)
	}
}

func TestTheManifestNeedsASession(t *testing.T) {
	f := newFixture(t)
	s := f.browser(t)
	if res, _ := s.get(reviews.ManifestPath(f.attemptID)); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("manifest without a session = %d, want 401", res.StatusCode)
	}
}
