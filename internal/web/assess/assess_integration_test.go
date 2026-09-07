//go:build integration

package assess_test

import (
	"context"
	"encoding/json"
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

	"recruiting/internal/api"
	"recruiting/internal/domain"
	"recruiting/internal/runner/server"
	"recruiting/internal/service"
	"recruiting/internal/store"
	"recruiting/internal/web/assess"
	"recruiting/internal/web/auth"
	"recruiting/internal/web/middleware"
)

type passRunner struct{}

func (passRunner) Execute(_ context.Context, req server.Request) (server.Response, error) {
	res := make([]server.TestResult, len(req.Tests))
	for i, t := range req.Tests {
		res[i] = server.TestResult{TestID: t.ID, Status: server.TestPass}
	}
	return server.Response{ID: req.ID, Status: server.StatusOK, Results: res}, nil
}

// TestSealedCookieReachesTheAttemptAPIUnderAssess walks the candidate path a
// browser would: the emailed link sets the sealed cookie (scoped to
// /assess/), the session page renders, and the island's API calls under
// /assess/api carry that cookie through to the attempt operations.
func TestSealedCookieReachesTheAttemptAPIUnderAssess(t *testing.T) {
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

	orgID, jobID, stageID, appID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := sys.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`insert into org (id, name, slug) values ($1, $2, $2)`, orgID, orgID.String())
	t.Cleanup(func() { _, _ = sys.Exec(context.Background(), `delete from org where id = $1`, orgID) })
	companyID, candID := uuid.New(), uuid.New()
	exec(`insert into client_company (id, org_id, name) values ($1, $2, 'Globex')`, companyID, orgID)
	exec(`insert into job (id, org_id, client_company_id, title, slug, status) values ($1, $2, $3, 'Go Engineer', $4, 'open')`, jobID, orgID, companyID, "go-"+orgID.String())
	exec(`insert into stage (id, org_id, job_id, position, name, kind) values ($1, $2, $3, 1, 'Assessment', $4)`, stageID, orgID, jobID, string(domain.StageAssessment))
	exec(`insert into candidate (id, org_id, email, name) values ($1, $2, $3, 'Ada')`, candID, orgID, "ada-"+orgID.String()+"@example.com")
	exec(`insert into application (id, org_id, job_id, candidate_id, client_company_id, stage_id) values ($1, $2, $3, $4, $5, $6)`, appID, orgID, jobID, candID, companyID, stageID)

	rec := service.Principal{Kind: service.PrincipalOrgUser, OrgID: orgID, Roles: []string{service.RoleRecruiter}}
	// The problem has to clear the quality review before an assessment will
	// take it: six cases, one public and three hidden, a tag, a statement of
	// some length, and two languages with a reference solution.
	parsed, err := domain.ParseProblemImport([]byte(`[{"kind":"code","title":"Adder","difficulty":"easy",
		"statement":"Read two integers from one line and print their sum. The line always holds exactly two integers separated by a single space. Both fit in a 64-bit signed integer, and so does the answer. Print the sum on its own line.",
		"tags":["math"],"allowed_languages":["python","javascript"],
		"reference_solutions":[{"language":"python","source":"print(3)"},{"language":"javascript","source":"console.log(3)"}],
		"test_cases":[{"input":"1 2","expected":"3","visibility":"public"},
			{"input":"2 2","expected":"4","visibility":"hidden"},
			{"input":"3 3","expected":"6","visibility":"hidden"},
			{"input":"4 4","expected":"8","visibility":"hidden"},
			{"input":"5 5","expected":"10","visibility":"hidden"},
			{"input":"6 6","expected":"12","visibility":"hidden"}]}]`))
	if err != nil {
		t.Fatal(err)
	}
	problem, err := service.NewProblemService(st, passRunner{}).Create(ctx, rec, parsed[0])
	if err != nil {
		t.Fatal(err)
	}
	assessments := service.NewAssessmentService(st)
	a, err := assessments.Create(ctx, rec, service.AssessmentInput{Name: "Screen", DurationMinutes: 30, InviteWindowDays: 3, ProblemIDs: []uuid.UUID{problem.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if err := assessments.AttachToStage(ctx, rec, jobID, stageID, a.ID); err != nil {
		t.Fatal(err)
	}
	attempts := service.NewAttemptService(st, nil, "http://example.test")
	att, err := attempts.Invite(ctx, service.AssessmentInvitePayload{ApplicationID: appID, StageID: stageID, OrgID: orgID})
	if err != nil {
		t.Fatal(err)
	}
	links := service.NewMagicLinkService(st)
	token, _, err := links.Issue(ctx, service.Principal{Kind: service.PrincipalSystem, OrgID: orgID}, service.LinkAssessment, att.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	// The composition serve.go is expected to build.
	r := api.NewRouter()
	api.MountAttempts(r.API, api.AttemptsDeps{Attempts: attempts})
	web := chi.NewMux()
	au, err := auth.Mount(web, auth.Deps{Auth: service.NewAuthService(st), Links: links, CookieSecret: []byte("test-secret")})
	if err != nil {
		t.Fatal(err)
	}
	assess.Mount(web, assess.Deps{Attempts: attempts, Assessment: au.Assessment(), API: r.Mux})
	r.Mux.Mount("/", web)
	srv := httptest.NewServer(r.Mux)
	t.Cleanup(srv.Close)

	jar, _ := cookiejar.New(nil)
	cli := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(method, path, contentType, body string, csrf bool) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		if csrf {
			for _, c := range jar.Cookies(req.URL) {
				if c.Name == middleware.CSRFCookie {
					req.Header.Set(middleware.CSRFHeader, c.Value)
				}
			}
		}
		res, err := cli.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}

	// Without the cookie the API path is unreachable, whichever mount.
	if code, _ := do(http.MethodGet, "/assess/api/attempts/"+att.ID.String(), "", "", false); code == http.StatusOK {
		t.Fatal("attempt API answered without the sealed cookie")
	}
	if code, _ := do(http.MethodGet, "/assess/"+token, "", "", false); code != http.StatusSeeOther {
		t.Fatalf("link entry = %d, want 303", code)
	}
	if code, body := do(http.MethodGet, "/assess/", "", "", false); code != http.StatusOK || !strings.Contains(body, "Start the assessment") {
		t.Fatalf("session page = %d %q", code, body)
	}
	code, body := do(http.MethodGet, "/assess/api/attempts/"+att.ID.String(), "", "", false)
	if code != http.StatusOK || !strings.Contains(body, `"status":"invited"`) {
		t.Fatalf("attempt API through /assess/api = %d %s", code, body)
	}
	// The cookie is scoped to /assess/, so the bare API mount sees none.
	if code, _ := do(http.MethodGet, "/api/v1/attempts/"+att.ID.String(), "", "", false); code != http.StatusUnauthorized {
		t.Errorf("bare /api/v1 mount = %d, want 401 (cookie must not be sent there)", code)
	}

	var csrf string
	for _, c := range jar.Cookies(mustURL(srv.URL)) {
		if c.Name == middleware.CSRFCookie {
			csrf = c.Value
		}
	}
	if code, _ := do(http.MethodPost, "/assess/start", "application/x-www-form-urlencoded", middleware.CSRFField+"="+csrf, false); code != http.StatusSeeOther {
		t.Fatalf("start = %d, want 303", code)
	}
	batch := `{"attempt_id":"` + att.ID.String() + `","events":[{"seq":1,"t":` + itoa(time.Now().UnixMilli()) + `,"problem_id":"` + problem.ID.String() + `","type":"focus","data":{}}]}`
	if code, body := do(http.MethodPost, "/assess/api/attempts/"+att.ID.String()+"/events", "application/json", batch, false); code != http.StatusForbidden {
		t.Errorf("JSON post without the CSRF header = %d %s, want 403", code, body)
	}
	if code, body := do(http.MethodPost, "/assess/api/attempts/"+att.ID.String()+"/events", "application/json", batch, true); code != http.StatusOK || !strings.Contains(body, `"last_seq":1`) {
		t.Errorf("JSON post with the CSRF header = %d %s", code, body)
	}
	beacon := strings.Replace(batch, `"seq":1`, `"seq":2`, 1)
	form := url.Values{middleware.CSRFField: {csrf}, "batch": {beacon}}
	if code, body := do(http.MethodPost, assess.BeaconPath, "application/x-www-form-urlencoded", form.Encode(), false); code != http.StatusOK || !strings.Contains(body, `"last_seq":2`) {
		t.Errorf("beacon form post = %d %s", code, body)
	}
	if code, body := do(http.MethodPost, "/assess/api/attempts/"+att.ID.String()+"/problems/"+problem.ID.String()+"/run", "application/json", `{"language":"python","source":"print(3)"}`, true); code != http.StatusAccepted {
		t.Errorf("run = %d %s", code, body)
	}
	var n int
	if err := sys.QueryRow(ctx, `select count(*) from attempt_event where attempt_id = $1`, att.ID).Scan(&n); err != nil || n != 2 {
		t.Errorf("%d events stored, %v; want 2", n, err)
	}
	var st2 struct{ Status string }
	_ = json.Unmarshal([]byte(body), &st2)
}

func mustURL(s string) *url.URL {
	u, _ := url.Parse(s)
	return u
}

func itoa(n int64) string { return string(mustJSON(n)) }

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
