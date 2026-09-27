//go:build integration

package service_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"recruiting/internal/domain"
	"recruiting/internal/queue"
	"recruiting/internal/service"
)

// queueFixture is one org carrying exactly one item of every queue rule, so
// each rule can be watched fire and clear on its own. Each step rule gets an
// application of its own, parked where that rule fires.
type queueFixture struct {
	*pipelineFixture
	queue *service.WorkQueueService
	exec  func(sql string, args ...any)
	// the applications behind each step rule; pf.appID itself sits in the
	// first stage and is the résumé to review
	callApp, feedbackApp, decisionApp, examSentApp, examReviewApp, forwardApp, silentApp, waitingApp, planApp uuid.UUID
	// the subjects of the rules that are not applications
	invitedAttempt, scoredAttempt, draftPacket, unratedPairing, waitingIntro uuid.UUID
	feedbackSlot                                                             uuid.UUID
	// the second recruiter, who shares the queue but not the snoozes
	mateID uuid.UUID
	// assessment the sittings belong to, and the sprint stage
	assessmentID, sprintStage uuid.UUID
}

func newQueueFixture(t *testing.T) *queueFixture {
	t.Helper()
	pf := newPipelineFixture(t)
	ctx := context.Background()
	q, err := queue.New(pf.st.Pool(), queue.Config{})
	if err != nil {
		t.Fatal(err)
	}
	f := &queueFixture{pipelineFixture: pf, queue: service.NewWorkQueueService(pf.st)}
	f.queue.Apps = pf.apps
	f.queue.Releases = service.NewReleaseService(pf.st, q, "https://example.test/")
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
	f.sprintStage = uuid.New()
	f.exec(`insert into stage (id, org_id, job_id, position, name, kind, round_seconds, break_seconds) values ($1, $2, $3, 7, 'Sprint', 'sprint', 300, 60)`,
		f.sprintStage, pf.orgID, pf.jobID)

	// call_unbooked: in the interview stage with no slot.
	f.callApp = f.application(t, "Call Candidate", pf.stages[domain.StageInterview])
	// feedback: an interview that ended an hour ago with no scorecard.
	f.feedbackApp = f.application(t, "Feedback Candidate", pf.stages[domain.StageInterview])
	f.feedbackSlot = uuid.New()
	f.exec(`insert into interview_slot (id, org_id, vetter_id, application_id, stage_id, starts_at, ends_at, status)
		values ($1, $2, $3, $4, $5, now() - interval '2 hours', now() - interval '1 hour', 'completed')`,
		f.feedbackSlot, pf.orgID, pf.userID, f.feedbackApp, pf.stages[domain.StageInterview])
	// decision: an interview with its scorecard in.
	f.decisionApp = f.application(t, "Decision Candidate", pf.stages[domain.StageInterview])
	f.exec(`insert into interview_slot (id, org_id, vetter_id, application_id, stage_id, starts_at, ends_at, status)
		values ($1, $2, $3, $4, $5, now() - interval '3 days', now() - interval '3 days' + interval '1 hour', 'completed')`,
		uuid.New(), pf.orgID, pf.userID, f.decisionApp, pf.stages[domain.StageInterview])
	f.exec(`insert into scorecard (org_id, application_id, stage_id, vetter_id, scores, overall) values ($1, $2, $3, $4, '[]', 'yes')`,
		pf.orgID, f.decisionApp, pf.stages[domain.StageInterview], pf.userID)
	// exam_unopened: an invite with hours left, never started.
	f.examSentApp = f.application(t, "Exam Sent Candidate", pf.stages[domain.StageAssessment])
	f.invitedAttempt = f.attempt(t, f.examSentApp, "invited", map[string]any{"invite_expires_at": time.Now().Add(4 * time.Hour)})
	// exam_review: a scored sitting nobody has reviewed.
	f.examReviewApp = f.application(t, "Exam Review Candidate", pf.stages[domain.StageAssessment])
	f.scoredAttempt = f.attempt(t, f.examReviewApp, "scored", map[string]any{"score": 88, "finished_at": time.Now().Add(-2 * time.Hour)})
	// forward: reached the client stage, not released.
	f.forwardApp = f.application(t, "Forward Candidate", pf.stages[domain.StageClientReview])
	// client_silent: released four days ago, nothing from the client since.
	f.silentApp = f.application(t, "Silent Candidate", pf.stages[domain.StageClientReview])
	f.exec(`update application set released_at = now() - interval '4 days' where id = $1`, f.silentApp)
	// client_waiting: released, and the client asked a question.
	f.waitingApp = f.application(t, "Waiting Candidate", pf.stages[domain.StageClientReview])
	f.exec(`update application set released_at = now() - interval '1 day' where id = $1`, f.waitingApp)
	f.exec(`insert into application_event (org_id, application_id, actor_kind, kind, reason) values ($1, $2, 'client_user', $3, 'Can they relocate?')`,
		pf.orgID, f.waitingApp, service.EventRequestInfo)
	// shortlist_plan: in the sprint stage with no sprint to be in.
	f.planApp = f.application(t, "Plan Candidate", f.sprintStage)
	// shortlist_draft: a packet built and never sent.
	f.draftPacket = uuid.New()
	f.exec(`insert into shortlist_packet (id, org_id, job_id, status, created_by) values ($1, $2, $3, 'draft', $4)`,
		f.draftPacket, pf.orgID, pf.jobID, pf.userID)
	f.exec(`insert into shortlist_pick (org_id, packet_id, application_id, rank) values ($1, $2, $3, 1)`,
		pf.orgID, f.draftPacket, pf.appID)
	// sprint_rating: a sprint conversation that ended an hour ago, unrated.
	sprintID := uuid.New()
	f.exec(`insert into sprint (id, org_id, job_id, stage_id, name, status, starts_at, round_seconds, break_seconds)
		values ($1, $2, $3, $4, 'Sprint', 'scheduled', now() - interval '1 hour', 300, 60)`, sprintID, pf.orgID, pf.jobID, f.sprintStage)
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

// application writes one more candidate on the fixture's job, parked in the
// given stage.
func (f *queueFixture) application(t *testing.T, name string, stageID uuid.UUID) uuid.UUID {
	t.Helper()
	var companyID uuid.UUID
	if err := f.sys.QueryRow(context.Background(), `select client_company_id from job where id = $1`, f.jobID).Scan(&companyID); err != nil {
		t.Fatal(err)
	}
	candID, appID := uuid.New(), uuid.New()
	f.exec(`insert into candidate (id, org_id, email, name) values ($1, $2, $3, $4)`, candID, f.orgID, candID.String()+"@example.com", name)
	f.exec(`insert into application (id, org_id, job_id, candidate_id, client_company_id, stage_id) values ($1, $2, $3, $4, $5, $6)`,
		appID, f.orgID, f.jobID, candID, companyID, stageID)
	return appID
}

// attempt writes one sitting on an application in the assessment stage, in
// the given status, with whatever columns the rule under test reads.
func (f *queueFixture) attempt(t *testing.T, appID uuid.UUID, status string, cols map[string]any) uuid.UUID {
	t.Helper()
	id := uuid.New()
	f.exec(`insert into attempt (id, org_id, application_id, assessment_id, stage_id, status) values ($1, $2, $3, $4, $5, $6)`,
		id, f.orgID, appID, f.assessmentID, f.stages[domain.StageAssessment], status)
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

// item is the one row a rule holds.
func (f *queueFixture) item(t *testing.T, p service.Principal, kind service.QueueKind) service.QueueItem {
	t.Helper()
	items := f.list(t, p, kind)
	if len(items) != 1 {
		t.Fatalf("%s holds %d rows, want 1: %+v", kind, len(items), items)
	}
	return items[0]
}

func onlySubject(t *testing.T, got []uuid.UUID, want uuid.UUID) {
	t.Helper()
	if len(got) != 1 || got[0] != want {
		t.Fatalf("subjects = %v, want exactly %v", got, want)
	}
}

func none(t *testing.T, got []uuid.UUID, what string) {
	t.Helper()
	if len(got) != 0 {
		t.Fatalf("%s: %v", what, got)
	}
}

func (f *queueFixture) recruiter() service.Principal { return f.principal(service.RoleRecruiter) }

func (f *queueFixture) mate() service.Principal {
	return service.Principal{Kind: service.PrincipalOrgUser, OrgID: f.orgID, UserID: f.mateID, Roles: []string{service.RoleRecruiter}}
}

func (f *queueFixture) stageOf(t *testing.T, appID uuid.UUID) (uuid.UUID, string) {
	t.Helper()
	var stage uuid.UUID
	var status string
	if err := f.sys.QueryRow(context.Background(), `select stage_id, status from application where id = $1`, appID).Scan(&stage, &status); err != nil {
		t.Fatal(err)
	}
	return stage, status
}

func TestQueueResumeRuleOffersAdvanceAndRejectAndClearsOnTheMove(t *testing.T) {
	f := newQueueFixture(t)
	p := f.recruiter()
	item := f.item(t, p, service.QueueResume)
	if item.ApplicationID != f.appID || len(item.Actions) != 2 {
		t.Fatalf("résumé row = %+v, want the application with advance and reject", item)
	}
	advance, reject := item.Actions[0], item.Actions[1]
	if advance.Kind != service.QueueActionAdvance || advance.ToStageID != f.stages[domain.StageInterview] {
		t.Fatalf("advance = %+v, want the next stage", advance)
	}
	if reject.Kind != service.QueueActionReject || reject.ToStageID != f.reject || !reject.NeedsReason {
		t.Fatalf("reject = %+v, want the rejected stage with a reason", reject)
	}
	if err := f.queue.Decide(context.Background(), p, service.DecideRequest{ApplicationID: f.appID, Action: advance.Kind, ToStageID: advance.ToStageID}); err != nil {
		t.Fatalf("advance from the queue: %v", err)
	}
	none(t, f.subjects(t, p, service.QueueResume), "an advanced applicant still reads as a résumé to review")
	if stage, _ := f.stageOf(t, f.appID); stage != f.stages[domain.StageInterview] {
		t.Fatalf("after advance the application sits in %s", stage)
	}
}

func TestQueueRejectFromTheQueueNeedsAReasonAndEmailsTheCandidate(t *testing.T) {
	f := newQueueFixture(t)
	p := f.recruiter()
	req := service.DecideRequest{ApplicationID: f.appID, Action: service.QueueActionReject, ToStageID: f.reject}
	if err := f.queue.Decide(context.Background(), p, req); err == nil {
		t.Fatal("a reject without a reason went through")
	}
	req.Reason = "Looking for more backend depth"
	if err := f.queue.Decide(context.Background(), p, req); err != nil {
		t.Fatalf("reject with reason: %v", err)
	}
	if _, status := f.stageOf(t, f.appID); status != string(domain.StatusRejected) {
		t.Fatalf("status after reject = %s", status)
	}
	var emails int
	if err := f.sys.QueryRow(context.Background(), `select count(*) from river_job where kind = 'email.send' and args->>'payload' like '%application_rejected%' and args->>'payload' like '%'||$1||'%'`, f.orgID.String()).Scan(&emails); err != nil {
		t.Fatal(err)
	}
	if emails != 1 {
		t.Fatalf("rejection emails queued = %d, want 1", emails)
	}
}

func TestQueueCallRuleFollowsTheBookingThroughToFeedbackAndDecision(t *testing.T) {
	f := newQueueFixture(t)
	p := f.recruiter()
	onlySubject(t, f.subjects(t, p, service.QueueCallUnbooked), f.callApp)

	// Booked for tomorrow: the ball is with the calendar, not a person.
	slot := uuid.New()
	f.exec(`insert into interview_slot (id, org_id, vetter_id, application_id, stage_id, starts_at, ends_at, status)
		values ($1, $2, $3, $4, $5, now() + interval '1 day', now() + interval '1 day' + interval '30 minutes', 'booked')`,
		slot, f.orgID, f.mateID, f.callApp, f.stages[domain.StageInterview])
	none(t, f.subjects(t, p, service.QueueCallUnbooked), "a booked call still reads as unbooked")
	for _, kind := range []service.QueueKind{service.QueueFeedback, service.QueueDecision} {
		for _, item := range f.list(t, p, kind) {
			if item.ApplicationID == f.callApp {
				t.Fatalf("an upcoming call raised a %s row", kind)
			}
		}
	}
	// Once it has happened the feedback is due, at once.
	f.exec(`update interview_slot set starts_at = now() - interval '1 hour', ends_at = now() - interval '30 minutes' where id = $1`, slot)
	got := f.subjects(t, p, service.QueueFeedback)
	if len(got) != 2 {
		t.Fatalf("feedback rows = %v, want the fixture's and the call's", got)
	}
	// And with the feedback in, the decision is.
	f.exec(`insert into scorecard (org_id, application_id, stage_id, vetter_id, scores, overall) values ($1, $2, $3, $4, '[]', 'no')`,
		f.orgID, f.callApp, f.stages[domain.StageInterview], f.mateID)
	if got := f.subjects(t, p, service.QueueFeedback); len(got) != 1 || got[0] != f.feedbackApp {
		t.Fatalf("feedback rows after the scorecard = %v, want only the fixture's", got)
	}
	decisions := f.subjects(t, p, service.QueueDecision)
	if len(decisions) != 2 {
		t.Fatalf("decision rows = %v, want the fixture's and the call's", decisions)
	}
}

func TestQueueExamRulesFollowTheSitting(t *testing.T) {
	f := newQueueFixture(t)
	p := f.recruiter()
	sent := f.item(t, p, service.QueueExamUnopened)
	if sent.ApplicationID != f.examSentApp || sent.Due == nil {
		t.Fatalf("exam sent row = %+v, want the invited application with its expiry", sent)
	}
	// Opened: with the candidate.
	f.exec(`update attempt set status = 'started', started_at = now() where id = $1`, f.invitedAttempt)
	none(t, f.subjects(t, p, service.QueueExamUnopened), "a started exam still reads as unopened")
	// An invite that lapsed unopened is a decision: re-invite or reject.
	f.exec(`update attempt set status = 'invited', started_at = null, invite_expires_at = now() - interval '1 hour' where id = $1`, f.invitedAttempt)
	var lapsed bool
	for _, item := range f.list(t, p, service.QueueDecision) {
		if item.ApplicationID == f.examSentApp && len(item.Actions) == 2 {
			lapsed = true
		}
	}
	if !lapsed {
		t.Fatal("a lapsed invite raised no decision")
	}

	review := f.item(t, p, service.QueueExamReview)
	if review.SubjectID != f.scoredAttempt || review.ActionURL != "/app/reviews/"+f.scoredAttempt.String() {
		t.Fatalf("exam review row = %+v, want the scored attempt and its review link", review)
	}
	f.exec(`insert into review (org_id, attempt_id, vetter_id, verdict) values ($1, $2, $3, 'pass')`, f.orgID, f.scoredAttempt, f.userID)
	none(t, f.subjects(t, p, service.QueueExamReview), "a reviewed sitting still waits for review")
	var decided bool
	for _, item := range f.list(t, p, service.QueueDecision) {
		if item.ApplicationID == f.examReviewApp {
			decided = true
		}
	}
	if !decided {
		t.Fatal("a reviewed exam raised no decision")
	}
}

func TestQueueExamReviewIgnoresPreviewSittings(t *testing.T) {
	f := newQueueFixture(t)
	// A preview belongs to the recruiter who opened it, never to an
	// application; it is scored like a real sitting and must raise nothing.
	f.exec(`insert into attempt (id, org_id, assessment_id, status, preview, preview_user_id, score, finished_at)
		values ($1, $2, $3, 'scored', true, $4, 91, now())`, uuid.New(), f.orgID, f.assessmentID, f.userID)
	onlySubject(t, f.subjects(t, f.recruiter(), service.QueueExamReview), f.scoredAttempt)
}

func TestQueueForwardRuleReleasesFromTheRow(t *testing.T) {
	f := newQueueFixture(t)
	p := f.recruiter()
	item := f.item(t, p, service.QueueForward)
	if item.ApplicationID != f.forwardApp || len(item.Actions) != 1 || item.Actions[0].Kind != service.QueueActionRelease {
		t.Fatalf("forward row = %+v, want the unreleased application with a release", item)
	}
	if err := f.queue.Decide(context.Background(), p, service.DecideRequest{ApplicationID: f.forwardApp, Action: service.QueueActionRelease}); err != nil {
		t.Fatalf("release from the queue: %v", err)
	}
	none(t, f.subjects(t, p, service.QueueForward), "a released application still waits to be forwarded")
}

func TestQueueClientSilentClearsWhenTheClientSpeaks(t *testing.T) {
	f := newQueueFixture(t)
	p := f.recruiter()
	onlySubject(t, f.subjects(t, p, service.QueueClientSilent), f.silentApp)
	f.exec(`insert into application_event (org_id, application_id, actor_kind, kind, reason) values ($1, $2, 'client_user', 'moved', 'Advanced')`, f.orgID, f.silentApp)
	none(t, f.subjects(t, p, service.QueueClientSilent), "a client who moved the candidate still reads as silent")
}

func TestQueueClientWaitingRuleClearsOnTheRecruitersReply(t *testing.T) {
	f := newQueueFixture(t)
	p := f.recruiter()
	onlySubject(t, f.subjects(t, p, service.QueueClientWaiting), f.waitingApp)

	f.exec(`insert into application_event (org_id, application_id, actor_kind, actor_id, kind, reason) values ($1, $2, 'org_user', $3, 'moved', 'They can')`,
		f.orgID, f.waitingApp, f.userID)
	none(t, f.subjects(t, p, service.QueueClientWaiting), "an answered question still sits in the queue")
}

func TestQueueShortlistPlanClearsOnceTheCandidateIsInASprint(t *testing.T) {
	f := newQueueFixture(t)
	p := f.recruiter()
	item := f.item(t, p, service.QueueShortlistPlan)
	if item.ApplicationID != f.planApp || item.ActionURL != "/app/jobs/"+f.jobID.String()+"/sprints" {
		t.Fatalf("plan row = %+v", item)
	}
	sprintID := uuid.New()
	f.exec(`insert into sprint (id, org_id, job_id, stage_id, name, status, starts_at, round_seconds, break_seconds)
		values ($1, $2, $3, $4, 'Round robin', 'scheduled', now() + interval '1 day', 300, 60)`, sprintID, f.orgID, f.jobID, f.sprintStage)
	f.exec(`insert into sprint_candidate (sprint_id, org_id, application_id, position) values ($1, $2, $3, 0)`, sprintID, f.orgID, f.planApp)
	none(t, f.subjects(t, p, service.QueueShortlistPlan), "a candidate in a sprint still waits for a plan")
}

func TestQueueShortlistDraftRuleClearsWhenThePacketIsSent(t *testing.T) {
	f := newQueueFixture(t)
	p := f.recruiter()
	onlySubject(t, f.subjects(t, p, service.QueueShortlistDraft), f.draftPacket)

	f.exec(`update shortlist_packet set status = 'sent', sent_at = now(), sent_by = $2 where id = $1`, f.draftPacket, f.userID)
	none(t, f.subjects(t, p, service.QueueShortlistDraft), "a sent packet still sits in the queue")
}

func TestQueueFeedbackRuleClearsWhenTheCardIsFiled(t *testing.T) {
	f := newQueueFixture(t)
	p := f.recruiter()
	onlySubject(t, f.subjects(t, p, service.QueueFeedback), f.feedbackApp)

	f.exec(`insert into scorecard (org_id, application_id, stage_id, vetter_id, scores, overall) values ($1, $2, $3, $4, '[]', 'yes')`,
		f.orgID, f.feedbackApp, f.stages[domain.StageInterview], f.userID)
	none(t, f.subjects(t, p, service.QueueFeedback), "a filed scorecard still sits in the queue")
}

func TestQueueSnoozeHidesTheItemForOneUserOnly(t *testing.T) {
	f := newQueueFixture(t)
	p, mate := f.recruiter(), f.mate()
	until := time.Now().Add(service.SnoozeWindow)
	if err := f.queue.Snooze(context.Background(), p, service.QueueExamReview, f.scoredAttempt, until); err != nil {
		t.Fatalf("snooze: %v", err)
	}
	none(t, f.subjects(t, p, service.QueueExamReview), "the snoozed item is still in its own queue")
	onlySubject(t, f.subjects(t, mate, service.QueueExamReview), f.scoredAttempt)

	// A snooze that has run out stops hiding anything.
	f.exec(`update queue_snooze set until = now() - interval '1 minute' where user_id = $1`, f.userID)
	onlySubject(t, f.subjects(t, p, service.QueueExamReview), f.scoredAttempt)
}

func TestQueueSnoozeIsPerSubject(t *testing.T) {
	f := newQueueFixture(t)
	p := f.recruiter()
	if err := f.queue.Snooze(context.Background(), p, service.QueueExamReview, f.scoredAttempt, time.Now().Add(service.SnoozeWindow)); err != nil {
		t.Fatalf("snooze: %v", err)
	}
	onlySubject(t, f.subjects(t, p, service.QueueExamUnopened), f.examSentApp)
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
			t.Fatalf("count for %s = %d, want 1 (%v)", kind, counts[kind], f.list(t, p, kind))
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

func TestQueueRowsCarryTheirActionAndCompany(t *testing.T) {
	f := newQueueFixture(t)
	for _, item := range f.list(t, f.recruiter(), "") {
		if item.ActionURL == "" || item.ActionLabel == "" {
			t.Fatalf("%s row has no action: %+v", item.Kind, item)
		}
		if item.JobTitle == "" || item.ClientName == "" {
			t.Fatalf("%s row names no job or client: %+v", item.Kind, item)
		}
		if item.Kind.Step() < 1 || item.Kind.Step() > 9 {
			t.Fatalf("%s is step %d", item.Kind, item.Kind.Step())
		}
	}
}

func TestQueueRefusesClientUsers(t *testing.T) {
	f := newQueueFixture(t)
	client := service.Principal{Kind: service.PrincipalClientUser, OrgID: f.orgID, UserID: uuid.New()}
	if _, err := f.queue.List(context.Background(), client, ""); err == nil {
		t.Fatal("a client user read the recruiter's work queue")
	}
	if err := f.queue.Decide(context.Background(), client, service.DecideRequest{ApplicationID: f.appID, Action: service.QueueActionRelease}); err == nil {
		t.Fatal("a client user decided from the recruiter's queue")
	}
}

func TestQueueTalentIntroWaitsUntilSent(t *testing.T) {
	f := newQueueFixture(t)
	p := f.recruiter()
	onlySubject(t, f.subjects(t, p, service.QueueTalentIntro), f.waitingIntro)
	items := f.list(t, p, service.QueueTalentIntro)
	if items[0].ActionLabel != "Send opportunity" {
		t.Fatalf("intro row = %+v", items[0])
	}
	f.exec(`update talent_intro set status = 'sent', sent_at = now() where id = $1`, f.waitingIntro)
	none(t, f.subjects(t, p, service.QueueTalentIntro), "a sent introduction still sits in the queue")
}

func (f *queueFixture) navCounts(t *testing.T, p service.Principal) map[string]int {
	t.Helper()
	nav, err := f.queue.NavCounts(context.Background(), p)
	if err != nil {
		t.Fatalf("nav counts: %v", err)
	}
	return nav
}

// The badges are a scan of the queue, and every screen draws them, so they
// are served from memory: a second read within the TTL runs no query, a
// decision through the service refreshes them, and the TTL catches whatever
// was written behind the service's back.
func TestQueueNavCountsAreCachedUntilAWriteOrTheTTL(t *testing.T) {
	f := newQueueFixture(t)
	p := f.recruiter()
	now := time.Now()
	f.queue.Now = func() time.Time { return now }
	want := len(service.QueueKinds)
	if nav := f.navCounts(t, p); nav["queue"] != want || f.queue.NavLoads() != 1 {
		t.Fatalf("first read: queue = %d (want %d), loads = %d (want 1)", nav["queue"], want, f.queue.NavLoads())
	}
	// A row written directly to the table is not seen: the second read is
	// answered from memory without a query.
	f.application(t, "Late Applicant", f.stages[domain.StageGeneric])
	if nav := f.navCounts(t, p); nav["queue"] != want || f.queue.NavLoads() != 1 {
		t.Fatalf("second read: queue = %d (want the cached %d), loads = %d (want 1)", nav["queue"], want, f.queue.NavLoads())
	}
	// A decision through the service drops the cache; the next read scans
	// again and finds the row written meanwhile. The résumé advanced into
	// the interview stage is a call to book, so the total grows by one.
	if err := f.queue.Decide(context.Background(), p, service.DecideRequest{ApplicationID: f.appID, Action: service.QueueActionAdvance, ToStageID: f.stages[domain.StageInterview]}); err != nil {
		t.Fatalf("advance from the queue: %v", err)
	}
	if nav := f.navCounts(t, p); nav["queue"] != want+1 || f.queue.NavLoads() != 2 {
		t.Fatalf("after decide: queue = %d (want %d), loads = %d (want 2)", nav["queue"], want+1, f.queue.NavLoads())
	}
	// Within the TTL a write made elsewhere stays invisible; past it the
	// badge catches up.
	f.application(t, "Later Applicant", f.stages[domain.StageGeneric])
	if nav := f.navCounts(t, p); nav["queue"] != want+1 || f.queue.NavLoads() != 2 {
		t.Fatalf("within the TTL: queue = %d (want %d), loads = %d (want 2)", nav["queue"], want+1, f.queue.NavLoads())
	}
	now = now.Add(service.NavCountsTTL + time.Second)
	if nav := f.navCounts(t, p); nav["queue"] != want+2 || f.queue.NavLoads() != 3 {
		t.Fatalf("past the TTL: queue = %d (want %d), loads = %d (want 3)", nav["queue"], want+2, f.queue.NavLoads())
	}
	// A snooze refreshes the badges too, and only the snoozer's badge
	// drops: the colleague's queue is their own read.
	if err := f.queue.Snooze(context.Background(), p, service.QueueExamReview, f.scoredAttempt, now.Add(service.SnoozeWindow)); err != nil {
		t.Fatalf("snooze: %v", err)
	}
	if nav := f.navCounts(t, p); nav["queue"] != want+1 || f.queue.NavLoads() != 4 {
		t.Fatalf("after snooze: queue = %d (want %d), loads = %d (want 4)", nav["queue"], want+1, f.queue.NavLoads())
	}
	if nav := f.navCounts(t, f.mate()); nav["queue"] != want+2 || f.queue.NavLoads() != 5 {
		t.Fatalf("colleague: queue = %d (want %d), loads = %d (want 5)", nav["queue"], want+2, f.queue.NavLoads())
	}
}

// Many pages opened at once, each drawing the sidebar, share one scan.
func TestQueueNavCountsShareOneScanAcrossConcurrentReads(t *testing.T) {
	f := newQueueFixture(t)
	p := f.recruiter()
	f.queue.Invalidate(f.orgID)
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			nav, err := f.queue.NavCounts(context.Background(), p)
			if err == nil && nav["queue"] != len(service.QueueKinds) {
				err = fmt.Errorf("queue = %d, want %d", nav["queue"], len(service.QueueKinds))
			}
			if err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if got := f.queue.NavLoads(); got != 1 {
		t.Fatalf("16 concurrent reads ran %d scans, want 1", got)
	}
}

// NavCountsFrom is the queue screen's path: the rows it has already listed
// count as the badge, only the other badges are read, and the cache takes
// the result so the next screen is served from memory.
func TestQueueNavCountsFromTheListPrimesTheCache(t *testing.T) {
	f := newQueueFixture(t)
	p := f.recruiter()
	items := f.list(t, p, "")
	nav, err := f.queue.NavCountsFrom(context.Background(), p, items[:3])
	if err != nil {
		t.Fatalf("nav counts from list: %v", err)
	}
	if nav["queue"] != 3 || nav["clients"] != 1 || nav["assessments"] != 1 {
		t.Fatalf("nav counts = %v, want the three rows given and one client and assessment", nav)
	}
	if nav := f.navCounts(t, p); nav["queue"] != 3 || f.queue.NavLoads() != 1 {
		t.Fatalf("after priming: queue = %d (want 3), loads = %d (want 1)", nav["queue"], f.queue.NavLoads())
	}
}
