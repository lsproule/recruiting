//go:build integration

package service_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"recruiting/internal/domain"
	"recruiting/internal/mail"
	"recruiting/internal/queue"
	"recruiting/internal/service"
)

// fakeBlob stands in for object storage so the service tests need only
// Postgres; internal/blob has its own MinIO integration test.
type fakeBlob struct {
	mu      sync.Mutex
	objects map[string][]byte
	putErr  error
	delErr  error
}

func newFakeBlob() *fakeBlob { return &fakeBlob{objects: map[string][]byte{}} }

func (b *fakeBlob) Put(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	if b.putErr != nil {
		return b.putErr
	}
	body, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.objects[key] = body
	return nil
}

func (b *fakeBlob) Delete(_ context.Context, key string) error {
	if b.delErr != nil {
		return b.delErr
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.objects, key)
	return nil
}

func (b *fakeBlob) has(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.objects[key]
	return ok
}

func (b *fakeBlob) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.objects)
}

func (b *fakeBlob) SignedGetURL(_ context.Context, key, filename string, _ time.Duration) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.objects[key]; !ok {
		return "", fmt.Errorf("fake blob: no object %s", key)
	}
	return "https://blob.example/" + key + "?filename=" + filename, nil
}

// candidateFixture is a job fixture plus the candidate and resume services.
type candidateFixture struct {
	*jobFixture
	blob       *fakeBlob
	candidates *service.CandidateService
	resumes    *service.ResumeService
}

// orgSlug is the slug newFixture gives the seeded org; the public apply URL
// names it ahead of the job slug.
func (f *candidateFixture) orgSlug() string { return f.orgID.String() }

func newCandidateFixture(t *testing.T) *candidateFixture {
	t.Helper()
	jf := newJobFixture(t)
	b := newFakeBlob()
	resumes := service.NewResumeService(jf.st, b)
	q, err := queue.New(jf.st.Pool(), queue.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return &candidateFixture{jobFixture: jf, blob: b, candidates: service.NewCandidateService(jf.st, resumes, q), resumes: resumes}
}

// emails counts queued email.send jobs of template addressed to the given
// recipient in the fixture's org.
func (f *candidateFixture) emails(t *testing.T, template, to string) int {
	t.Helper()
	var n int
	err := f.sys.QueryRow(f.ctx, `select count(*) from river_job where kind = $1 and args->'payload'->>'template' = $2
		and args->'payload'->>'to' = $3 and args->'payload'->>'org_id' = $4`,
		queue.KindEmailSend, template, to, f.orgID.String()).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestApplyQueuesOneAcknowledgementEmail(t *testing.T) {
	f := newCandidateFixture(t)
	job := f.newJob(t)

	got, err := f.candidates.Apply(f.ctx, f.orgSlug(), job.Slug, applyInput("ada@example.com", docxBytes(t, "Ada Lovelace")))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if n := f.emails(t, mail.TemplateApplyReceived, got.Email); n != 1 {
		t.Fatalf("queued %d apply_received emails, want exactly 1", n)
	}
	var data map[string]any
	var raw string
	err = f.sys.QueryRow(f.ctx, `select args->'payload'->>'data' from river_job where kind = $1 and args->'payload'->>'template' = $2 and args->'payload'->>'to' = $3`,
		queue.KindEmailSend, mail.TemplateApplyReceived, got.Email).Scan(&raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		t.Fatal(err)
	}
	if data["CandidateName"] != "Ada Lovelace" || data["JobTitle"] != job.Title || data["OrgName"] == "" || data["OrgName"] == nil {
		t.Errorf("email data = %v, want CandidateName, JobTitle, and OrgName set", data)
	}

	// A refused repeat application queues nothing more.
	if _, err := f.candidates.Apply(f.ctx, f.orgSlug(), job.Slug, applyInput("ada@example.com", docxBytes(t, "Ada"))); !errors.Is(err, service.ErrAlreadyApplied) {
		t.Fatalf("second apply error = %v, want ErrAlreadyApplied", err)
	}
	if n := f.emails(t, mail.TemplateApplyReceived, got.Email); n != 1 {
		t.Errorf("queued %d apply_received emails after a refused repeat, want still 1", n)
	}
}

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

func applyInput(email string, resume []byte) service.ApplyInput {
	return service.ApplyInput{
		Name: "Ada Lovelace", Email: email, Phone: "+44 20 7946 0000",
		Links:  []string{"https://example.com/ada"},
		Resume: service.ResumeUpload{Filename: "ada.docx", Data: resume},
	}
}

func TestApplyCreatesCandidateApplicationAndSearchableResume(t *testing.T) {
	f := newCandidateFixture(t)
	job := f.newJob(t)
	email := "Ada@Example.com"

	got, err := f.candidates.Apply(f.ctx, f.orgSlug(), job.Slug, applyInput(email, docxBytes(t, "Ada Lovelace", "Ten years of Elixir and Erlang")))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got.Email != strings.ToLower(email) {
		t.Errorf("stored email %q, want it normalised", got.Email)
	}

	detail, err := f.candidates.Detail(f.ctx, f.recruiter(), got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Applications) != 1 {
		t.Fatalf("candidate has %d applications, want 1", len(detail.Applications))
	}
	stages, err := f.jobs.Stages(f.ctx, f.recruiter(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Applications[0].StageName != stages[0].Name {
		t.Errorf("application landed in %q, want the first stage %q", detail.Applications[0].StageName, stages[0].Name)
	}
	if len(detail.Resumes) != 1 || detail.Resumes[0].TextStatus != service.ResumeExtracted {
		t.Fatalf("resumes = %+v, want one extracted", detail.Resumes)
	}

	// The search vector spans the resume text, not just the name.
	hits, err := f.candidates.Search(f.ctx, f.recruiter(), "Erlang")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != got.ID {
		t.Fatalf("search for resume text returned %d rows, want the applicant", len(hits))
	}
	if hits[0].ApplicationCount != 1 {
		t.Errorf("application count = %d, want 1", hits[0].ApplicationCount)
	}

	// A term in neither the name nor the resume must not match.
	miss, err := f.candidates.Search(f.ctx, f.recruiter(), "kubernetes")
	if err != nil {
		t.Fatal(err)
	}
	if len(miss) != 0 {
		t.Errorf("search for an absent term returned %d rows", len(miss))
	}
}

func TestApplyTwiceToTheSameJobIsRefusedFriendly(t *testing.T) {
	f := newCandidateFixture(t)
	job := f.newJob(t)
	resume := docxBytes(t, "Ada Lovelace")
	if _, err := f.candidates.Apply(f.ctx, f.orgSlug(), job.Slug, applyInput("ada@example.com", resume)); err != nil {
		t.Fatal(err)
	}
	_, err := f.candidates.Apply(f.ctx, f.orgSlug(), job.Slug, applyInput("ADA@example.com", resume))
	if !errors.Is(err, service.ErrAlreadyApplied) {
		t.Fatalf("second apply error = %v, want ErrAlreadyApplied", err)
	}
}

func TestApplyRejectsResumesByContentAndSize(t *testing.T) {
	f := newCandidateFixture(t)
	job := f.newJob(t)

	// A Windows executable renamed resume.pdf is still an executable.
	in := applyInput("mallory@example.com", []byte("MZ\x90\x00malware"))
	in.Resume.Filename = "resume.pdf"
	if _, err := f.candidates.Apply(f.ctx, f.orgSlug(), job.Slug, in); !errors.Is(err, domain.ErrResumeType) {
		t.Fatalf("renamed executable error = %v, want ErrResumeType", err)
	}

	big := applyInput("big@example.com", append([]byte("%PDF-1.4"), bytes.Repeat([]byte("a"), domain.MaxResumeBytes)...))
	if _, err := f.candidates.Apply(f.ctx, f.orgSlug(), job.Slug, big); !errors.Is(err, domain.ErrResumeTooLarge) {
		t.Fatalf("oversize error = %v, want ErrResumeTooLarge", err)
	}

	// Neither attempt may leave a candidate behind.
	hits, err := f.candidates.Search(f.ctx, f.recruiter(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Errorf("a rejected upload created %d candidates", len(hits))
	}
}

func TestApplyStoresTheApplicationWhenExtractionFails(t *testing.T) {
	f := newCandidateFixture(t)
	job := f.newJob(t)

	// Passes the PDF sniff but has no readable structure.
	broken := []byte("%PDF-1.4 this is not actually a pdf body")
	cand, err := f.candidates.Apply(f.ctx, f.orgSlug(), job.Slug, applyInput("grace@example.com", broken))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	detail, err := f.candidates.Detail(f.ctx, f.recruiter(), cand.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Applications) != 1 {
		t.Fatalf("failed extraction lost the application: %+v", detail.Applications)
	}
	if len(detail.Resumes) != 1 || detail.Resumes[0].TextStatus != service.ResumeFailed {
		t.Fatalf("resumes = %+v, want one marked failed", detail.Resumes)
	}
}

func TestApplyOnlyReachesOpenJobs(t *testing.T) {
	f := newCandidateFixture(t)
	job, err := f.jobs.CreateJob(f.ctx, f.recruiter(), service.NewJob{
		ClientCompanyID: f.companyID, Title: "Draft Role", Status: service.JobDraft,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.candidates.PublicJob(f.ctx, f.orgSlug(), job.Slug); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("public lookup of a draft job = %v, want ErrNotFound", err)
	}
	_, err = f.candidates.Apply(f.ctx, f.orgSlug(), job.Slug, applyInput("ada@example.com", docxBytes(t, "Ada")))
	if !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("apply to a draft job = %v, want ErrNotFound", err)
	}
}

func TestAddCandidateManuallyWithAnApplication(t *testing.T) {
	f := newCandidateFixture(t)
	job := f.newJob(t)

	cand, err := f.candidates.Add(f.ctx, f.recruiter(), service.NewCandidate{
		Name: "Grace Hopper", Email: "grace@example.com", JobID: job.ID,
		Resume: &service.ResumeUpload{Filename: "grace.docx", Data: docxBytes(t, "COBOL and compilers")},
	})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	detail, err := f.candidates.Detail(f.ctx, f.recruiter(), cand.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Applications) != 1 || detail.Applications[0].JobID != job.ID {
		t.Fatalf("applications = %+v, want one on the chosen job", detail.Applications)
	}

	// A candidate added without a job is just a candidate.
	solo, err := f.candidates.Add(f.ctx, f.recruiter(), service.NewCandidate{Name: "Alan Turing", Email: "alan@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	soloDetail, err := f.candidates.Detail(f.ctx, f.recruiter(), solo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(soloDetail.Applications) != 0 {
		t.Errorf("applications = %+v, want none", soloDetail.Applications)
	}
}

func TestAddCandidateNeedsRecruiterAndSearchNeedsAnOrgUser(t *testing.T) {
	f := newCandidateFixture(t)
	outsider := service.Principal{Kind: service.PrincipalClientUser, OrgID: f.orgID, ClientCompanyID: f.companyID}
	if _, err := f.candidates.Add(f.ctx, outsider, service.NewCandidate{Name: "X", Email: "x@example.com"}); !errors.Is(err, service.ErrForbidden) {
		t.Errorf("client add = %v, want ErrForbidden", err)
	}
	if _, err := f.candidates.Search(f.ctx, outsider, ""); !errors.Is(err, service.ErrForbidden) {
		t.Errorf("client search = %v, want ErrForbidden", err)
	}
}

func TestResumeDownloadURLIsSignedAndOrgOnly(t *testing.T) {
	f := newCandidateFixture(t)
	job := f.newJob(t)
	cand, err := f.candidates.Apply(f.ctx, f.orgSlug(), job.Slug, applyInput("ada@example.com", docxBytes(t, "Ada Lovelace")))
	if err != nil {
		t.Fatal(err)
	}
	detail, err := f.candidates.Detail(f.ctx, f.recruiter(), cand.ID)
	if err != nil {
		t.Fatal(err)
	}
	url, err := f.candidates.ResumeURL(f.ctx, f.recruiter(), cand.ID, detail.Resumes[0].ID)
	if err != nil {
		t.Fatalf("resume url: %v", err)
	}
	if !strings.HasPrefix(url, "https://blob.example/") || !strings.Contains(url, "ada.docx") {
		t.Errorf("signed url = %q", url)
	}
	outsider := service.Principal{Kind: service.PrincipalClientUser, OrgID: f.orgID, ClientCompanyID: f.companyID}
	if _, err := f.candidates.ResumeURL(f.ctx, outsider, cand.ID, detail.Resumes[0].ID); !errors.Is(err, service.ErrForbidden) {
		t.Errorf("client download = %v, want ErrForbidden", err)
	}
	if _, err := f.candidates.ResumeURL(f.ctx, f.recruiter(), cand.ID, uuid.New()); !errors.Is(err, service.ErrNotFound) {
		t.Errorf("unknown resume = %v, want ErrNotFound", err)
	}
}

// otherOrgJob seeds a second org whose job carries the same slug as this
// fixture's, which is what a per-slug public lookup would confuse.
func (f *candidateFixture) otherOrgJob(t *testing.T, slug string) (orgSlug, title string) {
	t.Helper()
	orgID, companyID, jobID := uuid.New(), uuid.New(), uuid.New()
	orgSlug, title = orgID.String(), "Rival Role"
	if _, err := f.sys.Exec(f.ctx, `insert into org (id, name, slug) values ($1, $2, $2)`, orgID, orgSlug); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.sys.Exec(context.Background(), `delete from org where id = $1`, orgID) })
	if _, err := f.sys.Exec(f.ctx, `insert into client_company (id, org_id, name) values ($1, $2, 'Initech')`, companyID, orgID); err != nil {
		t.Fatal(err)
	}
	_, err := f.sys.Exec(f.ctx, `insert into job (id, org_id, client_company_id, title, slug, status)
		values ($1, $2, $3, $4, $5, 'open')`, jobID, orgID, companyID, title, slug)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.sys.Exec(f.ctx, `insert into stage (org_id, job_id, position, name, kind) values ($1, $2, 1, 'Applied', 'generic')`, orgID, jobID); err != nil {
		t.Fatal(err)
	}
	return orgSlug, title
}

func TestPublicJobLookupIsScopedToOneOrg(t *testing.T) {
	f := newCandidateFixture(t)
	job := f.newJob(t)
	rivalOrgSlug, rivalTitle := f.otherOrgJob(t, job.Slug)

	mine, err := f.candidates.PublicJob(f.ctx, f.orgSlug(), job.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if mine.ID != job.ID || mine.Title != job.Title {
		t.Errorf("own org's URL served %q (%s), want %q", mine.Title, mine.ID, job.Title)
	}
	theirs, err := f.candidates.PublicJob(f.ctx, rivalOrgSlug, job.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if theirs.Title != rivalTitle || theirs.ID == job.ID {
		t.Errorf("other org's URL served %q (%s), want %q", theirs.Title, theirs.ID, rivalTitle)
	}
	// An org slug that does not own the job resolves to nothing at all.
	if _, err := f.candidates.PublicJob(f.ctx, "no-such-org", job.Slug); !errors.Is(err, service.ErrNotFound) {
		t.Errorf("unknown org slug = %v, want ErrNotFound", err)
	}
}

// wordyDocx builds a resume of distinct tokens roughly bytes long, the shape
// that overflows a tsvector when it is indexed whole.
func wordyDocx(t *testing.T, bytes int) []byte {
	t.Helper()
	var body strings.Builder
	for i := 0; body.Len() < bytes; i++ {
		fmt.Fprintf(&body, "token%dqz ", i)
	}
	return docxBytes(t, "Ada Lovelace", body.String())
}

func TestAnEnormousResumeStillAppliesAndStaysSearchable(t *testing.T) {
	f := newCandidateFixture(t)
	job := f.newJob(t)

	cand, err := f.candidates.Apply(f.ctx, f.orgSlug(), job.Slug, applyInput("ada@example.com", wordyDocx(t, 2<<20)))
	if err != nil {
		t.Fatalf("apply with a very long resume: %v", err)
	}
	detail, err := f.candidates.Detail(f.ctx, f.recruiter(), cand.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Applications) != 1 {
		t.Fatalf("applications = %+v, want one", detail.Applications)
	}
	hits, err := f.candidates.Search(f.ctx, f.recruiter(), "Lovelace")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != cand.ID {
		t.Fatalf("search by name returned %d rows, want the applicant", len(hits))
	}
}

func TestAFailedApplicationLeavesNothingBehind(t *testing.T) {
	f := newCandidateFixture(t)
	// A job with no stages cannot take an application.
	job, err := f.jobs.CreateJob(f.ctx, f.recruiter(), service.NewJob{
		ClientCompanyID: f.companyID, Title: "Stageless Role", Status: service.JobOpen,
	})
	if err != nil {
		t.Fatal(err)
	}
	// The pipeline editor refuses to leave a job with no stages, so the rows
	// go directly: this is the state a half-configured job would be in.
	if _, err := f.sys.Exec(f.ctx, `delete from stage where job_id = $1`, job.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := f.candidates.Apply(f.ctx, f.orgSlug(), job.Slug, applyInput("ada@example.com", docxBytes(t, "Ada"))); !errors.Is(err, service.ErrNoStages) {
		t.Fatalf("apply to a stageless job = %v, want ErrNoStages", err)
	}
	found, err := f.candidates.Search(f.ctx, f.recruiter(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 {
		t.Errorf("a rolled back application left %d candidates", len(found))
	}
	if f.blob.count() != 0 {
		t.Errorf("a rolled back application left %d objects in storage", f.blob.count())
	}
}

func TestApplyingAgainKeepsTheNameOnFileAndMergesLinks(t *testing.T) {
	f := newCandidateFixture(t)
	first := f.newJob(t)
	second, err := f.jobs.CreateJob(f.ctx, f.recruiter(), service.NewJob{
		ClientCompanyID: f.companyID, Title: "Staff Go Engineer", Status: service.JobOpen,
	})
	if err != nil {
		t.Fatal(err)
	}

	in := applyInput("ada@example.com", docxBytes(t, "Ada Lovelace"))
	in.Name = "Ada Lovelace"
	in.Links = []string{"https://example.com/ada"}
	cand, err := f.candidates.Apply(f.ctx, f.orgSlug(), first.Slug, in)
	if err != nil {
		t.Fatal(err)
	}

	// Someone applying to another role with the same address must not be
	// able to rename the person or drop the links already on file.
	impostor := applyInput("ADA@example.com", docxBytes(t, "Ada Lovelace"))
	impostor.Name = "Not Ada"
	impostor.Links = []string{"https://example.com/second"}
	again, err := f.candidates.Apply(f.ctx, f.orgSlug(), second.Slug, impostor)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != cand.ID {
		t.Fatalf("second apply created a new candidate %s, want %s", again.ID, cand.ID)
	}
	if again.Name != "Ada Lovelace" {
		t.Errorf("name is now %q; the public form must not rename an existing candidate", again.Name)
	}
	want := map[string]bool{"https://example.com/ada": false, "https://example.com/second": false}
	for _, l := range again.Links {
		if _, ok := want[l]; ok {
			want[l] = true
		}
	}
	for link, seen := range want {
		if !seen {
			t.Errorf("links %v lost %q", again.Links, link)
		}
	}

	// A recruiter editing the same person still may correct the name.
	fixed, err := f.candidates.Add(f.ctx, f.recruiter(), service.NewCandidate{Name: "Ada King", Email: "ada@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if fixed.Name != "Ada King" {
		t.Errorf("recruiter rename produced %q, want %q", fixed.Name, "Ada King")
	}
}

func TestAResumeIsNotReadableThroughAnotherCandidatesURL(t *testing.T) {
	f := newCandidateFixture(t)
	job := f.newJob(t)
	owner, err := f.candidates.Apply(f.ctx, f.orgSlug(), job.Slug, applyInput("ada@example.com", docxBytes(t, "Ada Lovelace")))
	if err != nil {
		t.Fatal(err)
	}
	detail, err := f.candidates.Detail(f.ctx, f.recruiter(), owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.candidates.Add(f.ctx, f.recruiter(), service.NewCandidate{Name: "Alan Turing", Email: "alan@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.candidates.ResumeURL(f.ctx, f.recruiter(), other.ID, detail.Resumes[0].ID); !errors.Is(err, service.ErrNotFound) {
		t.Errorf("resume read through another candidate = %v, want ErrNotFound", err)
	}
	if !f.blob.has("") && f.blob.count() != 1 {
		t.Errorf("storage holds %d objects, want 1", f.blob.count())
	}
}
