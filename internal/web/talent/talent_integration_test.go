//go:build integration

package talent_test

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
	"regexp"
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
	"recruiting/internal/web/apply"
	"recruiting/internal/web/auth"
	"recruiting/internal/web/layout"
	"recruiting/internal/web/middleware"
	"recruiting/internal/web/talent"
)

const testPassword = "hunter2-long-enough"

type memBlob struct{ objects map[string][]byte }

func (b *memBlob) Put(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	body, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	b.objects[key] = body
	return nil
}
func (b *memBlob) Delete(_ context.Context, key string) error { delete(b.objects, key); return nil }
func (b *memBlob) SignedGetURL(_ context.Context, key, filename string, _ time.Duration) (string, error) {
	return "https://blob.example/" + key + "?name=" + url.QueryEscape(filename), nil
}

type fixture struct {
	srv            *httptest.Server
	sys            *pgxpool.Pool
	talent         *service.TalentService
	links          *service.MagicLinkService
	orgID          uuid.UUID
	orgSlug        string
	companyID      uuid.UUID
	clientID       uuid.UUID
	jobID          uuid.UUID
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

	f := &fixture{sys: sys, orgID: uuid.New(), companyID: uuid.New(), clientID: uuid.New(), jobID: uuid.New()}
	f.orgSlug = f.orgID.String()
	f.recruiterEmail = "rec-" + f.orgID.String() + "@example.com"
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := sys.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`insert into org (id, name, slug) values ($1, 'Northwind', $2)`, f.orgID, f.orgSlug)
	t.Cleanup(func() { _, _ = sys.Exec(context.Background(), `delete from org where id = $1`, f.orgID) })
	hash, _ := service.HashPassword(testPassword)
	recruiterID := uuid.New()
	exec(`insert into org_user (id, org_id, email, name) values ($1, $2, $3, 'Rita Recruiter')`, recruiterID, f.orgID, f.recruiterEmail)
	exec(`insert into org_user_role (org_user_id, org_id, role) values ($1, $2, 'recruiter')`, recruiterID, f.orgID)
	exec(`insert into org_user_credential (org_user_id, org_id, password_hash) values ($1, $2, $3)`, recruiterID, f.orgID, hash)
	exec(`insert into client_company (id, org_id, name) values ($1, $2, 'Globex')`, f.companyID, f.orgID)
	exec(`insert into client_user (id, org_id, client_company_id, email, name) values ($1, $2, $3, $4, 'Carl Client')`, f.clientID, f.orgID, f.companyID, "client-"+f.orgID.String()+"@example.com")
	exec(`insert into job (id, org_id, client_company_id, title, slug, status, skills) values ($1, $2, $3, 'Platform Engineer', $4, 'open', '{go,postgres}')`, f.jobID, f.orgID, f.companyID, "plat-"+f.orgID.String())
	exec(`insert into stage (org_id, job_id, position, name, kind) values ($1, $2, 1, 'Applied', 'generic')`, f.orgID, f.jobID)

	q, err := queue.New(st.Pool(), queue.Config{})
	if err != nil {
		t.Fatal(err)
	}
	resumes := service.NewResumeService(st, &memBlob{objects: map[string][]byte{}})
	f.links = service.NewMagicLinkService(st)
	f.talent = service.NewTalentService(st, resumes, f.links, q, "https://example.test")

	mux := chi.NewMux()
	mux.Use(apply.MaxBody(apply.UploadBodyLimit))
	if _, err := auth.Mount(mux, auth.Deps{Auth: service.NewAuthService(st), CookieSecret: []byte("test-secret")}); err != nil {
		t.Fatal(err)
	}
	layout.MountStatic(mux)
	talent.Mount(mux, talent.Deps{Talent: f.talent, Links: f.links})
	talent.MountApp(mux, talent.Deps{Talent: f.talent, Org: service.NewOrgService(st)})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func lockSchema(t *testing.T, ownerURL string) {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(context.Background(), `select pg_advisory_lock_shared(7371)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
}

func (f *fixture) client() service.Principal {
	return service.Principal{Kind: service.PrincipalClientUser, OrgID: f.orgID, UserID: f.clientID, ClientCompanyID: f.companyID}
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
	v.t.Fatal("no csrf cookie; load a page first")
	return ""
}

// post sends a form; a filename adds a résumé part.
func (v *visitor) post(path string, fields map[string]string, filename string, file []byte) (*http.Response, string) {
	v.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fields[middleware.CSRFField] = v.csrf()
	for k, val := range fields {
		_ = mw.WriteField(k, val)
	}
	if filename != "" {
		part, _ := mw.CreateFormFile("resume", filename)
		_, _ = part.Write(file)
	}
	_ = mw.Close()
	req, _ := http.NewRequest(http.MethodPost, v.f.srv.URL+path, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := v.cli.Do(req)
	if err != nil {
		v.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

func (v *visitor) login(email string) {
	v.t.Helper()
	v.get("/app/login")
	form := url.Values{"email": {email}, "password": {testPassword}, middleware.CSRFField: {v.csrf()}}
	res, err := v.cli.PostForm(v.f.srv.URL+"/app/login", form)
	if err != nil {
		v.t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusSeeOther {
		v.t.Fatalf("login: %d", res.StatusCode)
	}
}

// token is the raw secret of the newest link of a purpose for a subject,
// re-issued here since the service only mails it.
func (f *fixture) token(t *testing.T, purpose string, subject uuid.UUID) string {
	t.Helper()
	tok, _, err := f.links.Issue(context.Background(), service.Principal{Kind: service.PrincipalSystem, OrgID: f.orgID}, purpose, subject, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

var joinFields = map[string]string{
	"name": "Grace Hopper", "email": "grace@example.com", "headline": "Compiler engineer",
	"skills": "go, postgres", "seniority": "senior", "location": "Berlin", "remote_policy": "remote",
	"salary_min": "90000", "available_from": "2027-01-04", "consent": "1",
}

func fields(over map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range joinFields {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}

func TestJoinPageFilesAProfileAndTheMemberManagesIt(t *testing.T) {
	f := newFixture(t)
	v := f.visitor(t)
	res, body := v.get(talent.JoinPath(f.orgSlug))
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "Join the talent network") || !strings.Contains(body, "Northwind") {
		t.Fatalf("join page: %d %s", res.StatusCode, body[:200])
	}
	if res, _ := v.get(talent.JoinPath("nobody")); res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown org: %d, want 404", res.StatusCode)
	}
	// Without consent the form comes back with the reason and nothing is filed.
	res, body = v.post(talent.JoinPath(f.orgSlug), fields(map[string]string{"consent": ""}), "", nil)
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "consent") {
		t.Fatalf("no consent: %d", res.StatusCode)
	}
	res, body = v.post(talent.JoinPath(f.orgSlug), fields(nil), "grace.docx", docxBytes(t, "Grace Hopper", "Kubernetes at scale"))
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "You are in") {
		t.Fatalf("join: %d %s", res.StatusCode, body[:300])
	}
	var profileID uuid.UUID
	if err := f.sys.QueryRow(context.Background(), `select p.id from talent_profile p join candidate c on c.id = p.candidate_id where c.email = 'grace@example.com' and p.org_id = $1`, f.orgID).Scan(&profileID); err != nil {
		t.Fatalf("profile row: %v", err)
	}

	// The member's own page reads and rewrites the profile.
	me := talent.ProfilePrefix + "/" + f.token(t, service.LinkProfile, profileID)
	res, body = v.get(me)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "Hi Grace Hopper") || !strings.Contains(body, `value="go, postgres"`) || !strings.Contains(body, "Replace your résumé") {
		t.Fatalf("profile page: %d %s", res.StatusCode, body[:300])
	}
	res, body = v.post(me, fields(map[string]string{"skills": "go, elixir", "location": "Lisbon"}), "", nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "Saved.") || !strings.Contains(body, `value="Lisbon"`) {
		t.Fatalf("update: %d %s", res.StatusCode, body[:300])
	}
	res, body = v.post(me+"/withdraw", map[string]string{}, "", nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "left the network") || !strings.Contains(body, "Rejoin the network") {
		t.Fatalf("withdraw: %d", res.StatusCode)
	}
	res, body = v.post(me+"/rejoin", map[string]string{}, "", nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "Welcome back") {
		t.Fatalf("rejoin: %d", res.StatusCode)
	}
	// A link of the wrong purpose does not open the page.
	if res, _ := v.get(talent.ProfilePrefix + "/" + f.token(t, service.LinkOpportunity, profileID)); res.StatusCode != http.StatusGone {
		t.Fatalf("wrong purpose: %d, want 410", res.StatusCode)
	}
}

func TestRecruiterSendsTheOpportunityAndTheCandidateAnswers(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	prof, err := f.talent.Join(ctx, f.orgSlug, service.TalentProfileInput{
		Name: "Ada Lovelace", Email: "ada@example.com", Skills: []string{"go", "postgres"}, Consent: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := f.talent.CreateRequest(ctx, f.client(), service.TalentRequestInput{Title: "Go engineer", Skills: []string{"go"}, JobID: f.jobID})
	if err != nil {
		t.Fatal(err)
	}
	intro, err := f.talent.Introduce(ctx, f.client(), req.ID, prof.ID)
	if err != nil {
		t.Fatal(err)
	}

	rec := f.visitor(t)
	rec.login(f.recruiterEmail)
	res, body := rec.get(talent.AppPrefix)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "Go engineer") || !strings.Contains(body, "1 to send") || !strings.Contains(body, "Ada Lovelace") {
		t.Fatalf("talent overview: %d %s", res.StatusCode, snippet(body, "Go engineer"))
	}
	res, body = rec.get(talent.AppPrefix + "?q=postgres")
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "Ada Lovelace") {
		t.Fatalf("network search: %d", res.StatusCode)
	}
	res, body = rec.get(talent.RequestPath(req.ID))
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "Ada Lovelace") || !strings.Contains(body, "Send opportunity") || !strings.Contains(body, "Platform Engineer") {
		t.Fatalf("request page: %d %s", res.StatusCode, snippet(body, "Send"))
	}
	res, _ = rec.post(talent.RequestPath(req.ID)+"/introductions/"+intro.ID.String()+"/send", map[string]string{"job_id": f.jobID.String()}, "", nil)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("send: %d", res.StatusCode)
	}
	if res, body = rec.get(talent.RequestPath(req.ID) + "?did=send"); !strings.Contains(body, "on its way") || !strings.Contains(body, ">Sent<") {
		t.Fatalf("after send: %s", snippet(body, "Sent"))
	}
	var n int
	if err := f.sys.QueryRow(ctx, `select count(*) from river_job where kind = $1 and args->'payload'->>'template' = 'opportunity' and args->'payload'->>'org_id' = $2`, queue.KindEmailSend, f.orgID.String()).Scan(&n); err != nil || n != 1 {
		t.Fatalf("opportunity emails = %d (%v)", n, err)
	}
	// The candidate's page shows the role and takes the answer; a second
	// answer is refused with the page re-rendered, not a 500.
	cand := f.visitor(t)
	them := talent.OpportunityPrefix + "/" + f.token(t, service.LinkOpportunity, intro.ID)
	res, body = cand.get(them)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "Platform Engineer at Globex") || !strings.Contains(body, "interested") {
		t.Fatalf("opportunity page: %d %s", res.StatusCode, snippet(body, "Globex"))
	}
	res, body = cand.post(them, map[string]string{"answer": "yes"}, "", nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "You are in the running") {
		t.Fatalf("accept: %d %s", res.StatusCode, snippet(body, "running"))
	}
	if res, _ = cand.post(them, map[string]string{"answer": "no"}, "", nil); res.StatusCode != http.StatusConflict {
		t.Fatalf("answering twice: %d, want 409", res.StatusCode)
	}
	if res, body = rec.get(talent.RequestPath(req.ID)); !strings.Contains(body, ">Accepted<") || !strings.Contains(body, "/app/applications/") {
		t.Fatalf("after acceptance: %s", snippet(body, "Accepted"))
	}
	// A vetter has no business here.
	vetterID := uuid.New()
	hash, _ := service.HashPassword(testPassword)
	vetterEmail := "vet-" + f.orgID.String() + "@example.com"
	if _, err := f.sys.Exec(ctx, `insert into org_user (id, org_id, email, name) values ($1, $2, $3, 'V')`, vetterID, f.orgID, vetterEmail); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sys.Exec(ctx, `insert into org_user_role (org_user_id, org_id, role) values ($1, $2, 'vetter')`, vetterID, f.orgID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sys.Exec(ctx, `insert into org_user_credential (org_user_id, org_id, password_hash) values ($1, $2, $3)`, vetterID, f.orgID, hash); err != nil {
		t.Fatal(err)
	}
	vet := f.visitor(t)
	vet.login(vetterEmail)
	if res, _ := vet.get(talent.AppPrefix); res.StatusCode != http.StatusForbidden {
		t.Fatalf("vetter on the talent screen: %d, want 403", res.StatusCode)
	}
}

var idInPath = regexp.MustCompile(`[0-9a-f-]{36}`)

func snippet(body, around string) string {
	i := strings.Index(body, around)
	if i < 0 {
		return body[:min(len(body), 300)]
	}
	return body[max(0, i-200):min(len(body), i+200)]
}

var _ = idInPath

// docxBytes builds the smallest file Word would open: a zip whose
// word/document.xml holds one paragraph per line.
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
