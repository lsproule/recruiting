//go:build integration

package service_test

import (
	"context"
	"math"
	"testing"

	"github.com/google/uuid"

	"recruiting/internal/domain"
	"recruiting/internal/service"
)

// clientAccountFixture is one client company running two jobs through the
// same pipeline: enough for the board to have something to merge and for the
// desk numbers to be more than a single row.
type clientAccountFixture struct {
	*pipelineFixture
	clients   *service.ClientService
	exec      func(sql string, args ...any)
	companyID uuid.UUID
	// secondJob runs the same stage names as the fixture's own job.
	secondJob uuid.UUID
	// stages of secondJob by the name they share with the first job's
	stages2 map[domain.StageKind]uuid.UUID
	// applications: one on each job's first stage, one awaiting the client
	firstStageApp, secondJobApp, awaitingApp uuid.UUID
}

func newClientAccountFixture(t *testing.T) *clientAccountFixture {
	t.Helper()
	pf := newPipelineFixture(t)
	ctx := context.Background()
	f := &clientAccountFixture{
		pipelineFixture: pf,
		clients:         service.NewClientService(pf.st, pf.apps),
		stages2:         map[domain.StageKind]uuid.UUID{},
		firstStageApp:   pf.appID,
	}
	f.exec = func(sql string, args ...any) {
		t.Helper()
		if _, err := pf.sys.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	if err := pf.sys.QueryRow(ctx, `select client_company_id from job where id = $1`, pf.jobID).Scan(&f.companyID); err != nil {
		t.Fatal(err)
	}
	f.exec(`update client_company set industry = 'Fintech', shortlist_sla_days = 5 where id = $1`, f.companyID)
	f.exec(`update job set created_by = $2, created_at = now() - interval '40 days' where id = $1`, pf.jobID, pf.userID)

	f.secondJob = uuid.New()
	f.exec(`insert into job (id, org_id, client_company_id, title, slug, status, created_by, created_at)
		values ($1, $2, $3, 'Rust Engineer', $4, 'open', $5, now() - interval '30 days')`,
		f.secondJob, pf.orgID, f.companyID, "rust-"+pf.orgID.String(), pf.userID)
	for i, k := range []domain.StageKind{domain.StageGeneric, domain.StageInterview, domain.StageAssessment, domain.StageClientReview} {
		id := uuid.New()
		f.stages2[k] = id
		f.exec(`insert into stage (id, org_id, job_id, position, name, kind) values ($1, $2, $3, $4, $5, $5)`, id, pf.orgID, f.secondJob, i+1, string(k))
	}

	f.secondJobApp = f.application(t, f.secondJob, f.stages2[domain.StageGeneric], "Bo Second", domain.StatusActive)
	f.awaitingApp = f.application(t, f.secondJob, f.stages2[domain.StageClientReview], "Cy Waiting", domain.StatusActive)
	return f
}

func (f *clientAccountFixture) application(t *testing.T, jobID, stageID uuid.UUID, name string, status domain.ApplicationStatus) uuid.UUID {
	t.Helper()
	candID, appID := uuid.New(), uuid.New()
	f.exec(`insert into candidate (id, org_id, email, name) values ($1, $2, $3, $4)`, candID, f.orgID, candID.String()+"@example.com", name)
	f.exec(`insert into application (id, org_id, job_id, candidate_id, client_company_id, stage_id, status) values ($1, $2, $3, $4, $5, $6, $7)`,
		appID, f.orgID, jobID, candID, f.companyID, stageID, string(status))
	return appID
}

// sentPacket puts a shortlist out for a job the given number of days after
// the job was opened, which is what the median reads.
func (f *clientAccountFixture) sentPacket(t *testing.T, jobID uuid.UUID, daysAfterOpening int) {
	t.Helper()
	id := uuid.New()
	f.exec(`insert into shortlist_packet (id, org_id, job_id, status, created_by, sent_at, sent_by)
		select $1, $2, $3, 'sent', $4, j.created_at + make_interval(days => $5), $4 from job j where j.id = $3`,
		id, f.orgID, jobID, f.userID, daysAfterOpening)
}

func (f *clientAccountFixture) account(t *testing.T) service.ClientAccount {
	t.Helper()
	accounts, err := f.clients.List(context.Background(), f.principal(service.RoleRecruiter))
	if err != nil {
		t.Fatalf("list clients: %v", err)
	}
	for _, a := range accounts {
		if a.ID == f.companyID {
			return a
		}
	}
	t.Fatalf("the fixture's client is missing from %d accounts", len(accounts))
	return service.ClientAccount{}
}

func TestClientListReportsTheDeskNumbers(t *testing.T) {
	f := newClientAccountFixture(t)
	got := f.account(t)
	if got.Name != "Globex" || got.Industry != "Fintech" {
		t.Fatalf("account = %+v, want the fixture's company", got)
	}
	if got.OpenJobs != 2 {
		t.Fatalf("open jobs = %d, want 2", got.OpenJobs)
	}
	if got.InPipeline != 3 {
		t.Fatalf("in pipeline = %d, want 3", got.InPipeline)
	}
	if got.AwaitingClient != 1 {
		t.Fatalf("awaiting client = %d, want 1", got.AwaitingClient)
	}
	if got.OwnerName == "" {
		t.Fatal("the account names no owner")
	}
	if got.ShortlistSLADays != 5 {
		t.Fatalf("SLA = %d days, want 5", got.ShortlistSLADays)
	}
}

func TestClientPlacedCountsOnlyTheLastNinetyDays(t *testing.T) {
	f := newClientAccountFixture(t)
	if got := f.account(t).Placed90d; got != 0 {
		t.Fatalf("placed 90d = %d before anyone was hired, want 0", got)
	}
	recent := f.application(t, f.jobID, f.hired, "Di Placed", domain.StatusHired)
	f.exec(`insert into application_event (org_id, application_id, actor_kind, actor_id, kind, to_stage_id, created_at)
		values ($1, $2, 'org_user', $3, 'moved', $4, now() - interval '10 days')`, f.orgID, recent, f.userID, f.hired)
	old := f.application(t, f.jobID, f.hired, "Ed Longago", domain.StatusHired)
	f.exec(`insert into application_event (org_id, application_id, actor_kind, actor_id, kind, to_stage_id, created_at)
		values ($1, $2, 'org_user', $3, 'moved', $4, now() - interval '200 days')`, f.orgID, old, f.userID, f.hired)

	if got := f.account(t).Placed90d; got != 1 {
		t.Fatalf("placed 90d = %d, want only the recent hire", got)
	}
}

func TestClientMedianDaysToShortlistUsesTheFirstPacketPerJob(t *testing.T) {
	f := newClientAccountFixture(t)
	if got := f.account(t).MedianDaysToShortlist; got != nil {
		t.Fatalf("median = %v before any packet was sent, want none", *got)
	}
	// The first job waited four days, and a later packet does not re-date it.
	f.sentPacket(t, f.jobID, 4)
	f.sentPacket(t, f.jobID, 20)
	f.sentPacket(t, f.secondJob, 10)

	got := f.account(t).MedianDaysToShortlist
	if got == nil {
		t.Fatal("median = none, want the middle of 4 and 10 days")
	}
	if math.Abs(*got-7) > 0.01 {
		t.Fatalf("median = %v days, want 7", *got)
	}
}

func TestClientDetailCarriesJobsAndPackets(t *testing.T) {
	f := newClientAccountFixture(t)
	f.sentPacket(t, f.secondJob, 3)
	detail, err := f.clients.Detail(context.Background(), f.principal(service.RoleRecruiter), f.companyID)
	if err != nil {
		t.Fatalf("client detail: %v", err)
	}
	if len(detail.Jobs) != 2 {
		t.Fatalf("detail lists %d jobs, want 2", len(detail.Jobs))
	}
	for _, job := range detail.Jobs {
		if job.ID == f.secondJob && job.Applicants != 2 {
			t.Fatalf("the second job reports %d applicants, want 2", job.Applicants)
		}
	}
	if len(detail.Packets) != 1 || detail.Packets[0].Status != service.ShortlistSent {
		t.Fatalf("detail packets = %+v, want the one sent packet", detail.Packets)
	}
}

func TestClientDetailRefusesAnUnknownCompany(t *testing.T) {
	f := newClientAccountFixture(t)
	if _, err := f.clients.Detail(context.Background(), f.principal(service.RoleRecruiter), uuid.New()); err == nil {
		t.Fatal("a company that does not exist has a detail screen")
	}
}

func TestClientBoardMergesTheJobsByStage(t *testing.T) {
	f := newClientAccountFixture(t)
	board, err := f.clients.Board(context.Background(), f.principal(service.RoleRecruiter), f.companyID, uuid.Nil)
	if err != nil {
		t.Fatalf("client board: %v", err)
	}
	if len(board.Jobs) != 2 {
		t.Fatalf("the board offers %d jobs to scope to, want 2", len(board.Jobs))
	}
	cards := map[string][]uuid.UUID{}
	for _, col := range board.Columns {
		for _, card := range col.Cards {
			cards[col.Stage.Name] = append(cards[col.Stage.Name], card.ID)
		}
	}
	// Both jobs' first stage is named "generic": one merged column, both cards.
	if got := cards[string(domain.StageGeneric)]; len(got) != 2 {
		t.Fatalf("the merged first column holds %v, want both jobs' cards", got)
	}
	if got := cards[string(domain.StageClientReview)]; len(got) != 1 || got[0] != f.awaitingApp {
		t.Fatalf("the client-review column holds %v, want the waiting application", got)
	}
	seen := map[string]bool{}
	for _, col := range board.Columns {
		if seen[col.Stage.Name] {
			t.Fatalf("stage %q has two columns; the jobs did not merge", col.Stage.Name)
		}
		seen[col.Stage.Name] = true
	}
	if board.Columns[0].Stage.Name != string(domain.StageGeneric) {
		t.Fatalf("the merged board starts at %q, want the first stage", board.Columns[0].Stage.Name)
	}
}

func TestClientBoardNarrowsToOneJob(t *testing.T) {
	f := newClientAccountFixture(t)
	board, err := f.clients.Board(context.Background(), f.principal(service.RoleRecruiter), f.companyID, f.secondJob)
	if err != nil {
		t.Fatalf("client board: %v", err)
	}
	for _, col := range board.Columns {
		for _, card := range col.Cards {
			if card.ID == f.firstStageApp {
				t.Fatal("scoping to one job still shows the other job's cards")
			}
		}
	}
	if board.JobID != f.secondJob {
		t.Fatalf("board scope = %v, want the second job", board.JobID)
	}
}

func TestClientScreensRefuseClientUsers(t *testing.T) {
	f := newClientAccountFixture(t)
	client := service.Principal{Kind: service.PrincipalClientUser, OrgID: f.orgID, UserID: uuid.New(), ClientCompanyID: f.companyID}
	if _, err := f.clients.List(context.Background(), client); err == nil {
		t.Fatal("a client user read the recruiter's book of accounts")
	}
	if _, err := f.clients.Detail(context.Background(), client, f.companyID); err == nil {
		t.Fatal("a client user read the recruiter's client detail")
	}
}
