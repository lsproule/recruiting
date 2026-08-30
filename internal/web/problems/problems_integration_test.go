//go:build integration

package problems_test

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
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

	"recruiting/internal/runner/server"
	"recruiting/internal/service"
	"recruiting/internal/store"
	"recruiting/internal/web/auth"
	"recruiting/internal/web/layout"
	"recruiting/internal/web/middleware"
	"recruiting/internal/web/problems"
)

const testPassword = "hunter2-long-enough"

// stubRunner passes every reference solution but the ones named in fail, so
// the screens can be exercised without a sandbox.
type stubRunner struct{ fail map[string]bool }

func (s stubRunner) Execute(_ context.Context, req server.Request) (server.Response, error) {
	res := make([]server.TestResult, len(req.Tests))
	for i, t := range req.Tests {
		res[i] = server.TestResult{TestID: t.ID, Status: server.TestPass}
		if s.fail[req.Source] {
			res[i].Status = server.TestFail
		}
	}
	return server.Response{ID: req.ID, Status: server.StatusOK, Results: res}, nil
}

type fixture struct {
	srv                         *httptest.Server
	sys                         *pgxpool.Pool
	orgID                       uuid.UUID
	recruiterEmail, vetterEmail string
}

func newFixture(t *testing.T, runner service.Executor) *fixture {
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
	for _, seed := range []struct{ email, role string }{
		{f.recruiterEmail, service.RoleRecruiter}, {f.vetterEmail, service.RoleVetter},
	} {
		id := uuid.New()
		exec(`insert into org_user (id, org_id, email, name) values ($1, $2, $3, $3)`, id, f.orgID, seed.email)
		exec(`insert into org_user_role (org_user_id, org_id, role) values ($1, $2, $3)`, id, f.orgID, seed.role)
		exec(`insert into org_user_credential (org_user_id, org_id, password_hash) values ($1, $2, $3)`, id, f.orgID, hash)
	}

	mux := chi.NewMux()
	if _, err := auth.Mount(mux, auth.Deps{Auth: service.NewAuthService(st), CookieSecret: []byte("test-secret")}); err != nil {
		t.Fatal(err)
	}
	layout.MountStatic(mux)
	problems.Mount(mux, problems.Deps{
		Problems: service.NewProblemService(st, runner), Org: service.NewOrgService(st),
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

// upload posts the import form the way a browser does, with the batch as a
// file part.
func (s *session) upload(path, filename, content string) (*http.Response, string) {
	s.t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	_ = w.WriteField(middleware.CSRFField, s.csrf())
	part, err := w.CreateFormFile("document", filename)
	if err != nil {
		s.t.Fatal(err)
	}
	if _, err := part.Write([]byte(content)); err != nil {
		s.t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		s.t.Fatal(err)
	}
	res, err := s.cli.Post(s.f.srv.URL+path, w.FormDataContentType(), &body)
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

// newProblemForm is the create form as the browser posts it.
func newProblemForm(title, source string) url.Values {
	return url.Values{
		"title": {title}, "kind": {"code"}, "difficulty": {"medium"},
		"tags": {"sorting, warmup"}, "allowed_languages": {"python"},
		"time_limit_ms": {"2000"}, "memory_limit_kb": {"262144"},
		"statement":      {"Print the sum."},
		"ref_language_0": {"python"}, "ref_source_0": {source},
		"ref_language_1": {""}, "ref_source_1": {""},
		"tc_input_0": {"1 2"}, "tc_expected_0": {"3"}, "tc_visibility_0": {"public"}, "tc_weight_0": {"1"},
		"tc_input_1": {"2 2"}, "tc_expected_1": {"4"}, "tc_visibility_1": {"hidden"}, "tc_weight_1": {"2"},
		"tc_input_2": {""}, "tc_expected_2": {""}, "tc_visibility_2": {"public"}, "tc_weight_2": {"1"},
	}
}

func TestProblemScreensCreateBrowseFilterAndEdit(t *testing.T) {
	f := newFixture(t, stubRunner{})
	s := f.browser(t)
	s.login(f.recruiterEmail)

	if res, body := s.get(problems.Prefix + "/new"); res.StatusCode != http.StatusOK || !strings.Contains(body, "Reference solutions") {
		t.Fatalf("new form = %d, body %q", res.StatusCode, body)
	}

	res, body := s.post(problems.Prefix+"/", newProblemForm("Screen Adder", "print(3)"))
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("create = %d, body %q", res.StatusCode, body)
	}
	detail := res.Header.Get("Location")

	if res, body := s.get(detail); res.StatusCode != http.StatusOK ||
		!strings.Contains(body, "Screen Adder") || !strings.Contains(body, "print(3)") {
		t.Fatalf("detail = %d, body %q", res.StatusCode, body)
	}

	for _, q := range []string{"?tag=sorting", "?difficulty=medium", "?kind=code", "?q=adder"} {
		if res, body := s.get(problems.Prefix + q); res.StatusCode != http.StatusOK || !strings.Contains(body, "Screen Adder") {
			t.Fatalf("filter %s = %d, body %q", q, res.StatusCode, body)
		}
	}
	for _, q := range []string{"?tag=graphs", "?difficulty=hard", "?kind=sql"} {
		if res, body := s.get(problems.Prefix + q); res.StatusCode != http.StatusOK || strings.Contains(body, "Screen Adder") {
			t.Fatalf("filter %s = %d, body %q", q, res.StatusCode, body)
		}
	}

	if res, body := s.get(detail + "/edit"); res.StatusCode != http.StatusOK || !strings.Contains(body, "Screen Adder") {
		t.Fatalf("edit form = %d, body %q", res.StatusCode, body)
	}
	edited := newProblemForm("Screen Adder", "print(3)")
	edited.Set("difficulty", "hard")
	if res, body := s.post(detail, edited); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("update = %d, body %q", res.StatusCode, body)
	}
	if res, body := s.get(detail); res.StatusCode != http.StatusOK || !strings.Contains(body, "hard") {
		t.Fatalf("detail after edit = %d, body %q", res.StatusCode, body)
	}

	if res, body := s.post(detail+"/delete", url.Values{}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete = %d, body %q", res.StatusCode, body)
	}
	if res, _ := s.get(detail); res.StatusCode != http.StatusNotFound {
		t.Fatalf("detail after delete = %d, want 404", res.StatusCode)
	}
}

func TestProblemFormShowsWhyAProblemWasRefused(t *testing.T) {
	f := newFixture(t, stubRunner{fail: map[string]bool{"print(9)": true}})
	s := f.browser(t)
	s.login(f.recruiterEmail)

	res, body := s.post(problems.Prefix+"/", newProblemForm("Screen Broken", "print(9)"))
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("create with a failing reference = %d, body %q", res.StatusCode, body)
	}
	if !strings.Contains(body, "does not solve the problem") {
		t.Errorf("the form does not say why: %q", body)
	}

	bad := newProblemForm("Screen Bad Language", "print(3)")
	bad.Set("allowed_languages", "cobol")
	if res, body := s.post(problems.Prefix+"/", bad); res.StatusCode != http.StatusUnprocessableEntity ||
		!strings.Contains(body, "cobol") {
		t.Fatalf("unknown language = %d, body %q", res.StatusCode, body)
	}
}

const importBatch = `[
  {"kind":"code","title":"Batch A","statement":"s","difficulty":"easy","tags":["batch"],
   "allowed_languages":["python"],
   "reference_solutions":[{"language":"python","source":"print(1)"}],
   "test_cases":[{"input":"","expected":"1","visibility":"public","weight":1}]},
  {"kind":"code","title":"Batch B","statement":"s","difficulty":"medium","tags":["batch"],
   "allowed_languages":["python"],
   "reference_solutions":[{"language":"python","source":"print(2)"}],
   "test_cases":[{"input":"","expected":"2","visibility":"public","weight":1}]}
]`

func TestImportScreenAcceptsABatch(t *testing.T) {
	f := newFixture(t, stubRunner{})
	s := f.browser(t)
	s.login(f.recruiterEmail)

	if res, body := s.get(problems.ImportPath); res.StatusCode != http.StatusOK || !strings.Contains(body, "JSON file") {
		t.Fatalf("import form = %d, body %q", res.StatusCode, body)
	}
	res, body := s.upload(problems.ImportPath, "batch.json", importBatch)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "Imported 2 problems") {
		t.Fatalf("import = %d, body %q", res.StatusCode, body)
	}
	if res, body := s.get(problems.Prefix + "?tag=batch"); res.StatusCode != http.StatusOK ||
		!strings.Contains(body, "Batch A") || !strings.Contains(body, "Batch B") {
		t.Fatalf("list after import = %d, body %q", res.StatusCode, body)
	}
}

func TestImportScreenReportsPerProblemErrorsAndStoresNothing(t *testing.T) {
	f := newFixture(t, stubRunner{fail: map[string]bool{"print(2)": true}})
	s := f.browser(t)
	s.login(f.recruiterEmail)

	res, body := s.upload(problems.ImportPath, "batch.json", importBatch)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("import with a failing reference = %d, body %q", res.StatusCode, body)
	}
	if !strings.Contains(body, "Batch B") || !strings.Contains(body, "Nothing was imported") {
		t.Errorf("the report does not name the failing problem: %q", body)
	}
	if strings.Contains(body, "Imported") {
		t.Errorf("a rejected batch was reported as imported: %q", body)
	}
	if res, body := s.get(problems.Prefix + "?tag=batch"); res.StatusCode != http.StatusOK ||
		strings.Contains(body, "Batch A") {
		t.Fatalf("a rejected batch stored problems: %d, body %q", res.StatusCode, body)
	}
}

func TestProblemScreensRefuseAVetter(t *testing.T) {
	f := newFixture(t, stubRunner{})
	s := f.browser(t)
	s.login(f.vetterEmail)
	if res, _ := s.get(problems.Prefix); res.StatusCode != http.StatusForbidden {
		t.Fatalf("vetter on the bank = %d, want 403", res.StatusCode)
	}
	if res, _ := s.get(problems.ImportPath); res.StatusCode != http.StatusForbidden {
		t.Fatalf("vetter on the import screen = %d, want 403", res.StatusCode)
	}
}

func TestProblemScreensRedirectAnAnonymousVisitor(t *testing.T) {
	f := newFixture(t, stubRunner{})
	s := f.browser(t)
	res, _ := s.get(problems.Prefix)
	if res.StatusCode != http.StatusSeeOther || !strings.Contains(res.Header.Get("Location"), "/app/login") {
		t.Fatalf("anonymous visitor = %d to %q", res.StatusCode, res.Header.Get("Location"))
	}
}

// A document that never parses is the author's mistake, not a server fault:
// the import screen says what is wrong with it.
func TestImportScreenReportsADocumentThatDoesNotParse(t *testing.T) {
	f := newFixture(t, stubRunner{})
	s := f.browser(t)
	s.login(f.recruiterEmail)

	for _, tc := range []struct {
		name, doc, want string
	}{
		{"unknown field", `[{"knid":"code"}]`, "knid"},
		{"malformed", `[{"kind":`, "not a valid problem batch"},
		{"trailing content", importBatch + " []", "more than one JSON value"},
		{"empty array", `[]`, "contains no problems"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, body := s.upload(problems.ImportPath, "batch.json", tc.doc)
			if res.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422; body %q", res.StatusCode, body)
			}
			if !strings.Contains(body, tc.want) {
				t.Errorf("the report does not say %q: %q", tc.want, body)
			}
			if !strings.Contains(body, "the document") {
				t.Errorf("the report does not blame the document: %q", body)
			}
			if strings.Contains(body, "Something went wrong") {
				t.Errorf("a bad document was reported as a server fault: %q", body)
			}
		})
	}
}

func TestImportScreenRefusesAnEmptySubmission(t *testing.T) {
	f := newFixture(t, stubRunner{})
	s := f.browser(t)
	s.login(f.recruiterEmail)
	res, body := s.post(problems.ImportPath, url.Values{})
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "choose a JSON file or paste the batch") {
		t.Fatalf("empty import = %d, body %q", res.StatusCode, body)
	}
}
