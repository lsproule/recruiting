//go:build integration

package apply_test

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

	"recruiting/internal/domain"
	"recruiting/internal/service"
	"recruiting/internal/store"
	"recruiting/internal/web/apply"
	"recruiting/internal/web/auth"
	"recruiting/internal/web/middleware"
)

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
	srv        *httptest.Server
	candidates *service.CandidateService
	orgID      uuid.UUID
	orgSlug    string
	openSlug   string
	draftSlug  string
	stageName  string
	// A second org recruiting under the same job slug, which a slug-only
	// public URL would confuse with the first.
	rivalOrgSlug string
	rivalTitle   string
}

// applyURL is the public address of a job: org slug, then job slug.
func (f *fixture) applyURL(orgSlug, jobSlug string) string {
	return "/apply/" + orgSlug + "/" + jobSlug
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

	f := &fixture{orgID: uuid.New(), stageName: "Applied", rivalTitle: "Rival Role"}
	f.orgSlug = f.orgID.String()
	f.openSlug = "go-engineer-" + strings.SplitN(f.orgID.String(), "-", 2)[0]
	f.draftSlug = "draft-" + strings.SplitN(f.orgID.String(), "-", 2)[0]
	if _, err := sys.Exec(ctx, `insert into org (id, name, slug) values ($1, $2, $2)`, f.orgID, f.orgID.String()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = sys.Exec(context.Background(), `delete from org where id = $1`, f.orgID) })
	companyID := uuid.New()
	if _, err := sys.Exec(ctx, `insert into client_company (id, org_id, name) values ($1, $2, 'Globex')`, companyID, f.orgID); err != nil {
		t.Fatal(err)
	}
	seedJob := func(orgID, companyID uuid.UUID, title, slug, status string) {
		jobID := uuid.New()
		_, err := sys.Exec(ctx, `insert into job (id, org_id, client_company_id, title, slug, status)
			values ($1, $2, $3, $4, $5, $6)`, jobID, orgID, companyID, title, slug, status)
		if err != nil {
			t.Fatal(err)
		}
		_, err = sys.Exec(ctx, `insert into stage (org_id, job_id, position, name, kind) values ($1, $2, 1, $3, 'generic')`, orgID, jobID, f.stageName)
		if err != nil {
			t.Fatal(err)
		}
	}
	seedJob(f.orgID, companyID, "Senior Go Engineer", f.openSlug, "open")
	seedJob(f.orgID, companyID, "Senior Go Engineer", f.draftSlug, "draft")

	// The rival org recruits for the same slug; each URL must serve its own.
	rivalOrgID, rivalCompanyID := uuid.New(), uuid.New()
	f.rivalOrgSlug = rivalOrgID.String()
	if _, err := sys.Exec(ctx, `insert into org (id, name, slug) values ($1, $2, $2)`, rivalOrgID, f.rivalOrgSlug); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = sys.Exec(context.Background(), `delete from org where id = $1`, rivalOrgID) })
	if _, err := sys.Exec(ctx, `insert into client_company (id, org_id, name) values ($1, $2, 'Initech')`, rivalCompanyID, rivalOrgID); err != nil {
		t.Fatal(err)
	}
	seedJob(rivalOrgID, rivalCompanyID, f.rivalTitle, f.openSlug, "open")

	resumes := service.NewResumeService(st, &memBlob{objects: map[string][]byte{}})
	f.candidates = service.NewCandidateService(st, resumes, nil)
	mux := chi.NewMux()
	// The body cap goes on before CSRF, which is where an unbounded upload
	// would otherwise be parsed.
	mux.Use(apply.MaxBody(apply.UploadBodyLimit))
	if _, err := auth.Mount(mux, auth.Deps{Auth: service.NewAuthService(st), CookieSecret: []byte("test-secret")}); err != nil {
		t.Fatal(err)
	}
	apply.Mount(mux, apply.Deps{Candidates: f.candidates})
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

type visitor struct {
	t   *testing.T
	f   *fixture
	cli *http.Client
}

func (f *fixture) visitor(t *testing.T) *visitor {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	return &visitor{t: t, f: f, cli: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (v *visitor) get(path string) (*http.Response, string) {
	v.t.Helper()
	res, err := v.cli.Get(v.f.srv.URL + path)
	if err != nil {
		v.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

func (v *visitor) csrf() string {
	v.t.Helper()
	u, _ := url.Parse(v.f.srv.URL)
	for _, c := range v.cli.Jar.Cookies(u) {
		if c.Name == middleware.CSRFCookie {
			return c.Value
		}
	}
	v.t.Fatal("no csrf cookie; load the form first")
	return ""
}

// submit posts the apply form as a browser would: multipart, with the file
// part named resume.
func (v *visitor) submit(path string, fields map[string]string, filename string, file []byte) (*http.Response, string) {
	v.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fields[middleware.CSRFField] = v.csrf()
	for k, val := range fields {
		if err := mw.WriteField(k, val); err != nil {
			v.t.Fatal(err)
		}
	}
	if filename != "" {
		part, err := mw.CreateFormFile("resume", filename)
		if err != nil {
			v.t.Fatal(err)
		}
		if _, err := part.Write(file); err != nil {
			v.t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		v.t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, v.f.srv.URL+path, &body)
	if err != nil {
		v.t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := v.cli.Do(req)
	if err != nil {
		v.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
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

func applyFields(email string) map[string]string {
	return map[string]string{
		"name": "Ada Lovelace", "email": email, "phone": "+44 20 7946 0000",
		"links": "https://example.com/ada",
	}
}

func TestApplyPageAcceptsAnApplication(t *testing.T) {
	f := newFixture(t)
	v := f.visitor(t)

	res, page := v.get(f.applyURL(f.orgSlug, f.openSlug))
	if res.StatusCode != http.StatusOK || !strings.Contains(page, "Senior Go Engineer") {
		t.Fatalf("apply page = %d, body %q", res.StatusCode, page)
	}

	res, body := v.submit(f.applyURL(f.orgSlug, f.openSlug), applyFields("ada@example.com"), "ada.docx", docxBytes(t, "Ada Lovelace", "Elixir and Erlang"))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("submit = %d, body %q", res.StatusCode, body)
	}
	if !strings.Contains(strings.ToLower(body), "thank") {
		t.Errorf("no confirmation in %q", body)
	}

	p := service.Principal{Kind: service.PrincipalOrgUser, OrgID: f.orgID, Roles: []string{service.RoleRecruiter}}
	hits, err := f.candidates.Search(context.Background(), p, "Erlang")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Email != "ada@example.com" {
		t.Fatalf("search after apply returned %+v", hits)
	}
}

func TestApplyPageRefusesTheSecondApplication(t *testing.T) {
	f := newFixture(t)
	v := f.visitor(t)
	v.get(f.applyURL(f.orgSlug, f.openSlug))
	resume := docxBytes(t, "Ada Lovelace")
	if res, body := v.submit(f.applyURL(f.orgSlug, f.openSlug), applyFields("ada@example.com"), "ada.docx", resume); res.StatusCode != http.StatusOK {
		t.Fatalf("first submit = %d %q", res.StatusCode, body)
	}
	res, body := v.submit(f.applyURL(f.orgSlug, f.openSlug), applyFields("ADA@example.com"), "ada.docx", resume)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("second submit = %d, want 422; body %q", res.StatusCode, body)
	}
	if !strings.Contains(body, "already applied") {
		t.Errorf("second submit body %q does not say the candidate already applied", body)
	}
}

func TestApplyPageRejectsFilesByContentAndSize(t *testing.T) {
	f := newFixture(t)
	v := f.visitor(t)
	v.get(f.applyURL(f.orgSlug, f.openSlug))

	res, body := v.submit(f.applyURL(f.orgSlug, f.openSlug), applyFields("mallory@example.com"), "resume.pdf", []byte("MZ\x90\x00malware"))
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "PDF") {
		t.Fatalf("renamed executable = %d, body %q", res.StatusCode, body)
	}

	big := append([]byte("%PDF-1.4"), bytes.Repeat([]byte("a"), domain.MaxResumeBytes)...)
	res, body = v.submit(f.applyURL(f.orgSlug, f.openSlug), applyFields("big@example.com"), "big.pdf", big)
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "10 MB") {
		t.Fatalf("oversize upload = %d, body %q", res.StatusCode, body)
	}
}

func TestApplyPageHidesJobsThatAreNotOpen(t *testing.T) {
	f := newFixture(t)
	v := f.visitor(t)
	if res, _ := v.get(f.applyURL(f.orgSlug, f.draftSlug)); res.StatusCode != http.StatusNotFound {
		t.Errorf("draft job apply page = %d, want 404", res.StatusCode)
	}
	if res, _ := v.get(f.applyURL(f.orgSlug, "no-such-job")); res.StatusCode != http.StatusNotFound {
		t.Errorf("unknown slug = %d, want 404", res.StatusCode)
	}
}

func TestApplyPageEnforcesCSRF(t *testing.T) {
	f := newFixture(t)
	v := f.visitor(t)
	v.get(f.applyURL(f.orgSlug, f.openSlug))
	fields := applyFields("ada@example.com")
	fields[middleware.CSRFField] = "wrong"
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, val := range fields {
		_ = mw.WriteField(k, val)
	}
	_ = mw.Close()
	req, _ := http.NewRequest(http.MethodPost, f.srv.URL+f.applyURL(f.orgSlug, f.openSlug), &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := v.cli.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("bad CSRF token = %d, want 403", res.StatusCode)
	}
}

func TestEachOrgsApplyURLServesItsOwnJob(t *testing.T) {
	f := newFixture(t)
	v := f.visitor(t)

	_, mine := v.get(f.applyURL(f.orgSlug, f.openSlug))
	if !strings.Contains(mine, "Senior Go Engineer") || strings.Contains(mine, f.rivalTitle) {
		t.Errorf("own org's URL served the wrong job: %q", mine)
	}
	_, theirs := v.get(f.applyURL(f.rivalOrgSlug, f.openSlug))
	if !strings.Contains(theirs, f.rivalTitle) || strings.Contains(theirs, "Senior Go Engineer") {
		t.Errorf("rival org's URL served the wrong job: %q", theirs)
	}
	// Crossing the two halves addresses nothing.
	if res, _ := v.get(f.applyURL("no-such-org", f.openSlug)); res.StatusCode != http.StatusNotFound {
		t.Errorf("unknown org slug = %d, want 404", res.StatusCode)
	}
}

func TestApplyPageRefusesAnOversizedRequestBody(t *testing.T) {
	f := newFixture(t)
	v := f.visitor(t)
	v.get(f.applyURL(f.orgSlug, f.openSlug))

	// Past the whole-request cap, so the body is refused before any form
	// parsing spools it anywhere.
	huge := bytes.Repeat([]byte("a"), apply.UploadBodyLimit+1<<20)
	res, _ := v.submit(f.applyURL(f.orgSlug, f.openSlug), applyFields("big@example.com"), "big.pdf", huge)
	if res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body = %d, want 413", res.StatusCode)
	}
}
