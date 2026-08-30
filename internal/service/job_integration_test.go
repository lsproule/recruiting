//go:build integration

package service_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"recruiting/internal/domain"
	"recruiting/internal/service"
)

// jobFixture is a seeded org with the default pipeline template and one client
// company, plus the job service under test.
type jobFixture struct {
	*fixture
	sys       *pgxpool.Pool
	jobs      *service.JobService
	companyID uuid.UUID
}

func newJobFixture(t *testing.T) *jobFixture {
	t.Helper()
	f := newFixture(t)
	ownerURL := os.Getenv("DATABASE_URL")
	sys, err := pgxpool.New(f.ctx, ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sys.Close)

	jf := &jobFixture{fixture: f, sys: sys, jobs: service.NewJobService(f.st), companyID: uuid.New()}
	_, err = sys.Exec(f.ctx, `insert into client_company (id, org_id, name) values ($1, $2, 'Globex')`, jf.companyID, f.orgID)
	if err != nil {
		t.Fatal(err)
	}

	// The default template is what BootstrapOrg seeds for a real org.
	templateID := uuid.New()
	_, err = sys.Exec(f.ctx, `insert into pipeline_template (id, org_id, name, is_default) values ($1, $2, 'Default', true)`, templateID, f.orgID)
	if err != nil {
		t.Fatal(err)
	}
	for i, s := range service.DefaultPipelineStages {
		_, err := sys.Exec(f.ctx, `insert into pipeline_template_stage (org_id, template_id, position, name, kind, unblind)
			values ($1, $2, $3, $4, $5, $6)`, f.orgID, templateID, i+1, s.Name, s.Kind, s.Unblind)
		if err != nil {
			t.Fatal(err)
		}
	}
	return jf
}

func (f *jobFixture) recruiter() service.Principal {
	return service.Principal{Kind: service.PrincipalOrgUser, OrgID: f.orgID, UserID: f.userID, Roles: []string{service.RoleRecruiter}}
}

func (f *jobFixture) newJob(t *testing.T) service.Job {
	t.Helper()
	job, err := f.jobs.CreateJob(f.ctx, f.recruiter(), service.NewJob{
		ClientCompanyID: f.companyID, Title: "Senior Go Engineer",
		Description: "## About\nWe build things.", Skills: []string{"go", "postgres"},
		Seniority: service.SenioritySenior, Location: "Berlin",
		RemotePolicy: service.RemoteHybrid, SalaryMin: 80000, SalaryMax: 110000,
		Status: service.JobOpen,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	return job
}

func TestCreateJobCopiesTheDefaultTemplateIntoStages(t *testing.T) {
	f := newJobFixture(t)
	job := f.newJob(t)
	if job.Slug != "senior-go-engineer" {
		t.Errorf("slug = %q", job.Slug)
	}

	stages, err := f.jobs.Stages(f.ctx, f.recruiter(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stages) != len(service.DefaultPipelineStages) {
		t.Fatalf("copied %d stages, want %d", len(stages), len(service.DefaultPipelineStages))
	}
	for i, want := range service.DefaultPipelineStages {
		got := stages[i]
		if got.Name != want.Name || string(got.Kind) != want.Kind || got.Unblind != want.Unblind {
			t.Errorf("stage %d = %+v, want %+v", i, got, want)
		}
		if got.Position != i+1 {
			t.Errorf("stage %d position = %d", i, got.Position)
		}
	}
	if err := domain.ValidatePipeline(stages); err != nil {
		t.Errorf("copied pipeline is not valid: %v", err)
	}
	// Applications must never point at template rows, so the copies are new.
	var templateIDs int
	err = f.sys.QueryRow(context.Background(),
		`select count(*) from pipeline_template_stage s join stage j on j.id = s.id where s.org_id = $1`, f.orgID).Scan(&templateIDs)
	if err != nil || templateIDs != 0 {
		t.Errorf("stages share ids with template rows: %d (%v)", templateIDs, err)
	}
}

func TestPipelineKeepsExactlyOneTerminalPerOutcome(t *testing.T) {
	f := newJobFixture(t)
	job := f.newJob(t)
	p := f.recruiter()

	_, err := f.jobs.AddStage(f.ctx, p, job.ID, service.StageInput{
		Name: "Hired too", Kind: domain.StageTerminal, Terminal: domain.StatusHired,
	})
	if !errors.Is(err, domain.ErrInvalidPipeline) {
		t.Fatalf("second hired stage: got %v, want ErrInvalidPipeline", err)
	}

	stages, err := f.jobs.Stages(f.ctx, p, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	var hired domain.Stage
	for _, s := range stages {
		if s.Terminal == domain.StatusHired {
			hired = s
		}
	}
	if hired.ID == uuid.Nil {
		t.Fatal("no hired stage")
	}
	// Removing the only hired stage leaves a pipeline nothing can close.
	if err := f.jobs.DeleteStage(f.ctx, p, job.ID, hired.ID); !errors.Is(err, domain.ErrInvalidPipeline) {
		t.Errorf("deleting the only hired stage: got %v, want ErrInvalidPipeline", err)
	}
	// Renaming and re-kinding a stage is fine as long as the shape holds.
	if _, err := f.jobs.UpdateStage(f.ctx, p, job.ID, stages[0].ID, service.StageInput{
		Name: "Inbox", Kind: domain.StageInterview, Unblind: true,
	}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	stages, _ = f.jobs.Stages(f.ctx, p, job.ID)
	if stages[0].Name != "Inbox" || stages[0].Kind != domain.StageInterview || !stages[0].Unblind {
		t.Errorf("rename did not persist: %+v", stages[0])
	}
}

func TestAddStageLandsBeforeTheTerminalsAndNeedsARealJob(t *testing.T) {
	f := newJobFixture(t)
	job := f.newJob(t)
	p := f.recruiter()

	added, err := f.jobs.AddStage(f.ctx, p, job.ID, service.StageInput{Name: "Take-home", Kind: domain.StageAssessment})
	if err != nil {
		t.Fatalf("add stage: %v", err)
	}
	stages, err := f.jobs.Stages(f.ctx, p, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stages[added.Position-1].ID != added.ID {
		t.Fatalf("reported position %d does not hold the new stage", added.Position)
	}
	for _, s := range stages[added.Position:] {
		if s.Kind != domain.StageTerminal {
			t.Errorf("%q sits after the added stage but is not terminal", s.Name)
		}
	}

	if _, err := f.jobs.AddStage(f.ctx, p, uuid.New(), service.StageInput{Name: "Ghost", Kind: domain.StageGeneric}); !errors.Is(err, service.ErrNotFound) {
		t.Errorf("add stage to an unknown job: got %v, want ErrNotFound", err)
	}
	if err := f.jobs.ReorderStages(f.ctx, p, uuid.New(), nil); !errors.Is(err, service.ErrNotFound) {
		t.Errorf("reorder an unknown job: got %v, want ErrNotFound", err)
	}
}

func TestDeletingAStageHoldingApplicationsIsRefused(t *testing.T) {
	f := newJobFixture(t)
	job := f.newJob(t)
	p := f.recruiter()
	stages, err := f.jobs.Stages(f.ctx, p, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	first := stages[0]

	candidateID := uuid.New()
	_, err = f.sys.Exec(f.ctx, `insert into candidate (id, org_id, email, name) values ($1, $2, $3, 'Ada')`,
		candidateID, f.orgID, candidateID.String()+"@example.com")
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.sys.Exec(f.ctx, `insert into application (org_id, job_id, candidate_id, client_company_id, stage_id)
		values ($1, $2, $3, $4, $5)`, f.orgID, job.ID, candidateID, f.companyID, first.ID)
	if err != nil {
		t.Fatal(err)
	}

	if err := f.jobs.DeleteStage(f.ctx, p, job.ID, first.ID); !errors.Is(err, service.ErrStageOccupied) {
		t.Fatalf("delete occupied stage: got %v, want ErrStageOccupied", err)
	}
	// An empty stage still deletes.
	if err := f.jobs.DeleteStage(f.ctx, p, job.ID, stages[1].ID); err != nil {
		t.Fatalf("delete empty stage: %v", err)
	}
}

func TestReorderStagesPersists(t *testing.T) {
	f := newJobFixture(t)
	job := f.newJob(t)
	p := f.recruiter()
	stages, err := f.jobs.Stages(f.ctx, p, job.ID)
	if err != nil {
		t.Fatal(err)
	}

	order := make([]uuid.UUID, len(stages))
	for i, s := range stages {
		order[i] = s.ID
	}
	order[0], order[1] = order[1], order[0]
	if err := f.jobs.ReorderStages(f.ctx, p, job.ID, order); err != nil {
		t.Fatalf("reorder: %v", err)
	}
	after, err := f.jobs.Stages(f.ctx, p, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range order {
		if after[i].ID != id {
			t.Fatalf("position %d = %s, want %s", i, after[i].ID, id)
		}
		if after[i].Position != i+1 {
			t.Errorf("stage %d position = %d", i, after[i].Position)
		}
	}
	// A partial order would silently drop stages, so it is refused.
	if err := f.jobs.ReorderStages(f.ctx, p, job.ID, order[:2]); err == nil {
		t.Error("partial order accepted")
	}
}

func TestJobEditingRequiresRecruiterOrAdmin(t *testing.T) {
	f := newJobFixture(t)
	vetter := service.Principal{Kind: service.PrincipalOrgUser, OrgID: f.orgID, UserID: f.userID, Roles: []string{service.RoleVetter}}
	_, err := f.jobs.CreateJob(f.ctx, vetter, service.NewJob{ClientCompanyID: f.companyID, Title: "Nope"})
	if !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("vetter create: got %v, want ErrForbidden", err)
	}
	job := f.newJob(t)
	if _, err := f.jobs.UpdateStage(f.ctx, vetter, job.ID, uuid.New(), service.StageInput{Name: "X", Kind: domain.StageGeneric}); !errors.Is(err, service.ErrForbidden) {
		t.Errorf("vetter stage edit: got %v, want ErrForbidden", err)
	}
	// A vetter may still read the pipeline they work in.
	if _, err := f.jobs.Stages(f.ctx, vetter, job.ID); err != nil {
		t.Errorf("vetter read: %v", err)
	}
}

func TestJobUpdateAndSlugCollision(t *testing.T) {
	f := newJobFixture(t)
	p := f.recruiter()
	job := f.newJob(t)

	updated, err := f.jobs.UpdateJob(f.ctx, p, job.ID, service.NewJob{
		ClientCompanyID: f.companyID, Title: "Staff Go Engineer", Slug: job.Slug,
		Skills: []string{"go"}, Seniority: service.SeniorityStaff, RemotePolicy: service.RemoteRemote,
		BlindMode: true, Status: service.JobClosed,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Title != "Staff Go Engineer" || !updated.BlindMode || updated.Status != service.JobClosed || updated.Seniority != service.SeniorityStaff {
		t.Errorf("update did not persist: %+v", updated)
	}

	if _, err := f.jobs.CreateJob(f.ctx, p, service.NewJob{ClientCompanyID: f.companyID, Title: "x", Slug: job.Slug}); !errors.Is(err, service.ErrSlugTaken) {
		t.Errorf("duplicate slug: got %v, want ErrSlugTaken", err)
	}
	if _, err := f.jobs.CreateJob(f.ctx, p, service.NewJob{ClientCompanyID: f.companyID, Title: "  "}); !errors.Is(err, service.ErrTitleRequired) {
		t.Errorf("blank title: got %v, want ErrTitleRequired", err)
	}
	if _, err := f.jobs.CreateJob(f.ctx, p, service.NewJob{ClientCompanyID: f.companyID, Title: "Bad", Seniority: "wizard"}); !errors.Is(err, service.ErrInvalidJob) {
		t.Errorf("unknown seniority: got %v, want ErrInvalidJob", err)
	}

	jobs, err := f.jobs.ListJobs(f.ctx, p)
	if err != nil || len(jobs) != 1 || jobs[0].ClientCompanyName != "Globex" {
		t.Fatalf("list = %+v (%v)", jobs, err)
	}
}
