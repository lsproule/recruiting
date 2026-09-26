//go:build integration

package client_test

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

	"errors"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"recruiting/internal/queue"
	"recruiting/internal/service"
	"recruiting/internal/store"
	"recruiting/internal/web/auth"
	"recruiting/internal/web/client"
	"recruiting/internal/web/layout"
	"recruiting/internal/web/middleware"
)

const testPassword = "hunter2-long-enough"

// fakeBlobs signs URLs without any object storage behind them.
type fakeBlobs struct{}

func (fakeBlobs) Put(context.Context, string, io.Reader, int64, string) error { return nil }
func (fakeBlobs) Delete(context.Context, string) error                        { return nil }
func (fakeBlobs) SignedGetURL(_ context.Context, key, filename string, _ time.Duration) (string, error) {
	return "https://blobs.test/" + key + "?name=" + filename, nil
}

type fixture struct {
	srv                    *httptest.Server
	sys                    *pgxpool.Pool
	st                     *store.Store
	release                *service.ReleaseService
	shortlists             *service.ShortlistService
	tokens                 *service.APITokenService
	orgID, jobID           uuid.UUID
	companyID              uuid.UUID
	draftJobID             uuid.UUID
	appID, hiddenAppID     uuid.UUID
	review, final, reject  uuid.UUID
	recruiterID            uuid.UUID
	recruiterEmail         string
	clientEmail, rivalMail string
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
		sys: sys, st: st,
		orgID: uuid.New(), jobID: uuid.New(), draftJobID: uuid.New(), appID: uuid.New(), hiddenAppID: uuid.New(),
		review: uuid.New(), final: uuid.New(), reject: uuid.New(), recruiterID: uuid.New(),
	}
	f.recruiterEmail = "rec-" + f.orgID.String() + "@example.com"
	f.clientEmail = "client-" + f.orgID.String() + "@example.com"
	f.rivalMail = "rival-" + f.orgID.String() + "@example.com"
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := sys.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`insert into org (id, name, slug) values ($1, $2, $2)`, f.orgID, f.orgID.String())
	t.Cleanup(func() { _, _ = sys.Exec(context.Background(), `delete from org where id = $1`, f.orgID) })
	hash, _ := service.HashPassword(testPassword)
	exec(`insert into org_user (id, org_id, email, name) values ($1, $2, $3, 'Rita Recruiter')`, f.recruiterID, f.orgID, f.recruiterEmail)
	exec(`insert into org_user_role (org_user_id, org_id, role) values ($1, $2, 'recruiter')`, f.recruiterID, f.orgID)
	vetterID := uuid.New()
	exec(`insert into org_user (id, org_id, email, name) values ($1, $2, $3, 'Vic Vetter')`, vetterID, f.orgID, "vet-"+f.orgID.String()+"@example.com")
	exec(`insert into org_user_role (org_user_id, org_id, role) values ($1, $2, 'vetter')`, vetterID, f.orgID)

	companyID, rivalID := uuid.New(), uuid.New()
	f.companyID = companyID
	exec(`insert into client_company (id, org_id, name) values ($1, $2, 'Globex')`, companyID, f.orgID)
	exec(`insert into client_company (id, org_id, name) values ($1, $2, 'Initech')`, rivalID, f.orgID)
	for _, cu := range []struct {
		company uuid.UUID
		email   string
	}{{companyID, f.clientEmail}, {rivalID, f.rivalMail}} {
		id := uuid.New()
		exec(`insert into client_user (id, org_id, client_company_id, email, name) values ($1, $2, $3, $4, 'Carl Client')`, id, f.orgID, cu.company, cu.email)
		exec(`insert into client_user_credential (client_user_id, org_id, password_hash) values ($1, $2, $3)`, id, f.orgID, hash)
	}
	exec(`insert into job (id, org_id, client_company_id, title, slug, status, blind_mode) values ($1, $2, $3, 'Senior Go Engineer', $4, 'open', true)`, f.jobID, f.orgID, companyID, "go-"+f.orgID.String())
	exec(`insert into job (id, org_id, client_company_id, title, slug, status) values ($1, $2, $3, 'Unannounced Role', $4, 'draft')`, f.draftJobID, f.orgID, companyID, "draft-"+f.orgID.String())
	exec(`insert into stage (id, org_id, job_id, position, name, kind) values ($1, $2, $3, 1, 'Screen', 'generic')`, uuid.New(), f.orgID, f.jobID)
	exec(`insert into stage (id, org_id, job_id, position, name, kind, unblind) values ($1, $2, $3, 2, 'Client review', 'client_review', false)`, f.review, f.orgID, f.jobID)
	exec(`insert into stage (id, org_id, job_id, position, name, kind, unblind) values ($1, $2, $3, 3, 'Final round', 'client_review', true)`, f.final, f.orgID, f.jobID)
	exec(`insert into stage (id, org_id, job_id, position, name, kind, terminal_status) values ($1, $2, $3, 4, 'Hired', 'terminal', 'hired')`, uuid.New(), f.orgID, f.jobID)
	exec(`insert into stage (id, org_id, job_id, position, name, kind, terminal_status) values ($1, $2, $3, 5, 'Rejected', 'terminal', 'rejected')`, f.reject, f.orgID, f.jobID)

	candID, hiddenCandID := uuid.New(), uuid.New()
	exec(`insert into candidate (id, org_id, email, name, phone, links) values ($1, $2, $3, 'Ada Lovelace', '+44 555 0100', '["https://github.com/ada-lovelace"]')`, candID, f.orgID, "ada-"+f.orgID.String()+"@example.com")
	exec(`insert into candidate (id, org_id, email, name) values ($1, $2, $3, 'Hidden Harry')`, hiddenCandID, f.orgID, "harry-"+f.orgID.String()+"@example.com")
	exec(`insert into resume (org_id, candidate_id, blob_key, filename, content_type, size_bytes) values ($1, $2, 'resumes/ada.pdf', 'ada.pdf', 'application/pdf', 10)`, f.orgID, candID)
	exec(`insert into application (id, org_id, job_id, candidate_id, client_company_id, stage_id, released_at, recruiter_summary) values ($1, $2, $3, $4, $5, $6, now(), 'Strong systems background.')`,
		f.appID, f.orgID, f.jobID, candID, companyID, f.review)
	exec(`insert into application (id, org_id, job_id, candidate_id, client_company_id, stage_id) values ($1, $2, $3, $4, $5, $6)`,
		f.hiddenAppID, f.orgID, f.jobID, hiddenCandID, companyID, f.review)
	exec(`insert into scorecard (org_id, application_id, stage_id, vetter_id, scores, overall, notes) values ($1, $2, $3, $4, '[{"name":"Depth","score":4,"notes":"CRITERION-SECRET"}]', 'yes', 'INTERNAL-SECRET')`,
		f.orgID, f.appID, f.review, vetterID)

	q, err := queue.New(st.Pool(), queue.Config{})
	if err != nil {
		t.Fatal(err)
	}
	apps := service.NewApplicationService(st, q, "https://example.test")
	f.release = service.NewReleaseService(st, q, "https://example.test")
	resumes := service.NewResumeService(st, fakeBlobs{})
	portal := service.NewClientPortalService(st, apps, resumes, q, "https://example.test")
	f.shortlists = service.NewShortlistService(st, f.release,
		service.NewReviewService(st, nil, nil), service.NewPoolService(st), q, "https://example.test")

	mux := chi.NewMux()
	if _, err := auth.Mount(mux, auth.Deps{Auth: service.NewAuthService(st), CookieSecret: []byte("test-secret")}); err != nil {
		t.Fatal(err)
	}
	layout.MountStatic(mux)
	f.tokens = service.NewAPITokenService(st)
	client.Mount(mux, client.Deps{
		Portal: portal, Shortlists: f.shortlists, Tokens: f.tokens, BaseURL: "https://example.test",
		Talent: service.NewTalentService(st, resumes, service.NewMagicLinkService(st), q, "https://example.test"),
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

func (f *fixture) browser(t *testing.T, email string) *session {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	s := &session{t: t, f: f, cli: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	s.get("/client/login")
	form := url.Values{"email": {email}, "password": {testPassword}, middleware.CSRFField: {s.csrf()}}
	res, err := s.cli.PostForm(f.srv.URL+"/client/login", form)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
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

func (f *fixture) recruiter() service.Principal {
	return service.Principal{Kind: service.PrincipalOrgUser, OrgID: f.orgID, UserID: f.recruiterID, Roles: []string{service.RoleRecruiter}}
}

// emails counts queued email.send jobs of one template addressed to one recipient.
func (f *fixture) emails(t *testing.T, template, to string) int {
	t.Helper()
	var n int
	err := f.sys.QueryRow(context.Background(),
		`select count(*) from river_job where kind = $1 and args->'payload'->>'template' = $2 and args->'payload'->>'to' = $3`,
		queue.KindEmailSend, template, to).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// secrets is everything a blind or client view must never carry.
var secrets = []string{"Ada Lovelace", "ada-", "+44 555 0100", "github.com/ada-lovelace", "INTERNAL-SECRET", "CRITERION-SECRET"}

func assertNone(t *testing.T, body string, words ...string) {
	t.Helper()
	for _, w := range words {
		if strings.Contains(body, w) {
			t.Errorf("client surface leaked %q", w)
		}
	}
}

func TestClientSeesOnlyReleasedApplicationsOfTheirOwnCompany(t *testing.T) {
	f := newFixture(t)
	s := f.browser(t, f.clientEmail)

	res, body := s.get(client.JobsPath)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "Senior Go Engineer") {
		t.Fatalf("jobs = %d, body %q", res.StatusCode, body)
	}
	res, body = s.get(client.JobPath(f.jobID))
	if res.StatusCode != http.StatusOK || strings.Contains(body, "Hidden Harry") {
		t.Fatalf("job = %d, body %q", res.StatusCode, body)
	}
	if !strings.Contains(body, client.ApplicationPath(f.appID)) {
		t.Fatalf("released application missing from job page: %q", body)
	}
	if res, _ = s.get(client.ApplicationPath(f.hiddenAppID)); res.StatusCode != http.StatusNotFound {
		t.Fatalf("unreleased application = %d, want 404", res.StatusCode)
	}

	rival := f.browser(t, f.rivalMail)
	if res, body = rival.get(client.JobsPath); res.StatusCode != http.StatusOK || strings.Contains(body, "Senior Go Engineer") {
		t.Fatalf("rival jobs = %d, body %q", res.StatusCode, body)
	}
	if res, _ = rival.get(client.JobPath(f.jobID)); res.StatusCode != http.StatusNotFound {
		t.Fatalf("rival job = %d, want 404", res.StatusCode)
	}
	if res, _ = rival.get(client.ApplicationPath(f.appID)); res.StatusCode != http.StatusNotFound {
		t.Fatalf("rival application = %d, want 404", res.StatusCode)
	}
}

func TestBlindModeRedactsUntilAnUnblindStageAndDeniesTheResume(t *testing.T) {
	f := newFixture(t)
	s := f.browser(t, f.clientEmail)

	res, body := s.get(client.JobPath(f.jobID))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("job = %d", res.StatusCode)
	}
	assertNone(t, body, secrets...)

	res, body = s.get(client.ApplicationPath(f.appID))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("application = %d, body %q", res.StatusCode, body)
	}
	assertNone(t, body, secrets...)
	for _, want := range []string{"Strong systems background.", "Depth", "Yes"} {
		if !strings.Contains(body, want) {
			t.Errorf("application page lacks %q: %q", want, body)
		}
	}
	if res, _ = s.get(client.ResumePath(f.appID)); res.StatusCode != http.StatusForbidden {
		t.Fatalf("blind resume = %d, want 403", res.StatusCode)
	}

	// Advancing into the unblind stage reveals the candidate.
	res, body = s.post(client.AdvancePath(f.appID), url.Values{"to_stage_id": {f.final.String()}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("advance = %d, body %q", res.StatusCode, body)
	}
	res, body = s.get(client.ApplicationPath(f.appID))
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "Ada Lovelace") || !strings.Contains(body, "github.com/ada-lovelace") {
		t.Fatalf("unblinded application = %d, body %q", res.StatusCode, body)
	}
	assertNone(t, body, "INTERNAL-SECRET", "CRITERION-SECRET")
	res, _ = s.get(client.ResumePath(f.appID))
	if res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(res.Header.Get("Location"), "https://blobs.test/resumes/ada.pdf") {
		t.Fatalf("resume = %d %q", res.StatusCode, res.Header.Get("Location"))
	}
}

func TestClientRejectNeedsAReason(t *testing.T) {
	f := newFixture(t)
	s := f.browser(t, f.clientEmail)
	res, body := s.post(client.RejectPath(f.appID), url.Values{"reason": {"  "}})
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "reason is required") {
		t.Fatalf("reject without reason = %d, body %q", res.StatusCode, body)
	}
	if res, body = s.post(client.RejectPath(f.appID), url.Values{"reason": {"Not enough Go"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("reject = %d, body %q", res.StatusCode, body)
	}
	if _, body = s.get(client.ApplicationPath(f.appID)); !strings.Contains(body, "Rejected") {
		t.Fatalf("rejected application page %q", body)
	}
}

func TestRequestInfoEmailsTheRecruiters(t *testing.T) {
	f := newFixture(t)
	s := f.browser(t, f.clientEmail)
	res, body := s.post(client.RequestInfoPath(f.appID), url.Values{"message": {""}})
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("empty request = %d, body %q", res.StatusCode, body)
	}
	if res, body = s.post(client.RequestInfoPath(f.appID), url.Values{"message": {"Can they relocate?"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("request info = %d, body %q", res.StatusCode, body)
	}
	if n := f.emails(t, "client_request_info", f.recruiterEmail); n != 1 {
		t.Fatalf("request info emails to the recruiter = %d, want 1", n)
	}
}

func TestUnreleaseRevokesAccess(t *testing.T) {
	f := newFixture(t)
	s := f.browser(t, f.clientEmail)
	if res, _ := s.get(client.ApplicationPath(f.appID)); res.StatusCode != http.StatusOK {
		t.Fatalf("application before = %d", res.StatusCode)
	}
	if _, err := f.release.Unrelease(context.Background(), f.recruiter(), f.appID); err != nil {
		t.Fatal(err)
	}
	if res, _ := s.get(client.ApplicationPath(f.appID)); res.StatusCode != http.StatusNotFound {
		t.Fatalf("application after unrelease = %d, want 404", res.StatusCode)
	}
	if res, _ := s.get(client.ResumePath(f.appID)); res.StatusCode != http.StatusNotFound {
		t.Fatalf("resume after unrelease = %d, want 404", res.StatusCode)
	}
}

func TestReleaseNotifiesTheClientCompany(t *testing.T) {
	f := newFixture(t)
	if _, err := f.release.Release(context.Background(), f.recruiter(), f.hiddenAppID); err != nil {
		t.Fatal(err)
	}
	if n := f.emails(t, "client_release_notice", f.clientEmail); n != 1 {
		t.Fatalf("release notices to the client = %d, want 1", n)
	}
	if n := f.emails(t, "client_release_notice", f.rivalMail); n != 0 {
		t.Fatalf("release notices to another company = %d, want 0", n)
	}
	// Releasing again is idempotent and sends nothing more.
	if _, err := f.release.Release(context.Background(), f.recruiter(), f.hiddenAppID); err != nil {
		t.Fatal(err)
	}
	if n := f.emails(t, "client_release_notice", f.clientEmail); n != 1 {
		t.Fatalf("release notices after a repeat = %d, want 1", n)
	}
	s := f.browser(t, f.clientEmail)
	if res, _ := s.get(client.ApplicationPath(f.hiddenAppID)); res.StatusCode != http.StatusOK {
		t.Fatalf("released application = %d", res.StatusCode)
	}
}

func TestDraftJobsAreNotShownToTheClient(t *testing.T) {
	f := newFixture(t)
	s := f.browser(t, f.clientEmail)
	if _, body := s.get(client.JobsPath); strings.Contains(body, "Unannounced Role") {
		t.Fatalf("draft job listed: %q", body)
	}
	if res, _ := s.get(client.JobPath(f.draftJobID)); res.StatusCode != http.StatusNotFound {
		t.Fatalf("draft job = %d, want 404", res.StatusCode)
	}
}

func TestClientCannotAdvanceBackwards(t *testing.T) {
	f := newFixture(t)
	s := f.browser(t, f.clientEmail)
	if res, body := s.post(client.AdvancePath(f.appID), url.Values{"to_stage_id": {f.final.String()}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("advance = %d, body %q", res.StatusCode, body)
	}
	res, body := s.post(client.AdvancePath(f.appID), url.Values{"to_stage_id": {f.review.String()}})
	if res.StatusCode != http.StatusForbidden || !strings.Contains(body, "not permitted") {
		t.Fatalf("advance backwards = %d, body %q", res.StatusCode, body)
	}
}

func TestOverlongInputIsRefused(t *testing.T) {
	f := newFixture(t)
	s := f.browser(t, f.clientEmail)
	if res, _ := s.post(client.RejectPath(f.appID), url.Values{"reason": {strings.Repeat("é", 501)}}); res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("long reason = %d, want 422", res.StatusCode)
	}
	if res, _ := s.post(client.RequestInfoPath(f.appID), url.Values{"message": {strings.Repeat("é", 4001)}}); res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("long message = %d, want 422", res.StatusCode)
	}
	if n := f.emails(t, "client_request_info", f.recruiterEmail); n != 0 {
		t.Fatalf("emails after refusals = %d", n)
	}
}

func TestRequestInfoNeedsAnActiveApplication(t *testing.T) {
	f := newFixture(t)
	s := f.browser(t, f.clientEmail)
	if res, _ := s.post(client.RejectPath(f.appID), url.Values{"reason": {"No"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("reject = %d", res.StatusCode)
	}
	res, body := s.post(client.RequestInfoPath(f.appID), url.Values{"message": {"Still there?"}})
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "already closed") {
		t.Fatalf("request info on closed = %d, body %q", res.StatusCode, body)
	}
}

func (f *fixture) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := f.sys.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestConcurrentReleasesRecordAndNotifyOnce(t *testing.T) {
	f := newFixture(t)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.release.Release(context.Background(), f.recruiter(), f.hiddenAppID); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := f.count(t, `select count(*) from application_event where application_id = $1 and kind = 'released'`, f.hiddenAppID); n != 1 {
		t.Fatalf("released events = %d, want 1", n)
	}
	if n := f.emails(t, "client_release_notice", f.clientEmail); n != 1 {
		t.Fatalf("release notices = %d, want 1", n)
	}
}

type brokenQueue struct{}

func (brokenQueue) Enqueue(context.Context, pgx.Tx, string, any) (int64, error) {
	return 0, errors.New("queue down")
}

func (brokenQueue) EnqueueAt(context.Context, pgx.Tx, string, any, time.Time) (int64, error) {
	return 0, errors.New("queue down")
}

func (brokenQueue) CancelTx(context.Context, pgx.Tx, int64) error { return nil }

func TestReleaseRollsBackWhenTheNoticeCannotBeQueued(t *testing.T) {
	f := newFixture(t)
	rel := service.NewReleaseService(f.st, brokenQueue{}, "https://example.test")
	if _, err := rel.Release(context.Background(), f.recruiter(), f.hiddenAppID); err == nil {
		t.Fatal("release succeeded without a notice")
	}
	if n := f.count(t, `select count(*) from application where id = $1 and released_at is not null`, f.hiddenAppID); n != 0 {
		t.Fatal("released_at was set although the notice failed")
	}
	if n := f.count(t, `select count(*) from application_event where application_id = $1 and kind = 'released'`, f.hiddenAppID); n != 0 {
		t.Fatal("released event written although the notice failed")
	}
}

func TestClientPortalTalentAndDeveloperPages(t *testing.T) {
	f := newFixture(t)
	s := f.browser(t, f.clientEmail)

	// Talent requests: the form files one, the page lists it, and the
	// request page reads matches (none yet) and closes it.
	res, body := s.get(client.TalentPath)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "Who are you looking for?") || !strings.Contains(body, "Senior Go Engineer") {
		t.Fatalf("talent page: %d %s", res.StatusCode, body[:200])
	}
	res, body = s.post(client.TalentPath, url.Values{"title": {"Staff engineer"}, "skills": {""}})
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "at least one skill") {
		t.Fatalf("no skills: %d", res.StatusCode)
	}
	res, _ = s.post(client.TalentPath, url.Values{"title": {"Staff engineer"}, "skills": {"go, postgres"}, "seniority": {"staff"}, "job_id": {f.jobID.String()}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("create: %d", res.StatusCode)
	}
	loc := res.Header.Get("Location")
	res, body = s.get(loc)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "Staff engineer") || !strings.Contains(body, "Nobody in the network fits this yet") || !strings.Contains(body, "joins Senior Go Engineer") {
		t.Fatalf("request page: %d %s", res.StatusCode, body[:300])
	}
	assertNone(t, body, secrets...)
	if res, _ = s.post(loc+"/close", url.Values{}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("close: %d", res.StatusCode)
	}
	if _, body = s.get(loc + "?did=close"); !strings.Contains(body, "The request is closed") || strings.Contains(body, "Request introduction") {
		t.Fatalf("closed request still offers matches")
	}
	// Another company sees none of it.
	rival := f.browser(t, f.rivalMail)
	if res, _ := rival.get(loc); res.StatusCode != http.StatusNotFound {
		t.Fatalf("rival on the request: %d, want 404", res.StatusCode)
	}

	// Developer: a token is issued, shown once, listed by prefix, and revoked.
	res, body = s.get(client.DeveloperPath)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "/api/v1/docs") || !strings.Contains(body, "No tokens yet") {
		t.Fatalf("developer page: %d %s", res.StatusCode, body[:200])
	}
	res, body = s.post(client.DeveloperPath+"/tokens", url.Values{"name": {"ATS sync"}, "expires_on": {"2030-01-01"}})
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "Copy this token now") {
		t.Fatalf("issue: %d %s", res.StatusCode, body[:300])
	}
	secret := regexp.MustCompile(`<code class="secret">([^<]+)</code>`).FindStringSubmatch(body)
	if secret == nil {
		t.Fatal("no secret on the page")
	}
	p, err := f.tokens.ResolveToken(context.Background(), secret[1])
	if err != nil || p.Kind != service.PrincipalClientUser || p.ClientCompanyID != f.companyID {
		t.Fatalf("resolved = %+v, %v", p, err)
	}
	_, body = s.get(client.DeveloperPath)
	if !strings.Contains(body, "ATS sync") || strings.Contains(body, secret[1]) || !strings.Contains(body, "1 Jan 2030") {
		t.Fatalf("developer page after issue: %s", body[:300])
	}
	id := regexp.MustCompile(`/client/developer/tokens/([0-9a-f-]{36})/revoke`).FindStringSubmatch(body)
	if id == nil {
		t.Fatal("no revoke form")
	}
	if res, _ = s.post(client.DeveloperPath+"/tokens/"+id[1]+"/revoke", url.Values{}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("revoke: %d", res.StatusCode)
	}
	if _, err := f.tokens.ResolveToken(context.Background(), secret[1]); err == nil {
		t.Fatal("a revoked token still resolves")
	}
}
