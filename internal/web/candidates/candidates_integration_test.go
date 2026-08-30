//go:build integration

package candidates_test

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
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

	"recruiting/internal/service"
	"recruiting/internal/store"
	"recruiting/internal/web/apply"
	"recruiting/internal/web/auth"
	"recruiting/internal/web/candidates"
	"recruiting/internal/web/layout"
	"recruiting/internal/web/middleware"
)

const testPassword = "hunter2-long-enough"

// memBlob keeps uploaded resumes in memory; MinIO has its own test.
type memBlob struct{ objects map[string][]byte }

func (b *memBlob) Put(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	body, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	b.objects[key] = body
	return nil
}

func (b *memBlob) Delete(_ context.Context, key string) error {
	delete(b.objects, key)
	return nil
}

func (b *memBlob) SignedGetURL(_ context.Context, key, filename string, _ time.Duration) (string, error) {
	return "https://blob.example/" + key + "?name=" + url.QueryEscape(filename), nil
}

type fixture struct {
	srv                         *httptest.Server
	candidates                  *service.CandidateService
	orgID, jobID                uuid.UUID
	orgSlug, jobSlug, stageName string
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

	f := &fixture{orgID: uuid.New(), jobID: uuid.New(), stageName: "Applied"}
	f.orgSlug = f.orgID.String()
	f.jobSlug = "go-engineer-" + strings.SplitN(f.orgID.String(), "-", 2)[0]
	f.recruiterEmail = "rec-" + f.orgID.String() + "@example.com"
	f.vetterEmail = "vet-" + f.orgID.String() + "@example.com"
	if _, err := sys.Exec(ctx, `insert into org (id, name, slug) values ($1, $2, $2)`, f.orgID, f.orgID.String()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = sys.Exec(context.Background(), `delete from org where id = $1`, f.orgID) })
	hash, _ := service.HashPassword(testPassword)
	for _, seed := range []struct{ email, role string }{
		{f.recruiterEmail, service.RoleRecruiter}, {f.vetterEmail, service.RoleVetter},
	} {
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
	companyID := uuid.New()
	if _, err := sys.Exec(ctx, `insert into client_company (id, org_id, name) values ($1, $2, 'Globex')`, companyID, f.orgID); err != nil {
		t.Fatal(err)
	}
	_, err = sys.Exec(ctx, `insert into job (id, org_id, client_company_id, title, slug, status)
		values ($1, $2, $3, 'Senior Go Engineer', $4, 'open')`, f.jobID, f.orgID, companyID, f.jobSlug)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sys.Exec(ctx, `insert into stage (org_id, job_id, position, name, kind) values ($1, $2, 1, $3, 'generic')`, f.orgID, f.jobID, f.stageName); err != nil {
		t.Fatal(err)
	}

	resumes := service.NewResumeService(st, &memBlob{objects: map[string][]byte{}})
	f.candidates = service.NewCandidateService(st, resumes)
	mux := chi.NewMux()
	// The body cap goes on before CSRF, which is where an unbounded upload
	// would otherwise be parsed.
	mux.Use(apply.MaxBody(apply.UploadBodyLimit))
	if _, err := auth.Mount(mux, auth.Deps{Auth: service.NewAuthService(st), CookieSecret: []byte("test-secret")}); err != nil {
		t.Fatal(err)
	}
	layout.MountStatic(mux)
	candidates.Mount(mux, candidates.Deps{Candidates: f.candidates, Jobs: service.NewJobService(st), Org: service.NewOrgService(st)})
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

// seedApplicant runs the public apply flow so the list has something in it.
func (f *fixture) seedApplicant(t *testing.T) service.Candidate {
	t.Helper()
	cand, err := f.candidates.Apply(context.Background(), f.orgSlug, f.jobSlug, service.ApplyInput{
		Name: "Ada Lovelace", Email: "ada@example.com",
		Resume: service.ResumeUpload{Filename: "ada.docx", Data: docxBytes(t, "Ada Lovelace", "Elixir and Erlang")},
	})
	if err != nil {
		t.Fatal(err)
	}
	return cand
}

func docxBytes(t *testing.T, paragraphs ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	var body strings.Builder
	body.WriteString(`<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`)
	for _, p := range paragraphs {
		fmt.Fprintf(&body, `<w:p><w:r><w:t>%s</w:t></w:r></w:p>`, p)
	}
	body.WriteString(`</w:body></w:document>`)
	if _, err := w.Write([]byte(body.String())); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
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

// addCandidate posts the manual-add form the way the browser does.
func (s *session) addCandidate(fields map[string]string, filename string, file []byte) (*http.Response, string) {
	s.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fields[middleware.CSRFField] = s.csrf()
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			s.t.Fatal(err)
		}
	}
	if filename != "" {
		part, err := mw.CreateFormFile("resume", filename)
		if err != nil {
			s.t.Fatal(err)
		}
		if _, err := part.Write(file); err != nil {
			s.t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		s.t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, s.f.srv.URL+"/app/candidates", &body)
	if err != nil {
		s.t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := s.cli.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

func TestCandidateListSearchesResumeText(t *testing.T) {
	f := newFixture(t)
	f.seedApplicant(t)
	s := f.browser(t)
	s.login(f.recruiterEmail)

	res, body := s.get("/app/candidates")
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "Ada Lovelace") {
		t.Fatalf("list = %d, body %q", res.StatusCode, body)
	}
	if res, body := s.get("/app/candidates?q=Erlang"); res.StatusCode != http.StatusOK || !strings.Contains(body, "Ada Lovelace") {
		t.Fatalf("resume-text search = %d, body %q", res.StatusCode, body)
	}
	res, body = s.get("/app/candidates?q=kubernetes")
	if res.StatusCode != http.StatusOK || strings.Contains(body, "Ada Lovelace") {
		t.Fatalf("search for an absent term still listed the candidate: %q", body)
	}
}

func TestCandidateListNeedsASession(t *testing.T) {
	f := newFixture(t)
	s := f.browser(t)
	res, _ := s.get("/app/candidates")
	if res.StatusCode != http.StatusSeeOther || !strings.Contains(res.Header.Get("Location"), "/app/login") {
		t.Fatalf("anonymous list = %d %s, want a login redirect", res.StatusCode, res.Header.Get("Location"))
	}
}

func TestCandidateDetailShowsApplicationsAndResumeDownload(t *testing.T) {
	f := newFixture(t)
	cand := f.seedApplicant(t)
	s := f.browser(t)
	s.login(f.recruiterEmail)

	res, body := s.get("/app/candidates/" + cand.ID.String())
	if res.StatusCode != http.StatusOK {
		t.Fatalf("detail = %d, body %q", res.StatusCode, body)
	}
	for _, want := range []string{"Ada Lovelace", "Senior Go Engineer", f.stageName, "ada.docx"} {
		if !strings.Contains(body, want) {
			t.Errorf("detail page does not mention %q", want)
		}
	}

	detail, err := f.candidates.Detail(context.Background(), service.Principal{
		Kind: service.PrincipalOrgUser, OrgID: f.orgID, Roles: []string{service.RoleRecruiter},
	}, cand.ID)
	if err != nil {
		t.Fatal(err)
	}
	res, _ = s.get("/app/candidates/" + cand.ID.String() + "/resumes/" + detail.Resumes[0].ID.String())
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("resume download = %d, want a redirect to the signed URL", res.StatusCode)
	}
	if loc := res.Header.Get("Location"); !strings.HasPrefix(loc, "https://blob.example/") {
		t.Errorf("resume download went to %q", loc)
	}
}

func TestRecruiterAddsACandidateManually(t *testing.T) {
	f := newFixture(t)
	s := f.browser(t)
	s.login(f.recruiterEmail)
	s.get("/app/candidates/new")

	res, body := s.addCandidate(map[string]string{
		"name": "Grace Hopper", "email": "grace@example.com", "job_id": f.jobID.String(),
	}, "grace.docx", docxBytes(t, "COBOL and compilers"))
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("manual add = %d, body %q", res.StatusCode, body)
	}
	_, page := s.get(res.Header.Get("Location"))
	for _, want := range []string{"Grace Hopper", "Senior Go Engineer"} {
		if !strings.Contains(page, want) {
			t.Errorf("candidate page after add does not mention %q: %q", want, page)
		}
	}
}

func TestAddingACandidateIsRecruiterOnly(t *testing.T) {
	f := newFixture(t)
	s := f.browser(t)
	s.login(f.vetterEmail)

	// A vetter still reads the list: they score the people in it.
	if res, _ := s.get("/app/candidates"); res.StatusCode != http.StatusOK {
		t.Errorf("vetter list = %d, want 200", res.StatusCode)
	}
	res, _ := s.addCandidate(map[string]string{"name": "Grace Hopper", "email": "grace@example.com"}, "", nil)
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("vetter add = %d, want 403", res.StatusCode)
	}
}

func TestAResumeURLIsScopedToItsCandidate(t *testing.T) {
	f := newFixture(t)
	owner := f.seedApplicant(t)
	s := f.browser(t)
	s.login(f.recruiterEmail)

	detail, err := f.candidates.Detail(context.Background(), service.Principal{
		Kind: service.PrincipalOrgUser, OrgID: f.orgID, Roles: []string{service.RoleRecruiter},
	}, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.candidates.Add(context.Background(), service.Principal{
		Kind: service.PrincipalOrgUser, OrgID: f.orgID, Roles: []string{service.RoleRecruiter},
	}, service.NewCandidate{Name: "Alan Turing", Email: "alan@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	res, _ := s.get("/app/candidates/" + other.ID.String() + "/resumes/" + detail.Resumes[0].ID.String())
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("another candidate's resume URL = %d, want 404", res.StatusCode)
	}
}

func TestManualAddRefusesAnOversizedRequestBody(t *testing.T) {
	f := newFixture(t)
	s := f.browser(t)
	s.login(f.recruiterEmail)
	s.get("/app/candidates/new")

	huge := bytes.Repeat([]byte("a"), apply.UploadBodyLimit+1<<20)
	res, _ := s.addCandidate(map[string]string{"name": "Grace Hopper", "email": "grace@example.com"}, "big.pdf", huge)
	if res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body = %d, want 413", res.StatusCode)
	}
}
