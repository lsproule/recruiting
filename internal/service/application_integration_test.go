//go:build integration

package service_test

import (
	"context"
	"errors"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"recruiting/internal/domain"
	"recruiting/internal/queue"
	"recruiting/internal/service"
	"recruiting/internal/store"
)

type pipelineFixture struct {
	sys    *pgxpool.Pool
	st     *store.Store
	apps   *service.ApplicationService
	orgID  uuid.UUID
	jobID  uuid.UUID
	appID  uuid.UUID
	userID uuid.UUID
	stages map[domain.StageKind]uuid.UUID // one stage per kind; terminal keyed by outcome below
	hired  uuid.UUID
	reject uuid.UUID
}

func newPipelineFixture(t *testing.T) *pipelineFixture {
	t.Helper()
	ownerURL := os.Getenv("DATABASE_URL")
	if ownerURL == "" {
		t.Fatal("DATABASE_URL is not set; run `make dev-up` and use `make test-integration`")
	}
	lockSchemaShared(t, ownerURL)
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

	f := &pipelineFixture{sys: sys, st: st, orgID: uuid.New(), jobID: uuid.New(), appID: uuid.New(), userID: uuid.New(), stages: map[domain.StageKind]uuid.UUID{}}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := sys.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`insert into org (id, name, slug) values ($1, $2, $2)`, f.orgID, f.orgID.String())
	t.Cleanup(func() { _, _ = sys.Exec(context.Background(), `delete from org where id = $1`, f.orgID) })
	exec(`insert into org_user (id, org_id, email, name) values ($1, $2, $3, $3)`, f.userID, f.orgID, "rec-"+f.orgID.String()+"@example.com")
	companyID := uuid.New()
	exec(`insert into client_company (id, org_id, name) values ($1, $2, 'Globex')`, companyID, f.orgID)
	exec(`insert into job (id, org_id, client_company_id, title, slug, status) values ($1, $2, $3, 'Go Engineer', $4, 'open')`, f.jobID, f.orgID, companyID, "go-"+f.orgID.String())
	for i, k := range []domain.StageKind{domain.StageGeneric, domain.StageInterview, domain.StageAssessment, domain.StageClientReview} {
		id := uuid.New()
		f.stages[k] = id
		exec(`insert into stage (id, org_id, job_id, position, name, kind) values ($1, $2, $3, $4, $5, $5)`, id, f.orgID, f.jobID, i+1, string(k))
	}
	f.hired, f.reject = uuid.New(), uuid.New()
	exec(`insert into stage (id, org_id, job_id, position, name, kind, terminal_status) values ($1, $2, $3, 5, 'Hired', 'terminal', 'hired')`, f.hired, f.orgID, f.jobID)
	exec(`insert into stage (id, org_id, job_id, position, name, kind, terminal_status) values ($1, $2, $3, 6, 'Rejected', 'terminal', 'rejected')`, f.reject, f.orgID, f.jobID)
	candID := uuid.New()
	exec(`insert into candidate (id, org_id, email, name) values ($1, $2, $3, 'Ada Lovelace')`, candID, f.orgID, "ada-"+f.orgID.String()+"@example.com")
	exec(`insert into application (id, org_id, job_id, candidate_id, client_company_id, stage_id) values ($1, $2, $3, $4, $5, $6)`, f.appID, f.orgID, f.jobID, candID, companyID, f.stages[domain.StageGeneric])

	q, err := queue.New(st.Pool(), queue.Config{})
	if err != nil {
		t.Fatal(err)
	}
	f.apps = service.NewApplicationService(st, q, "https://example.test/")
	return f
}

func lockSchemaShared(t *testing.T, ownerURL string) {
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

func (f *pipelineFixture) principal(roles ...string) service.Principal {
	return service.Principal{Kind: service.PrincipalOrgUser, OrgID: f.orgID, UserID: f.userID, Roles: roles}
}

func (f *pipelineFixture) move(t *testing.T, p service.Principal, to uuid.UUID, req service.MoveRequest) (service.Application, error) {
	t.Helper()
	req.ApplicationID, req.ToStageID = f.appID, to
	return f.apps.Move(context.Background(), p, req)
}

// jobs counts queued jobs of kind whose payload names the fixture's org.
func (f *pipelineFixture) jobs(t *testing.T, kind string) int {
	t.Helper()
	var n int
	err := f.sys.QueryRow(context.Background(),
		`select count(*) from river_job where kind = $1 and args->>'payload' like '%' || $2 || '%'`, kind, f.orgID.String()).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMoveWritesAnEventAndQueuesTheBookingInvite(t *testing.T) {
	f := newPipelineFixture(t)
	rec := f.principal(service.RoleRecruiter)

	app, err := f.move(t, rec, f.stages[domain.StageInterview], service.MoveRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if app.StageID != f.stages[domain.StageInterview] || app.Status != domain.StatusActive {
		t.Fatalf("after move: stage %s status %s", app.StageName, app.Status)
	}
	events, err := f.apps.Events(context.Background(), rec, f.appID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != service.EventMoved || events[0].FromStage != "generic" || events[0].ToStage != "interview" || events[0].ActorKind != "org_user" || events[0].ActorID != f.userID {
		t.Fatalf("events = %+v", events)
	}
	if n := f.jobs(t, queue.KindEmailSend); n != 1 {
		t.Fatalf("booking invite jobs = %d, want exactly one", n)
	}
	var links int
	if err := f.sys.QueryRow(context.Background(), `select count(*) from magic_link where purpose = 'book' and subject_id = $1`, f.appID).Scan(&links); err != nil {
		t.Fatal(err)
	}
	if links != 1 {
		t.Fatalf("book links = %d, want 1", links)
	}
}

func TestLeavingInterviewNeedsAScorecardOrAnOverride(t *testing.T) {
	f := newPipelineFixture(t)
	rec := f.principal(service.RoleRecruiter)
	if _, err := f.move(t, rec, f.stages[domain.StageInterview], service.MoveRequest{}); err != nil {
		t.Fatal(err)
	}
	_, err := f.move(t, rec, f.stages[domain.StageAssessment], service.MoveRequest{})
	if !errors.Is(err, domain.ErrPrereqMissing) {
		t.Fatalf("without scorecard: %v, want ErrPrereqMissing", err)
	}
	if n := f.jobs(t, queue.KindAssessmentInvite); n != 0 {
		t.Fatalf("refused move still queued %d assessment invites", n)
	}
	_, err = f.move(t, rec, f.stages[domain.StageAssessment], service.MoveRequest{OverridePrereq: true})
	if !errors.Is(err, domain.ErrReasonRequired) {
		t.Fatalf("override without reason: %v, want ErrReasonRequired", err)
	}
	if _, err := f.move(t, rec, f.stages[domain.StageAssessment], service.MoveRequest{OverridePrereq: true, Reason: "screened by phone"}); err != nil {
		t.Fatal(err)
	}
	events, _ := f.apps.Events(context.Background(), rec, f.appID)
	last := events[len(events)-1]
	if !last.Override || last.Reason != "screened by phone" {
		t.Fatalf("override event = %+v", last)
	}
	if n := f.jobs(t, queue.KindAssessmentInvite); n != 1 {
		t.Fatalf("assessment invite jobs = %d, want exactly one", n)
	}

	// A scorecard on the interview stage satisfies the prerequisite.
	f2 := newPipelineFixture(t)
	rec2 := f2.principal(service.RoleRecruiter)
	if _, err := f2.move(t, rec2, f2.stages[domain.StageInterview], service.MoveRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f2.sys.Exec(context.Background(), `insert into scorecard (org_id, application_id, stage_id, vetter_id, overall) values ($1, $2, $3, $4, 'yes')`,
		f2.orgID, f2.appID, f2.stages[domain.StageInterview], f2.userID); err != nil {
		t.Fatal(err)
	}
	if _, err := f2.move(t, f2.principal(service.RoleVetter), f2.stages[domain.StageAssessment], service.MoveRequest{}); err != nil {
		t.Fatalf("vetter advance after scorecard: %v", err)
	}
}

func TestMoveRolesAndTerminals(t *testing.T) {
	f := newPipelineFixture(t)
	rec := f.principal(service.RoleRecruiter)
	if _, err := f.move(t, f.principal(service.RoleVetter), f.stages[domain.StageInterview], service.MoveRequest{}); !errors.Is(err, domain.ErrForbiddenMove) {
		t.Errorf("vetter out of generic: %v", err)
	}
	if _, err := f.move(t, rec, f.reject, service.MoveRequest{}); !errors.Is(err, domain.ErrReasonRequired) {
		t.Errorf("reject without reason: %v", err)
	}
	if _, err := f.move(t, rec, uuid.New(), service.MoveRequest{}); !errors.Is(err, service.ErrNotFound) {
		t.Errorf("unknown stage: %v", err)
	}
	app, err := f.move(t, rec, f.reject, service.MoveRequest{Reason: "not a fit"})
	if err != nil || app.Status != domain.StatusRejected {
		t.Fatalf("reject: %v, status %s", err, app.Status)
	}
	if _, err := f.move(t, f.principal(service.RoleAdmin), f.stages[domain.StageGeneric], service.MoveRequest{}); !errors.Is(err, domain.ErrTerminal) {
		t.Errorf("out of rejected: %v, want ErrTerminal", err)
	}
	if _, err := f.apps.Withdraw(context.Background(), rec, f.appID, ""); !errors.Is(err, service.ErrNotActive) {
		t.Errorf("withdraw a closed application: %v", err)
	}
}

func TestWithdrawReleaseAndUnreleaseAreEvents(t *testing.T) {
	f := newPipelineFixture(t)
	rec := f.principal(service.RoleRecruiter)
	ctx := context.Background()
	if _, err := f.apps.Release(ctx, f.principal(service.RoleVetter), f.appID); !errors.Is(err, service.ErrForbidden) {
		t.Errorf("vetter release: %v", err)
	}
	app, err := f.apps.Release(ctx, rec, f.appID)
	if err != nil || !app.Released {
		t.Fatalf("release: %v, released %v", err, app.Released)
	}
	if _, err := f.apps.Release(ctx, rec, f.appID); err != nil {
		t.Fatal(err)
	}
	if app, err = f.apps.Unrelease(ctx, rec, f.appID); err != nil || app.Released {
		t.Fatalf("unrelease: %v, released %v", err, app.Released)
	}
	if _, err := f.apps.Withdraw(ctx, f.principal(service.RoleVetter), f.appID, "moved on"); !errors.Is(err, service.ErrForbidden) {
		t.Errorf("vetter withdraw: %v", err)
	}
	if app, err = f.apps.Withdraw(ctx, rec, f.appID, "took another offer"); err != nil || app.Status != domain.StatusWithdrawn || app.StageID != f.stages[domain.StageGeneric] {
		t.Fatalf("withdraw: %v, %+v", err, app)
	}
	events, err := f.apps.Events(ctx, rec, f.appID)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range events {
		kinds = append(kinds, e.Kind)
	}
	want := []string{service.EventReleased, service.EventUnreleased, service.EventWithdrawn}
	if len(kinds) != len(want) {
		t.Fatalf("event kinds = %v, want %v (a repeated release records nothing)", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("event kinds = %v, want %v", kinds, want)
		}
	}
}

func TestBoardAndListFilter(t *testing.T) {
	f := newPipelineFixture(t)
	rec := f.principal(service.RoleRecruiter)
	ctx := context.Background()
	board, err := f.apps.Board(ctx, rec, f.jobID)
	if err != nil {
		t.Fatal(err)
	}
	if len(board.Columns) != 6 || len(board.Columns[0].Cards) != 1 || board.Columns[0].Cards[0].CandidateName != "Ada Lovelace" {
		t.Fatalf("board = %+v", board.Columns)
	}
	list, err := f.apps.List(ctx, rec, f.jobID, service.ListFilter{Query: "ADA"})
	if err != nil || len(list) != 1 {
		t.Fatalf("list by name: %v, %d rows", err, len(list))
	}
	list, _ = f.apps.List(ctx, rec, f.jobID, service.ListFilter{StageID: f.stages[domain.StageInterview]})
	if len(list) != 0 {
		t.Fatalf("list by empty stage: %d rows", len(list))
	}
	list, _ = f.apps.List(ctx, rec, f.jobID, service.ListFilter{Status: domain.StatusRejected})
	if len(list) != 0 {
		t.Fatalf("list by status: %d rows", len(list))
	}
}

func TestConcurrentMovesLetExactlyOneThrough(t *testing.T) {
	f := newPipelineFixture(t)
	rec := f.principal(service.RoleRecruiter)
	const n = 3
	var wg sync.WaitGroup
	start := make(chan struct{})
	var mu sync.Mutex
	var won int
	var lost []error
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := f.move(t, rec, f.stages[domain.StageInterview], service.MoveRequest{})
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				won++
			} else {
				lost = append(lost, err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if won != 1 {
		t.Fatalf("winning moves = %d, want exactly one (losers: %v)", won, lost)
	}
	for _, err := range lost {
		if !errors.Is(err, service.ErrStale) {
			t.Errorf("loser got %v, want ErrStale", err)
		}
	}
	events, err := f.apps.Events(context.Background(), rec, f.appID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Errorf("events = %d, want 1", len(events))
	}
	if n := f.jobs(t, queue.KindEmailSend); n != 1 {
		t.Errorf("booking invites = %d, want 1", n)
	}
}

// Eight moves at once is more than the pool used to have connections for
// when a move held two transactions; it must simply serialise.
func TestManyConcurrentMovesDoNotExhaustThePool(t *testing.T) {
	f := newPipelineFixture(t)
	rec := f.principal(service.RoleRecruiter)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = f.apps.Move(ctx, rec, service.MoveRequest{ApplicationID: f.appID, ToStageID: f.stages[domain.StageInterview]})
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("concurrent moves did not finish; pool deadlock")
	}
	app, err := f.apps.Application(context.Background(), rec, f.appID)
	if err != nil || app.StageID != f.stages[domain.StageInterview] {
		t.Fatalf("after the storm: %v, stage %s", err, app.StageName)
	}
}

func TestReenteringInterviewRevokesTheOldBookingLink(t *testing.T) {
	f := newPipelineFixture(t)
	rec := f.principal(service.RoleAdmin)
	if _, err := f.move(t, rec, f.stages[domain.StageInterview], service.MoveRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.move(t, rec, f.stages[domain.StageGeneric], service.MoveRequest{OverridePrereq: true, Reason: "sent back"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.move(t, rec, f.stages[domain.StageInterview], service.MoveRequest{}); err != nil {
		t.Fatal(err)
	}
	var live, revoked int
	err := f.sys.QueryRow(context.Background(),
		`select count(*) filter (where revoked_at is null), count(*) filter (where revoked_at is not null) from magic_link where purpose = 'book' and subject_id = $1`, f.appID).Scan(&live, &revoked)
	if err != nil {
		t.Fatal(err)
	}
	if live != 1 || revoked != 1 {
		t.Fatalf("book links live %d revoked %d, want 1 and 1", live, revoked)
	}
}

func TestMoveRefusesOtherJobsStagesAndUnreleasedClientReads(t *testing.T) {
	f := newPipelineFixture(t)
	ctx := context.Background()
	rec := f.principal(service.RoleRecruiter)
	otherJob, otherStage := uuid.New(), uuid.New()
	var companyID uuid.UUID
	if err := f.sys.QueryRow(ctx, `select client_company_id from application where id = $1`, f.appID).Scan(&companyID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sys.Exec(ctx, `insert into job (id, org_id, client_company_id, title, slug, status) values ($1, $2, $3, 'Other', $4, 'open')`, otherJob, f.orgID, companyID, "other-"+f.orgID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sys.Exec(ctx, `insert into stage (id, org_id, job_id, position, name, kind) values ($1, $2, $3, 1, 'X', 'generic')`, otherStage, f.orgID, otherJob); err != nil {
		t.Fatal(err)
	}
	if _, err := f.move(t, rec, otherStage, service.MoveRequest{}); !errors.Is(err, service.ErrNotFound) {
		t.Errorf("cross-job stage: %v, want ErrNotFound", err)
	}
	if _, err := f.move(t, rec, f.stages[domain.StageGeneric], service.MoveRequest{}); !errors.Is(err, service.ErrStale) {
		t.Errorf("move to the current stage: %v, want ErrStale", err)
	}
	client := service.Principal{Kind: service.PrincipalClientUser, OrgID: f.orgID, ClientCompanyID: companyID, UserID: uuid.New()}
	if _, err := f.move(t, client, f.stages[domain.StageClientReview], service.MoveRequest{}); !errors.Is(err, service.ErrNotFound) {
		t.Errorf("client on an unreleased application: %v, want ErrNotFound", err)
	}
	if _, err := f.apps.Release(ctx, rec, f.appID); err != nil {
		t.Fatal(err)
	}
	stranger := client
	stranger.ClientCompanyID = uuid.New()
	if _, err := f.move(t, stranger, f.stages[domain.StageClientReview], service.MoveRequest{}); !errors.Is(err, service.ErrNotFound) {
		t.Errorf("client of another company: %v, want ErrNotFound", err)
	}
	if _, err := f.move(t, client, f.stages[domain.StageClientReview], service.MoveRequest{}); !errors.Is(err, domain.ErrForbiddenMove) {
		t.Errorf("client out of generic: %v, want ErrForbiddenMove", err)
	}
	if _, err := f.move(t, rec, f.stages[domain.StageClientReview], service.MoveRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.move(t, client, f.hired, service.MoveRequest{}); !errors.Is(err, domain.ErrForbiddenMove) {
		t.Errorf("client hire: %v, want ErrForbiddenMove", err)
	}
	app, err := f.move(t, client, f.reject, service.MoveRequest{Reason: "no"})
	if err != nil || app.Status != domain.StatusRejected {
		t.Fatalf("client reject: %v %+v", err, app)
	}
	events, _ := f.apps.Events(ctx, rec, f.appID)
	last := events[len(events)-1]
	if last.ActorKind != "client_user" || last.ActorID != client.UserID {
		t.Errorf("client event actor = %+v", last)
	}
}
