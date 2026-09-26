//go:build integration

package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"recruiting/internal/domain"
	"recruiting/internal/service"
)

// queueFixture is one org carrying exactly one item of every queue rule, so
// each rule can be watched fire and clear on its own.
type queueFixture struct {
	*pipelineFixture
	queue *service.WorkQueueService
	exec  func(sql string, args ...any)
	// the subject of each rule's row
	scoredAttempt, invitedAttempt, waitingApp, draftPacket, overdueSlot, unratedPairing, waitingIntro uuid.UUID
	// the second recruiter, who shares the queue but not the snoozes
	mateID uuid.UUID
	// assessment the sittings belong to
	assessmentID uuid.UUID
}

func newQueueFixture(t *testing.T) *queueFixture {
	t.Helper()
	pf := newPipelineFixture(t)
	ctx := context.Background()
	f := &queueFixture{pipelineFixture: pf, queue: service.NewWorkQueueService(pf.st)}
	f.exec = func(sql string, args ...any) {
		t.Helper()
		if _, err := pf.sys.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	f.mateID = uuid.New()
	f.exec(`insert into org_user (id, org_id, email, name) values ($1, $2, $3, 'Mate Recruiter')`,
		f.mateID, pf.orgID, "mate-"+pf.orgID.String()+"@example.com")

	problemID := uuid.New()
	f.assessmentID = uuid.New()
	f.exec(`insert into problem (id, org_id, kind, title, statement) values ($1, $2, 'code', 'Adder', 'Add them')`, problemID, pf.orgID)
	f.exec(`insert into assessment (id, org_id, name, duration_minutes) values ($1, $2, 'Screen', 60)`, f.assessmentID, pf.orgID)

	// review: a scored sitting nobody has reviewed.
	f.scoredAttempt = f.attempt(t, "scored", map[string]any{"score": 88, "finished_at": time.Now().Add(-2 * time.Hour)})
	// expiring: an invite that lapses within the day, never started.
	f.invitedAttempt = f.attempt(t, "invited", map[string]any{"invite_expires_at": time.Now().Add(4 * time.Hour)})
	// client_waiting: a question from the client with no answer after it.
	f.waitingApp = pf.appID
	f.exec(`insert into application_event (org_id, application_id, actor_kind, kind, reason) values ($1, $2, 'client_user', $3, 'Can they relocate?')`,
		pf.orgID, f.waitingApp, service.EventRequestInfo)
	// shortlist_draft: a packet built and never sent.
	f.draftPacket = uuid.New()
	f.exec(`insert into shortlist_packet (id, org_id, job_id, status, created_by) values ($1, $2, $3, 'draft', $4)`,
		f.draftPacket, pf.orgID, pf.jobID, pf.userID)
	f.exec(`insert into shortlist_pick (org_id, packet_id, application_id, rank) values ($1, $2, $3, 1)`,
		pf.orgID, f.draftPacket, pf.appID)
	// scorecard_overdue: an interview that ended two days ago.
	f.overdueSlot = uuid.New()
	f.exec(`insert into interview_slot (id, org_id, vetter_id, application_id, stage_id, starts_at, ends_at, status)
		values ($1, $2, $3, $4, $5, now() - interval '2 days', now() - interval '2 days' + interval '1 hour', 'completed')`,
		f.overdueSlot, pf.orgID, pf.userID, pf.appID, pf.stages[domain.StageInterview])
	// sprint_rating: a sprint conversation that ended an hour ago, unrated.
	sprintStage := uuid.New()
	f.exec(`insert into stage (id, org_id, job_id, position, name, kind, round_seconds, break_seconds) values ($1, $2, $3, 7, 'Sprint', 'sprint', 300, 60)`,
		sprintStage, pf.orgID, pf.jobID)
	sprintID := uuid.New()
	f.exec(`insert into sprint (id, org_id, job_id, stage_id, name, status, starts_at, round_seconds, break_seconds)
		values ($1, $2, $3, $4, 'Sprint', 'scheduled', now() - interval '1 hour', 300, 60)`, sprintID, pf.orgID, pf.jobID, sprintStage)
	f.unratedPairing = uuid.New()
	f.exec(`insert into sprint_pairing (id, sprint_id, org_id, round, interviewer_id, application_id) values ($1, $2, $3, 0, $4, $5)`,
		f.unratedPairing, sprintID, pf.orgID, pf.userID, pf.appID)
	// talent_intro: a company asked to meet someone and nobody has sent them
	// the opportunity.
	var companyID, candID uuid.UUID
	if err := pf.sys.QueryRow(ctx, `select client_company_id, candidate_id from application where id = $1`, pf.appID).Scan(&companyID, &candID); err != nil {
		t.Fatal(err)
	}
	requestID := uuid.New()
	f.exec(`insert into talent_request (id, org_id, client_company_id, title, skills) values ($1, $2, $3, 'Go engineer', '{go}')`, requestID, pf.orgID, companyID)
	f.waitingIntro = uuid.New()
	f.exec(`insert into talent_intro (id, org_id, request_id, candidate_id, source, score) values ($1, $2, $3, $4, 'network', 0.7)`,
		f.waitingIntro, pf.orgID, requestID, candID)
	return f
}

// attempt writes one sitting on the fixture's application in the given
// status, with whatever columns the rule under test reads.
func (f *queueFixture) attempt(t *testing.T, status string, cols map[string]any) uuid.UUID {
	t.Helper()
	id := uuid.New()
	f.exec(`insert into attempt (id, org_id, application_id, assessment_id, stage_id, status) values ($1, $2, $3, $4, $5, $6)`,
		id, f.orgID, f.appID, f.assessmentID, f.stages[domain.StageAssessment], status)
	for col, val := range cols {
		f.exec(`update attempt set `+col+` = $2 where id = $1`, id, val)
	}
	return id
}

func (f *queueFixture) list(t *testing.T, p service.Principal, filter service.QueueKind) []service.QueueItem {
	t.Helper()
	items, err := f.queue.List(context.Background(), p, filter)
	if err != nil {
		t.Fatalf("list queue: %v", err)
	}
	return items
}

// subjects is the ids the queue reports for one rule.
func (f *queueFixture) subjects(t *testing.T, p service.Principal, kind service.QueueKind) []uuid.UUID {
	t.Helper()
	out := []uuid.UUID{}
	for _, item := range f.list(t, p, kind) {
		if item.Kind != kind {
			t.Fatalf("filter %s returned a %s row", kind, item.Kind)
		}
		out = append(out, item.SubjectID)
	}
	return out
}

func onlySubject(t *testing.T, got []uuid.UUID, want uuid.UUID) {
	t.Helper()
	if len(got) != 1 || got[0] != want {
		t.Fatalf("subjects = %v, want exactly %v", got, want)
	}
}

func (f *queueFixture) recruiter() service.Principal { return f.principal(service.RoleRecruiter) }

func (f *queueFixture) mate() service.Principal {
	return service.Principal{Kind: service.PrincipalOrgUser, OrgID: f.orgID, UserID: f.mateID, Roles: []string{service.RoleRecruiter}}
}

func TestQueueReviewRuleFiresUntilTheVerdictIsIn(t *testing.T) {
	f := newQueueFixture(t)
	p := f.recruiter()
	onlySubject(t, f.subjects(t, p, service.QueueReview), f.scoredAttempt)

	f.exec(`insert into review (org_id, attempt_id, vetter_id, verdict) values ($1, $2, $3, 'pass')`, f.orgID, f.scoredAttempt, f.userID)
	if got := f.subjects(t, p, service.QueueReview); len(got) != 0 {
		t.Fatalf("a reviewed sitting still sits in the queue: %v", got)
	}
}

func TestQueueReviewIgnoresPreviewSittings(t *testing.T) {
	f := newQueueFixture(t)
	preview := uuid.New()
	f.exec(`insert into attempt (id, org_id, assessment_id, status, preview, preview_user_id, score, finished_at)
		values ($1, $2, $3, 'scored', true, $4, 91, now())`, preview, f.orgID, f.assessmentID, f.userID)
	onlySubject(t, f.subjects(t, f.recruiter(), service.QueueReview), f.scoredAttempt)
}

func TestQueueExpiringRuleFiresUntilTheSittingStarts(t *testing.T) {
	f := newQueueFixture(t)
	p := f.recruiter()
	onlySubject(t, f.subjects(t, p, service.QueueExpiring), f.invitedAttempt)

	f.exec(`update attempt set status = 'started', started_at = now() where id = $1`, f.invitedAttempt)
	if got := f.subjects(t, p, service.QueueExpiring); len(got) != 0 {
		t.Fatalf("a started sitting still sits in the queue: %v", got)
	}
}

func TestQueueExpiringIgnoresInvitesWithRoomLeft(t *testing.T) {
	f := newQueueFixture(t)
	f.exec(`update attempt set invite_expires_at = now() + interval '5 days' where id = $1`, f.invitedAttempt)
	if got := f.subjects(t, f.recruiter(), service.QueueExpiring); len(got) != 0 {
		t.Fatalf("an invite with days left is queued: %v", got)
	}
}

func TestQueueClientWaitingRuleClearsOnTheRecruitersReply(t *testing.T) {
	f := newQueueFixture(t)
	p := f.recruiter()
	onlySubject(t, f.subjects(t, p, service.QueueClientWaiting), f.waitingApp)

	f.exec(`insert into application_event (org_id, application_id, actor_kind, actor_id, kind, reason) values ($1, $2, 'org_user', $3, 'moved', 'They can')`,
		f.orgID, f.waitingApp, f.userID)
	if got := f.subjects(t, p, service.QueueClientWaiting); len(got) != 0 {
		t.Fatalf("an answered question still sits in the queue: %v", got)
	}
}

func TestQueueShortlistDraftRuleClearsWhenThePacketIsSent(t *testing.T) {
	f := newQueueFixture(t)
	p := f.recruiter()
	onlySubject(t, f.subjects(t, p, service.QueueShortlistDraft), f.draftPacket)

	f.exec(`update shortlist_packet set status = 'sent', sent_at = now(), sent_by = $2 where id = $1`, f.draftPacket, f.userID)
	if got := f.subjects(t, p, service.QueueShortlistDraft); len(got) != 0 {
		t.Fatalf("a sent packet still sits in the queue: %v", got)
	}
}

func TestQueueScorecardOverdueRuleClearsWhenTheCardIsFiled(t *testing.T) {
	f := newQueueFixture(t)
	p := f.recruiter()
	onlySubject(t, f.subjects(t, p, service.QueueScorecardOverdue), f.overdueSlot)

	f.exec(`insert into scorecard (org_id, application_id, stage_id, vetter_id, scores, overall) values ($1, $2, $3, $4, '[]', 'yes')`,
		f.orgID, f.appID, f.stages[domain.StageInterview], f.userID)
	if got := f.subjects(t, p, service.QueueScorecardOverdue); len(got) != 0 {
		t.Fatalf("a filed scorecard still sits in the queue: %v", got)
	}
}

func TestQueueScorecardOverdueIgnoresFreshInterviews(t *testing.T) {
	f := newQueueFixture(t)
	f.exec(`update interview_slot set starts_at = now() - interval '2 hours', ends_at = now() - interval '1 hour' where id = $1`, f.overdueSlot)
	if got := f.subjects(t, f.recruiter(), service.QueueScorecardOverdue); len(got) != 0 {
		t.Fatalf("an interview that ended an hour ago is overdue: %v", got)
	}
}

func TestQueueSnoozeHidesTheItemForOneUserOnly(t *testing.T) {
	f := newQueueFixture(t)
	p, mate := f.recruiter(), f.mate()
	until := time.Now().Add(service.SnoozeWindow)
	if err := f.queue.Snooze(context.Background(), p, service.QueueReview, f.scoredAttempt, until); err != nil {
		t.Fatalf("snooze: %v", err)
	}
	if got := f.subjects(t, p, service.QueueReview); len(got) != 0 {
		t.Fatalf("the snoozed item is still in its own queue: %v", got)
	}
	onlySubject(t, f.subjects(t, mate, service.QueueReview), f.scoredAttempt)

	// A snooze that has run out stops hiding anything.
	f.exec(`update queue_snooze set until = now() - interval '1 minute' where user_id = $1`, f.userID)
	onlySubject(t, f.subjects(t, p, service.QueueReview), f.scoredAttempt)
}

func TestQueueSnoozeIsPerSubject(t *testing.T) {
	f := newQueueFixture(t)
	p := f.recruiter()
	if err := f.queue.Snooze(context.Background(), p, service.QueueReview, f.scoredAttempt, time.Now().Add(service.SnoozeWindow)); err != nil {
		t.Fatalf("snooze: %v", err)
	}
	onlySubject(t, f.subjects(t, p, service.QueueExpiring), f.invitedAttempt)
	onlySubject(t, f.subjects(t, p, service.QueueShortlistDraft), f.draftPacket)
}

func TestQueueCountsMatchTheRows(t *testing.T) {
	f := newQueueFixture(t)
	p := f.recruiter()
	counts, err := f.queue.Counts(context.Background(), p)
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	for _, kind := range service.QueueKinds {
		if counts[kind] != 1 {
			t.Fatalf("count for %s = %d, want 1", kind, counts[kind])
		}
	}
	if got := len(f.list(t, p, "")); got != len(service.QueueKinds) {
		t.Fatalf("unfiltered queue holds %d rows, want %d", got, len(service.QueueKinds))
	}
	nav, err := f.queue.NavCounts(context.Background(), p)
	if err != nil {
		t.Fatalf("nav counts: %v", err)
	}
	if nav["queue"] != len(service.QueueKinds) {
		t.Fatalf("nav queue count = %d, want %d", nav["queue"], len(service.QueueKinds))
	}
	if nav["clients"] != 1 || nav["assessments"] != 1 {
		t.Fatalf("nav counts = %v, want one client and one assessment", nav)
	}
}

func TestQueueRowsCarryTheirAction(t *testing.T) {
	f := newQueueFixture(t)
	for _, item := range f.list(t, f.recruiter(), "") {
		if item.ActionURL == "" || item.ActionLabel == "" {
			t.Fatalf("%s row has no action: %+v", item.Kind, item)
		}
		if item.JobTitle == "" || item.ClientName == "" {
			t.Fatalf("%s row names no job or client: %+v", item.Kind, item)
		}
	}
}

func TestQueueRefusesClientUsers(t *testing.T) {
	f := newQueueFixture(t)
	client := service.Principal{Kind: service.PrincipalClientUser, OrgID: f.orgID, UserID: uuid.New()}
	if _, err := f.queue.List(context.Background(), client, ""); err == nil {
		t.Fatal("a client user read the recruiter's work queue")
	}
}

func TestQueueTalentIntroWaitsUntilSent(t *testing.T) {
	f := newQueueFixture(t)
	p := f.recruiter()
	onlySubject(t, f.subjects(t, p, service.QueueTalentIntro), f.waitingIntro)
	items := f.list(t, p, service.QueueTalentIntro)
	if len(items) != 1 || items[0].ActionLabel != "Send opportunity" || items[0].Due == nil {
		t.Fatalf("talent intro item = %+v", items)
	}
	f.exec(`update talent_intro set status = 'sent', sent_at = now() where id = $1`, f.waitingIntro)
	if got := f.subjects(t, p, service.QueueTalentIntro); len(got) != 0 {
		t.Fatalf("a sent introduction is still queued: %v", got)
	}
}
