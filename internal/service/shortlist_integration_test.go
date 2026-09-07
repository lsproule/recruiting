//go:build integration

package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"recruiting/internal/domain"
	"recruiting/internal/queue"
	"recruiting/internal/service"
)

// shortlistFixture is a job whose applications have already sat and been
// scored, plus the client company the packet would go to. The sittings are
// written directly: what the shortlist cares about is the score on them.
type shortlistFixture struct {
	*pipelineFixture
	shortlists *service.ShortlistService
	companyID  uuid.UUID
	clients    int
	// scored applications, best first; low is below any sane threshold and
	// rejected is a strong score on a closed application.
	high, mid, third, low, rejected uuid.UUID
}

func newShortlistFixture(t *testing.T) *shortlistFixture {
	t.Helper()
	pf := newPipelineFixture(t)
	ctx := context.Background()
	f := &shortlistFixture{pipelineFixture: pf, clients: 2}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pf.sys.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	if err := pf.sys.QueryRow(ctx, `select client_company_id from job where id = $1`, pf.jobID).Scan(&f.companyID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < f.clients; i++ {
		exec(`insert into client_user (id, org_id, client_company_id, email, name) values ($1, $2, $3, $4, 'Carl Client')`,
			uuid.New(), pf.orgID, f.companyID, "client"+uuid.NewString()+"@example.com")
	}
	problemID, assessmentID := uuid.New(), uuid.New()
	exec(`insert into problem (id, org_id, kind, title, statement) values ($1, $2, 'code', 'Adder', 'Add them')`, problemID, pf.orgID)
	exec(`insert into assessment (id, org_id, name, duration_minutes) values ($1, $2, 'Screen', 60)`, assessmentID, pf.orgID)
	exec(`insert into assessment_problem (assessment_id, org_id, problem_id, position) values ($1, $2, $3, 1)`, assessmentID, pf.orgID, problemID)

	// scored adds an application with a sitting at that score, in the given
	// stage, so the pool has something to rank.
	scored := func(name string, score float64, stage uuid.UUID, status domain.ApplicationStatus) uuid.UUID {
		t.Helper()
		candID, appID := uuid.New(), uuid.New()
		exec(`insert into candidate (id, org_id, email, name) values ($1, $2, $3, $4)`, candID, pf.orgID, candID.String()+"@example.com", name)
		exec(`insert into application (id, org_id, job_id, candidate_id, client_company_id, stage_id, status) values ($1, $2, $3, $4, $5, $6, $7)`,
			appID, pf.orgID, pf.jobID, candID, f.companyID, stage, string(status))
		exec(`insert into attempt (org_id, application_id, assessment_id, stage_id, status, score, finished_at) values ($1, $2, $3, $4, 'scored', $5, now())`,
			pf.orgID, appID, assessmentID, pf.stages[domain.StageAssessment], score)
		return appID
	}
	assess := pf.stages[domain.StageAssessment]
	f.high = scored("Ada High", 92, assess, domain.StatusActive)
	f.mid = scored("Bo Mid", 81, assess, domain.StatusActive)
	f.third = scored("Ev Third", 76, assess, domain.StatusActive)
	f.low = scored("Cy Low", 44, assess, domain.StatusActive)
	f.rejected = scored("Di Rejected", 90, pf.reject, domain.StatusRejected)

	q, err := queue.New(pf.st.Pool(), queue.Config{})
	if err != nil {
		t.Fatal(err)
	}
	f.shortlists = service.NewShortlistService(pf.st,
		service.NewReleaseService(pf.st, q, "https://example.test"),
		service.NewReviewService(pf.st, nil, nil), service.NewPoolService(pf.st), q, "https://example.test")
	return f
}

func (f *shortlistFixture) save(t *testing.T, p service.Principal, apps ...uuid.UUID) service.ShortlistPacket {
	t.Helper()
	packet, err := f.shortlists.Save(context.Background(), p, nil,
		service.ShortlistInput{JobID: f.jobID, Note: "Ranked on how they debugged.", ApplicationIDs: apps})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	return packet
}

func (f *shortlistFixture) releasedCount(t *testing.T, ids ...uuid.UUID) int {
	t.Helper()
	var n int
	if err := f.sys.QueryRow(context.Background(),
		`select count(*) from application where id = any($1) and released_at is not null`, ids).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSendShortlistReleasesEveryPickAndEmailsEveryClientUser(t *testing.T) {
	f := newShortlistFixture(t)
	rec := f.principal(service.RoleRecruiter)
	before := f.jobs(t, queue.KindEmailSend)

	packet := f.save(t, rec, f.high, f.mid, f.third)
	sent, err := f.shortlists.Send(context.Background(), rec, packet.ID)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if sent.Status != "sent" || sent.SentAt == nil || sent.SentBy != f.userID {
		t.Fatalf("after send: status %q sentAt %v sentBy %s", sent.Status, sent.SentAt, sent.SentBy)
	}
	if got := f.releasedCount(t, f.high, f.mid, f.third); got != 3 {
		t.Fatalf("released %d of 3 picks", got)
	}
	if got := f.jobs(t, queue.KindEmailSend) - before; got != f.clients {
		t.Fatalf("queued %d notices for %d client users", got, f.clients)
	}
	if len(sent.Picks) != 3 {
		t.Fatalf("picks: %d", len(sent.Picks))
	}
	for i, pick := range sent.Picks {
		if pick.Rank != i+1 {
			t.Fatalf("pick %d has rank %d", i, pick.Rank)
		}
	}
	if sent.Picks[0].ApplicationID != f.high || sent.Picks[1].ApplicationID != f.mid {
		t.Fatalf("picks are not in the order they were saved: %+v", sent.Picks)
	}
}

func TestSendShortlistRefusesARejectedPickByName(t *testing.T) {
	f := newShortlistFixture(t)
	rec := f.principal(service.RoleRecruiter)
	packet := f.save(t, rec, f.high, f.rejected)

	_, err := f.shortlists.Send(context.Background(), rec, packet.ID)
	if !errors.Is(err, service.ErrPickRejected) {
		t.Fatalf("send with a rejected pick: %v", err)
	}
	if !strings.Contains(err.Error(), "Di Rejected") {
		t.Fatalf("refusal does not name the candidate: %v", err)
	}
	if got := f.releasedCount(t, f.high, f.rejected); got != 0 {
		t.Fatalf("a refused send released %d applications", got)
	}
}

func TestShortlistPacketIsImmutableOnceSent(t *testing.T) {
	f := newShortlistFixture(t)
	rec := f.principal(service.RoleRecruiter)
	packet := f.save(t, rec, f.high)
	if _, err := f.shortlists.Send(context.Background(), rec, packet.ID); err != nil {
		t.Fatalf("send: %v", err)
	}

	_, err := f.shortlists.Save(context.Background(), rec, &packet.ID,
		service.ShortlistInput{JobID: f.jobID, Note: "second thoughts", ApplicationIDs: []uuid.UUID{f.mid}})
	if !errors.Is(err, service.ErrPacketSent) {
		t.Fatalf("saving over a sent packet: %v", err)
	}
	if _, err := f.shortlists.Send(context.Background(), rec, packet.ID); !errors.Is(err, service.ErrPacketSent) {
		t.Fatalf("sending twice: %v", err)
	}
	again, err := f.shortlists.Get(context.Background(), rec, packet.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if again.Note != "Ranked on how they debugged." || len(again.Picks) != 1 {
		t.Fatalf("the sent packet changed: %+v", again)
	}
}

func TestSaveShortlistRanksPicksUniquelyInTheOrderGiven(t *testing.T) {
	f := newShortlistFixture(t)
	rec := f.principal(service.RoleRecruiter)
	packet := f.save(t, rec, f.mid, f.high)

	reordered, err := f.shortlists.Save(context.Background(), rec, &packet.ID,
		service.ShortlistInput{JobID: f.jobID, Note: packet.Note, ApplicationIDs: []uuid.UUID{f.high, f.mid}})
	if err != nil {
		t.Fatalf("reorder: %v", err)
	}
	if len(reordered.Picks) != 2 || reordered.Picks[0].ApplicationID != f.high || reordered.Picks[1].ApplicationID != f.mid {
		t.Fatalf("reorder did not stick: %+v", reordered.Picks)
	}
	var ranks int
	if err := f.sys.QueryRow(context.Background(),
		`select count(distinct rank) from shortlist_pick where packet_id = $1`, packet.ID).Scan(&ranks); err != nil {
		t.Fatal(err)
	}
	if ranks != 2 {
		t.Fatalf("distinct ranks: %d", ranks)
	}
	if _, err := f.shortlists.Save(context.Background(), rec, &packet.ID,
		service.ShortlistInput{JobID: f.jobID, ApplicationIDs: []uuid.UUID{f.high, f.high}}); err == nil {
		t.Fatal("the same candidate was accepted twice")
	}
}

func TestShortlistPoolKeepsScoredApplicationsAboveTheThreshold(t *testing.T) {
	f := newShortlistFixture(t)
	rec := f.principal(service.RoleRecruiter)

	pool, err := f.shortlists.Pool(context.Background(), rec, f.jobID, 0)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	seen := map[uuid.UUID]bool{}
	for _, c := range pool {
		seen[c.ApplicationID] = true
		if c.Score < service.DefaultShortlistMinScore {
			t.Fatalf("%s scored %v, below the default threshold", c.CandidateName, c.Score)
		}
	}
	if !seen[f.high] || !seen[f.mid] {
		t.Fatalf("the qualified applications are missing: %+v", pool)
	}
	if seen[f.low] {
		t.Fatal("an application below the threshold is in the pool")
	}
	if seen[f.appID] {
		t.Fatal("an application with no scored sitting is in the pool")
	}
}
