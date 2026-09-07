//go:build integration

package service_test

import (
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"recruiting/internal/domain"
	"recruiting/internal/queue"
	"recruiting/internal/service"
)

// intakeFixture is a seeded org with the default pipeline template, a small
// problem bank, and the intake service under test.
type intakeFixture struct {
	*fixture
	sys      *pgxpool.Pool
	intake   *service.IntakeService
	jobs     *service.JobService
	assess   *service.AssessmentService
	template uuid.UUID
	easy     uuid.UUID
	medium   uuid.UUID
	hard     uuid.UUID
}

func newIntakeFixture(t *testing.T) *intakeFixture {
	t.Helper()
	f := newFixture(t)
	sys, err := pgxpool.New(f.ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sys.Close)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := sys.Exec(f.ctx, sql, args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	q, err := queue.New(f.st.Pool(), queue.Config{})
	if err != nil {
		t.Fatal(err)
	}
	fx := &intakeFixture{
		fixture: f, sys: sys,
		jobs:     service.NewJobService(f.st),
		assess:   service.NewAssessmentService(f.st),
		template: uuid.New(), easy: uuid.New(), medium: uuid.New(), hard: uuid.New(),
	}
	fx.intake = service.NewIntakeService(f.st, q, "https://cadre.example")

	exec(`insert into pipeline_template (id, org_id, name, is_default) values ($1, $2, 'Default', true)`, fx.template, f.orgID)
	for i, s := range service.DefaultPipelineStages {
		exec(`insert into pipeline_template_stage (org_id, template_id, position, name, kind, unblind)
		      values ($1, $2, $3, $4, $5, $6)`, f.orgID, fx.template, i+1, s.Name, s.Kind, s.Unblind)
	}
	for _, p := range []struct {
		id         uuid.UUID
		title      string
		difficulty string
		tags       string
	}{
		{fx.easy, "Warm up", "easy", `{go}`},
		{fx.medium, "Rate limiter", "medium", `{concurrency}`},
		{fx.hard, "Backpressure queue", "hard", `{concurrency}`},
	} {
		exec(`insert into problem (id, org_id, kind, title, statement, difficulty, tags, allowed_languages, quality, proven_languages)
		      values ($1, $2, 'code', $3, 'Solve it.', $4, $5, '{python}', 80, '{python}')`,
			p.id, f.orgID, p.title, p.difficulty, p.tags)
	}
	return fx
}

// queuedInvites counts the invite emails this org has waiting in the queue.
func (f *intakeFixture) queuedInvites(t *testing.T) int {
	t.Helper()
	var n int
	err := f.sys.QueryRow(f.ctx,
		`select count(*) from river_job where kind = $1 and args->>'payload' like '%' || $2 || '%'`,
		queue.KindEmailSend, f.orgID.String()).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// queuedInviteTo is the recipient of this org's one queued invite.
func (f *intakeFixture) queuedInviteTo(t *testing.T) string {
	t.Helper()
	var raw string
	err := f.sys.QueryRow(f.ctx,
		`select args->>'payload' from river_job where kind = $1 and args->>'payload' like '%' || $2 || '%'`,
		queue.KindEmailSend, f.orgID.String()).Scan(&raw)
	if err != nil {
		t.Fatalf("invite job: %v", err)
	}
	var email queue.EmailPayload
	if err := json.Unmarshal([]byte(raw), &email); err != nil {
		t.Fatal(err)
	}
	return email.To
}

func (f *intakeFixture) recruiter() service.Principal {
	return service.Principal{Kind: service.PrincipalOrgUser, OrgID: f.orgID, UserID: f.userID, Roles: []string{service.RoleRecruiter}}
}

// filled is a complete intake: every step answered with something valid.
func (f *intakeFixture) filled() service.IntakePayload {
	return service.IntakePayload{
		Client: service.IntakeClient{
			Company: "Northwind Systems", Industry: "Payments infrastructure",
			ContactName: "Priya Raman", ContactEmail: "priya@northwind.example",
			ShortlistSLADays: 5, Brief: "Fewer candidates, all of whom can reason about backpressure.",
		},
		Job: service.IntakeJob{
			Title: "Staff Backend Engineer", Seniority: service.SeniorityStaff,
			Location: "New York", RemotePolicy: service.RemoteHybrid,
			SalaryMin: 210000, SalaryMax: 255000, TemplateID: f.template,
		},
		Skills:     []string{"concurrency", "go"},
		Assessment: service.IntakeAssessment{Source: service.IntakeSourceDefault},
	}
}

// walk fills the whole draft step by step, as the wizard does.
func (f *intakeFixture) walk(t *testing.T, id uuid.UUID, in service.IntakePayload) service.IntakeDraft {
	t.Helper()
	var draft service.IntakeDraft
	for step := service.IntakeStepClient; step <= service.IntakeStepQuestion; step++ {
		var err error
		draft, err = f.intake.SaveStep(f.ctx, f.recruiter(), id, step, in, step+1)
		if err != nil {
			t.Fatalf("save step %d: %v", step, err)
		}
	}
	return draft
}

func TestIntakeDraftResumesAtTheSavedStep(t *testing.T) {
	f := newIntakeFixture(t)
	draft, err := f.intake.Open(f.ctx, f.recruiter())
	if err != nil {
		t.Fatal(err)
	}
	if draft.Step != service.IntakeStepClient {
		t.Errorf("a new draft opens at step %d, want %d", draft.Step, service.IntakeStepClient)
	}

	in := f.filled()
	if _, err := f.intake.SaveStep(f.ctx, f.recruiter(), draft.ID, service.IntakeStepClient, in, service.IntakeStepJob); err != nil {
		t.Fatal(err)
	}
	// Opening again is resuming: the same draft, at the step it was left on,
	// with what was typed still there.
	again, err := f.intake.Open(f.ctx, f.recruiter())
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != draft.ID {
		t.Errorf("opened a second draft %s, want %s", again.ID, draft.ID)
	}
	if again.Step != service.IntakeStepJob {
		t.Errorf("resumed at step %d, want %d", again.Step, service.IntakeStepJob)
	}
	if again.Payload.Client.Company != in.Client.Company || again.Payload.Client.Brief != in.Client.Brief {
		t.Errorf("client step did not survive: %+v", again.Payload.Client)
	}
}

func TestIntakeStepValidationNamesWhatIsMissing(t *testing.T) {
	f := newIntakeFixture(t)
	draft, err := f.intake.Open(f.ctx, f.recruiter())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		step int
		in   func(service.IntakePayload) service.IntakePayload
	}{
		{"no company", service.IntakeStepClient, func(p service.IntakePayload) service.IntakePayload {
			p.Client.Company = ""
			return p
		}},
		{"no contact email", service.IntakeStepClient, func(p service.IntakePayload) service.IntakePayload {
			p.Client.ContactEmail = "not-an-address"
			return p
		}},
		{"no job title", service.IntakeStepJob, func(p service.IntakePayload) service.IntakePayload {
			p.Job.Title = ""
			return p
		}},
		{"no skills", service.IntakeStepSkills, func(p service.IntakePayload) service.IntakePayload {
			p.Skills = nil
			return p
		}},
		{"unknown question source", service.IntakeStepQuestion, func(p service.IntakePayload) service.IntakePayload {
			p.Assessment.Source = "telepathy"
			return p
		}},
	} {
		_, err := f.intake.SaveStep(f.ctx, f.recruiter(), draft.ID, tc.step, tc.in(f.filled()), tc.step+1)
		if !errors.Is(err, service.ErrIntakeInvalid) {
			t.Errorf("%s: err = %v, want ErrIntakeInvalid", tc.name, err)
		}
	}
}

func TestIntakeStepBackDoesNotValidate(t *testing.T) {
	f := newIntakeFixture(t)
	draft, err := f.intake.Open(f.ctx, f.recruiter())
	if err != nil {
		t.Fatal(err)
	}
	in := f.filled()
	in.Job.Title = ""
	// Going back from an unfinished step must keep what is there, not refuse
	// to move.
	back, err := f.intake.SaveStep(f.ctx, f.recruiter(), draft.ID, service.IntakeStepJob, in, service.IntakeStepClient)
	if err != nil {
		t.Fatalf("moving back: %v", err)
	}
	if back.Step != service.IntakeStepClient {
		t.Errorf("step = %d, want %d", back.Step, service.IntakeStepClient)
	}
}

func TestIntakeDefaultSetFollowsSeniority(t *testing.T) {
	f := newIntakeFixture(t)
	draft, err := f.intake.Open(f.ctx, f.recruiter())
	if err != nil {
		t.Fatal(err)
	}
	f.walk(t, draft.ID, f.filled())

	slots, err := f.intake.Set(f.ctx, f.recruiter(), draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 2 {
		t.Fatalf("staff set has %d slots, want 2", len(slots))
	}
	if slots[0].Chosen.ID != f.medium || slots[1].Chosen.ID != f.hard {
		t.Errorf("staff set = %s/%s, want the medium and hard problems", slots[0].Chosen.Title, slots[1].Chosen.Title)
	}

	junior := f.filled()
	junior.Job.Seniority = service.SeniorityJunior
	if _, err := f.intake.SaveStep(f.ctx, f.recruiter(), draft.ID, service.IntakeStepJob, junior, service.IntakeStepSkills); err != nil {
		t.Fatal(err)
	}
	slots, err = f.intake.Set(f.ctx, f.recruiter(), draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 2 || slots[0].Chosen.ID != f.easy || slots[1].Chosen.ID != f.medium {
		t.Errorf("junior set = %v, want the easy and medium problems", slots)
	}
}

func TestIntakeCreateBuildsTheWholeClient(t *testing.T) {
	f := newIntakeFixture(t)
	draft, err := f.intake.Open(f.ctx, f.recruiter())
	if err != nil {
		t.Fatal(err)
	}
	in := f.filled()
	f.walk(t, draft.ID, in)

	got, err := f.intake.Create(f.ctx, f.recruiter(), draft.ID)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// The client company carries what the intake asked for.
	var name, industry, brief string
	var sla *int32
	err = f.sys.QueryRow(f.ctx, `select name, industry, brief, shortlist_sla_days from client_company where id = $1`,
		got.ClientCompanyID).Scan(&name, &industry, &brief, &sla)
	if err != nil {
		t.Fatal(err)
	}
	if name != in.Client.Company || industry != in.Client.Industry || brief != in.Client.Brief {
		t.Errorf("company = %q/%q/%q", name, industry, brief)
	}
	if sla == nil || int(*sla) != in.Client.ShortlistSLADays {
		t.Errorf("shortlist SLA = %v, want %d", sla, in.Client.ShortlistSLADays)
	}

	// The client user belongs to it and has an invite on the way.
	var userCompany uuid.UUID
	var email string
	if err := f.sys.QueryRow(f.ctx, `select client_company_id, email from client_user where id = $1`, got.ClientUserID).Scan(&userCompany, &email); err != nil {
		t.Fatal(err)
	}
	if userCompany != got.ClientCompanyID || email != in.Client.ContactEmail {
		t.Errorf("client user = %s/%s", userCompany, email)
	}
	if to := f.queuedInviteTo(t); to != in.Client.ContactEmail {
		t.Errorf("invite queued to %q, want the client contact", to)
	}
	if got.InviteLink == "" {
		t.Error("no invite link")
	}

	// The job runs the template's pipeline, and the assessment hangs off its
	// assessment stage.
	job, err := f.jobs.Job(f.ctx, f.recruiter(), got.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Title != in.Job.Title || job.ClientCompanyID != got.ClientCompanyID {
		t.Errorf("job = %+v", job)
	}
	stages, err := f.jobs.Stages(f.ctx, f.recruiter(), got.JobID)
	if err != nil {
		t.Fatal(err)
	}
	var assessmentStage uuid.UUID
	for _, st := range stages {
		if st.Kind == domain.StageAssessment {
			assessmentStage = st.ID
			break
		}
	}
	if assessmentStage == uuid.Nil {
		t.Fatal("the job has no assessment stage")
	}
	attached, err := f.assess.StageAssessment(f.ctx, f.recruiter(), assessmentStage)
	if err != nil {
		t.Fatal(err)
	}
	if attached == nil || attached.ID != got.AssessmentID {
		t.Fatalf("stage assessment = %v, want %s", attached, got.AssessmentID)
	}
	if len(attached.Problems) != 2 {
		t.Errorf("assessment carries %d problems, want the two of the default set", len(attached.Problems))
	}

	// The draft is gone, so the next intake starts clean.
	if _, err := f.intake.Draft(f.ctx, f.recruiter(), draft.ID); !errors.Is(err, service.ErrNotFound) {
		t.Errorf("draft after create: %v, want ErrNotFound", err)
	}
}

func TestIntakeCreateRollsBackWhenTheAssessmentIsRefused(t *testing.T) {
	f := newIntakeFixture(t)
	// A problem below the quality floor cannot be attached, so the whole
	// intake must leave nothing behind.
	rejected := uuid.New()
	if _, err := f.sys.Exec(f.ctx, `insert into problem (id, org_id, kind, title, statement, difficulty, tags, allowed_languages, quality)
	      values ($1, $2, 'code', 'Half-written', 'Solve it.', 'medium', '{concurrency}', '{python}', 10)`, rejected, f.orgID); err != nil {
		t.Fatal(err)
	}
	draft, err := f.intake.Open(f.ctx, f.recruiter())
	if err != nil {
		t.Fatal(err)
	}
	in := f.filled()
	in.Assessment = service.IntakeAssessment{Source: service.IntakeSourceBank, ProblemIDs: []uuid.UUID{rejected}}
	f.walk(t, draft.ID, in)

	if _, err := f.intake.Create(f.ctx, f.recruiter(), draft.ID); !errors.Is(err, service.ErrProblemQuality) {
		t.Fatalf("create: %v, want ErrProblemQuality", err)
	}
	for _, table := range []string{"client_company", "client_user", "job", "assessment"} {
		var n int
		if err := f.sys.QueryRow(f.ctx, `select count(*) from `+table+` where org_id = $1`, f.orgID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s holds %d rows after a refused intake, want none", table, n)
		}
	}
	if n := f.queuedInvites(t); n != 0 {
		t.Errorf("queued %d invites for a refused intake, want none", n)
	}
	// The draft survives so the recruiter can fix the set and try again.
	if _, err := f.intake.Draft(f.ctx, f.recruiter(), draft.ID); err != nil {
		t.Errorf("draft after a refused create: %v", err)
	}
}

func TestIntakeRefusesATemplateWithoutAnAssessmentStage(t *testing.T) {
	f := newIntakeFixture(t)
	lean := uuid.New()
	if _, err := f.sys.Exec(f.ctx, `insert into pipeline_template (id, org_id, name, is_default) values ($1, $2, 'Lean', false)`, lean, f.orgID); err != nil {
		t.Fatal(err)
	}
	for i, s := range []struct{ name, kind string }{{"Applied", "generic"}, {"Hired", "terminal"}, {"Rejected", "terminal"}} {
		if _, err := f.sys.Exec(f.ctx, `insert into pipeline_template_stage (org_id, template_id, position, name, kind)
		      values ($1, $2, $3, $4, $5)`, f.orgID, lean, i+1, s.name, s.kind); err != nil {
			t.Fatal(err)
		}
	}
	templates, err := f.intake.Templates(f.ctx, f.recruiter())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tm := range templates {
		if tm.ID == lean {
			found = true
			if tm.HasAssessment {
				t.Error("the lean template is reported as carrying an assessment stage")
			}
		}
	}
	if !found {
		t.Fatalf("templates = %+v, want the lean one listed", templates)
	}

	draft, err := f.intake.Open(f.ctx, f.recruiter())
	if err != nil {
		t.Fatal(err)
	}
	in := f.filled()
	in.Job.TemplateID = lean
	// The refusal lands on the job step, before there is anything to create.
	_, err = f.intake.SaveStep(f.ctx, f.recruiter(), draft.ID, service.IntakeStepJob, in, service.IntakeStepSkills)
	if !errors.Is(err, service.ErrTemplateNoAssessment) {
		t.Fatalf("job step: %v, want ErrTemplateNoAssessment", err)
	}
}

func TestIntakeDraftIsPrivateToItsOrg(t *testing.T) {
	f := newIntakeFixture(t)
	draft, err := f.intake.Open(f.ctx, f.recruiter())
	if err != nil {
		t.Fatal(err)
	}
	other := service.Principal{Kind: service.PrincipalOrgUser, OrgID: uuid.New(), UserID: uuid.New(), Roles: []string{service.RoleRecruiter}}
	if _, err := f.intake.Draft(f.ctx, other, draft.ID); !errors.Is(err, service.ErrNotFound) {
		t.Errorf("another org read the draft: %v", err)
	}
}

// payloadJSON proves the draft round-trips through the jsonb column rather
// than through a value the service happened to keep in memory.
func TestIntakeDraftIsStoredAsJSON(t *testing.T) {
	f := newIntakeFixture(t)
	draft, err := f.intake.Open(f.ctx, f.recruiter())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.intake.SaveStep(f.ctx, f.recruiter(), draft.ID, service.IntakeStepClient, f.filled(), service.IntakeStepJob); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := f.sys.QueryRow(f.ctx, `select payload from intake_draft where id = $1`, draft.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var stored service.IntakePayload
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("stored payload is not the intake's own shape: %v", err)
	}
	if stored.Client.Company != "Northwind Systems" {
		t.Errorf("stored company = %q", stored.Client.Company)
	}
}
