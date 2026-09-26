//go:build integration

package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"recruiting/internal/domain"
	"recruiting/internal/service"
)

type processFixture struct {
	*pipelineFixture
	procs     *service.ProcessService
	jobs      *service.JobService
	companyID uuid.UUID
}

func newProcessFixture(t *testing.T) *processFixture {
	t.Helper()
	pf := newPipelineFixture(t)
	f := &processFixture{pipelineFixture: pf, procs: service.NewProcessService(pf.st), jobs: service.NewJobService(pf.st)}
	if err := pf.sys.QueryRow(context.Background(), `select client_company_id from job where id = $1`, pf.jobID).Scan(&f.companyID); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *processFixture) recruiter() service.Principal { return f.principal(service.RoleRecruiter) }

func TestProcessCopiedFromTheLibraryCarriesItsSettingsIntoJobs(t *testing.T) {
	f := newProcessFixture(t)
	ctx := context.Background()
	loop, err := f.procs.Create(ctx, f.recruiter(), service.ProcessInput{Name: "Loop"}, service.ProcessSource{LibraryKey: domain.ProcessEngineeringLoop})
	if err != nil {
		t.Fatalf("create from library: %v", err)
	}
	if loop.LibraryKey != domain.ProcessEngineeringLoop || loop.Description == "" || len(loop.Stages) != 9 {
		t.Fatalf("process = %+v", loop)
	}
	if got := loop.Stages[3]; got.Kind != domain.StageSprint || got.RoundSeconds != 300 || got.BreakSeconds != 60 {
		t.Fatalf("sprint stage = %+v", got)
	}
	if got := loop.Stages[4]; got.InterviewFormat != domain.FormatVideo || got.DurationMinutes != 60 {
		t.Fatalf("technical interview = %+v", got)
	}

	// The fixture's org has no template rows at all, so the copy is the
	// only process and becomes the default a job is built from.
	if err := f.procs.SetDefault(ctx, f.recruiter(), loop.ID); err != nil {
		t.Fatalf("set default: %v", err)
	}
	job, err := f.jobs.CreateJob(ctx, f.recruiter(), service.NewJob{ClientCompanyID: f.companyID, Title: "Loop job", Status: service.JobOpen})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	stages, err := f.jobs.Stages(ctx, f.recruiter(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stages) != 9 {
		t.Fatalf("job has %d stages, want 9", len(stages))
	}
	if got := stages[3]; got.Kind != domain.StageSprint || got.RoundSeconds != 300 || got.BreakSeconds != 60 {
		t.Fatalf("copied sprint stage = %+v", got)
	}
	if got := stages[5]; got.Kind != domain.StageInterview || got.InterviewFormat != domain.FormatVideo || got.DurationMinutes != 45 {
		t.Fatalf("copied HR interview = %+v", got)
	}
	if got := stages[8]; got.Terminal != domain.StatusRejected {
		t.Fatalf("last stage = %+v", got)
	}
	var template uuid.NullUUID
	if err := f.sys.QueryRow(ctx, `select template_id from job where id = $1`, job.ID).Scan(&template); err != nil {
		t.Fatal(err)
	}
	if !template.Valid || template.UUID != loop.ID {
		t.Fatalf("job template = %v, want %s", template, loop.ID)
	}
	got, err := f.procs.Get(ctx, f.recruiter(), loop.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Jobs != 1 {
		t.Fatalf("process counts %d jobs, want 1", got.Jobs)
	}
}

func TestProcessStageEditorValidatesAndLeavesJobsAlone(t *testing.T) {
	f := newProcessFixture(t)
	ctx := context.Background()
	rec := f.recruiter()
	proc, err := f.procs.Create(ctx, rec, service.ProcessInput{Name: "Blank"}, service.ProcessSource{})
	if err != nil {
		t.Fatal(err)
	}
	if len(proc.Stages) != 3 {
		t.Fatalf("a blank process starts with %d stages, want 3", len(proc.Stages))
	}
	sprint, err := f.procs.AddStage(ctx, rec, proc.ID, service.StageInput{Name: "Speed round", Kind: domain.StageSprint, RoundSeconds: 240})
	if err != nil {
		t.Fatalf("add sprint stage: %v", err)
	}
	if sprint.Position != 2 || sprint.BreakSeconds != domain.DefaultBreakSeconds {
		t.Fatalf("added stage = %+v (should sit before the terminals with the default break)", sprint)
	}
	if _, err := f.procs.UpdateStage(ctx, rec, proc.ID, sprint.ID, service.StageInput{Name: "Speed round", Kind: domain.StageSprint, RoundSeconds: 5}); !errors.Is(err, domain.ErrInvalidPipeline) {
		t.Fatalf("a 5-second round was accepted: %v", err)
	}
	if _, err := f.procs.UpdateStage(ctx, rec, proc.ID, sprint.ID, service.StageInput{Name: "Tech", Kind: domain.StageInterview, InterviewFormat: "telepathy"}); !errors.Is(err, domain.ErrInvalidPipeline) {
		t.Fatalf("an unknown format was accepted: %v", err)
	}
	video, err := f.procs.UpdateStage(ctx, rec, proc.ID, sprint.ID, service.StageInput{Name: "Tech", Kind: domain.StageInterview, InterviewFormat: domain.FormatVideo, DurationMinutes: 90})
	if err != nil {
		t.Fatal(err)
	}
	if video.RoundSeconds != 0 || video.InterviewFormat != domain.FormatVideo || video.DurationMinutes != 90 {
		t.Fatalf("kind change kept stale settings: %+v", video)
	}
	// Removing a terminal breaks the pipeline and is refused.
	got, _ := f.procs.Get(ctx, rec, proc.ID)
	if err := f.procs.DeleteStage(ctx, rec, proc.ID, got.Stages[len(got.Stages)-1].ID); !errors.Is(err, domain.ErrInvalidPipeline) {
		t.Fatalf("deleting a terminal was allowed: %v", err)
	}
	// The default cannot be deleted; another can, and a vetter can do none of it.
	if err := f.procs.SetDefault(ctx, rec, proc.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.procs.Delete(ctx, rec, proc.ID); !errors.Is(err, service.ErrProcessDefault) {
		t.Fatalf("deleting the default: %v", err)
	}
	other, err := f.procs.Create(ctx, rec, service.ProcessInput{Name: "Other"}, service.ProcessSource{ProcessID: proc.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(other.Stages) != len(got.Stages) {
		t.Fatalf("copy has %d stages, want %d", len(other.Stages), len(got.Stages))
	}
	if _, err := f.procs.AddStage(ctx, f.principal(service.RoleVetter), other.ID, service.StageInput{Name: "X", Kind: domain.StageGeneric}); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("a vetter edited a process: %v", err)
	}
	if err := f.procs.Delete(ctx, rec, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.procs.Get(ctx, rec, other.ID); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("deleted process still reads: %v", err)
	}
	// Another org sees none of it.
	stranger := service.Principal{Kind: service.PrincipalOrgUser, OrgID: uuid.New(), UserID: uuid.New(), Roles: []string{service.RoleAdmin}}
	if _, err := f.procs.Get(ctx, stranger, proc.ID); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("another org read the process: %v", err)
	}
}
