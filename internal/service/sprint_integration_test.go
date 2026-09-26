//go:build integration

package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"recruiting/internal/domain"
	"recruiting/internal/queue"
	"recruiting/internal/service"
)

type sprintFixture struct {
	*pipelineFixture
	sprints *service.SprintService
	rooms   *service.RoomService
	stage   uuid.UUID
	// two more candidates beside the fixture's own, and a second interviewer
	apps     []uuid.UUID
	vetterID uuid.UUID
	now      time.Time
}

func newSprintFixture(t *testing.T) *sprintFixture {
	t.Helper()
	pf := newPipelineFixture(t)
	ctx := context.Background()
	f := &sprintFixture{pipelineFixture: pf, stage: uuid.New(), vetterID: uuid.New()}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pf.sys.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	// The pipeline fixture has stages at positions 1–6; the sprint stage
	// goes at 7 so both terminals still close the pipeline.
	exec(`update stage set position = position + 10 where job_id = $1 and kind = 'terminal'`, pf.jobID)
	exec(`insert into stage (id, org_id, job_id, position, name, kind, round_seconds, break_seconds) values ($1, $2, $3, 7, 'Screening sprint', 'sprint', 300, 60)`,
		f.stage, pf.orgID, pf.jobID)
	exec(`insert into org_user (id, org_id, email, name) values ($1, $2, $3, 'Vera Vetter')`, f.vetterID, pf.orgID, "vet-"+pf.orgID.String()+"@example.com")
	exec(`insert into org_user_role (org_user_id, org_id, role) values ($1, $2, 'vetter')`, f.vetterID, pf.orgID)
	exec(`insert into org_user_role (org_user_id, org_id, role) values ($1, $2, 'recruiter')`, pf.userID, pf.orgID)
	var companyID uuid.UUID
	if err := pf.sys.QueryRow(ctx, `select client_company_id from job where id = $1`, pf.jobID).Scan(&companyID); err != nil {
		t.Fatal(err)
	}
	exec(`update application set stage_id = $1 where id = $2`, f.stage, pf.appID)
	f.apps = []uuid.UUID{pf.appID}
	for _, name := range []string{"Bob Builder", "Cy Coder"} {
		cand, app := uuid.New(), uuid.New()
		exec(`insert into candidate (id, org_id, email, name) values ($1, $2, $3, $4)`, cand, pf.orgID, cand.String()+"@example.com", name)
		exec(`insert into application (id, org_id, job_id, candidate_id, client_company_id, stage_id) values ($1, $2, $3, $4, $5, $6)`, app, pf.orgID, pf.jobID, cand, companyID, f.stage)
		f.apps = append(f.apps, app)
	}
	q, err := queue.New(pf.st.Pool(), queue.Config{})
	if err != nil {
		t.Fatal(err)
	}
	f.sprints = service.NewSprintService(pf.st, q, "https://example.test/")
	f.rooms = service.NewRoomService(pf.st, nil)
	f.now = time.Date(2026, time.October, 5, 9, 0, 0, 0, time.UTC)
	f.sprints.Now = func() time.Time { return f.now }
	f.rooms.Now = func() time.Time { return f.now }
	return f
}

func (f *sprintFixture) recruiter() service.Principal { return f.principal(service.RoleRecruiter) }

func (f *sprintFixture) vetter() service.Principal {
	return service.Principal{Kind: service.PrincipalOrgUser, OrgID: f.orgID, UserID: f.vetterID, Roles: []string{service.RoleVetter}}
}

func (f *sprintFixture) input(start time.Time) service.SprintInput {
	return service.SprintInput{
		JobID: f.jobID, StageID: f.stage, Name: "Monday sprint", StartsAt: start,
		RoundSeconds: 300, BreakSeconds: 60,
		InterviewerIDs: []uuid.UUID{f.userID, f.vetterID}, ApplicationIDs: f.apps,
	}
}

func (f *sprintFixture) candidateLink(t *testing.T, sp service.Sprint, applicationID uuid.UUID) service.Principal {
	t.Helper()
	for _, c := range sp.Candidates {
		if c.ApplicationID == applicationID {
			return service.Principal{Kind: service.PrincipalMagicLink, OrgID: f.orgID, MagicPurpose: service.LinkSprint, SubjectID: c.ID}
		}
	}
	t.Fatalf("no candidate row for %s", applicationID)
	return service.Principal{}
}

func TestSprintIsPlannedScheduledRatedAndSummarised(t *testing.T) {
	f := newSprintFixture(t)
	ctx := context.Background()
	rec := f.recruiter()
	start := f.now.Add(time.Hour)

	sp, err := f.sprints.Create(ctx, rec, f.input(start))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if sp.Status != service.SprintDraft || sp.Rounds() != 3 || len(sp.Pairings) != 6 {
		t.Fatalf("sprint = status %s, %d rounds, %d pairings", sp.Status, sp.Rounds(), len(sp.Pairings))
	}
	if sp.EndsAt().Sub(sp.StartsAt) != 3*300*time.Second+2*60*time.Second {
		t.Fatalf("duration = %s", sp.EndsAt().Sub(sp.StartsAt))
	}
	// Every candidate meets every interviewer exactly once.
	for _, app := range f.apps {
		mine := sp.PairingsFor(app)
		if len(mine) != 2 || mine[0].InterviewerID == mine[1].InterviewerID {
			t.Fatalf("candidate %s has pairings %+v", app, mine)
		}
	}

	// A stage that is not a sprint stage, or a candidate from elsewhere, is refused.
	bad := f.input(start)
	bad.StageID = f.stages[domain.StageGeneric]
	if _, err := f.sprints.Create(ctx, rec, bad); !errors.Is(err, service.ErrSprintStage) {
		t.Fatalf("generic stage accepted: %v", err)
	}
	bad = f.input(start)
	bad.ApplicationIDs = append(bad.ApplicationIDs, uuid.New())
	if _, err := f.sprints.Create(ctx, rec, bad); !errors.Is(err, service.ErrSprintCandidate) {
		t.Fatalf("unknown candidate accepted: %v", err)
	}
	if _, err := f.sprints.Create(ctx, f.vetter(), f.input(start)); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("a vetter created a sprint: %v", err)
	}

	// Nothing is rated before it is scheduled, and the lobby is closed.
	if _, err := f.sprints.Rate(ctx, rec, sp.PairingsOf(f.userID)[0].ID, service.RatingInput{Score: 4, Recommendation: "yes"}); !errors.Is(err, service.ErrSprintNotScheduled) {
		t.Fatalf("rating a draft: %v", err)
	}

	// Scheduling issues one link per candidate and one invite per candidate.
	before := f.jobs(t, queue.KindEmailSend)
	sp, err = f.sprints.Schedule(ctx, rec, sp.ID)
	if err != nil {
		t.Fatalf("schedule: %v", err)
	}
	if sp.Status != service.SprintScheduled {
		t.Fatalf("status = %s", sp.Status)
	}
	if got := f.jobs(t, queue.KindEmailSend) - before; got != 3 {
		t.Fatalf("invites queued = %d, want 3", got)
	}
	var links int
	if err := f.sys.QueryRow(ctx, `select count(*) from magic_link where purpose = 'sprint' and org_id = $1 and revoked_at is null`, f.orgID).Scan(&links); err != nil {
		t.Fatal(err)
	}
	if links != 3 {
		t.Fatalf("sprint links = %d, want 3", links)
	}
	if _, err := f.sprints.Update(ctx, rec, sp.ID, f.input(start)); !errors.Is(err, service.ErrSprintNotDraft) {
		t.Fatalf("a scheduled sprint was replanned: %v", err)
	}

	// The candidate's lobby shows their own two conversations and nobody else.
	lobby, err := f.sprints.Lobby(ctx, f.candidateLink(t, sp, f.apps[1]))
	if err != nil {
		t.Fatalf("lobby: %v", err)
	}
	if len(lobby.Pairings) != 2 || lobby.ApplicationID != f.apps[1] || len(lobby.Sprint.Candidates) != 0 || lobby.CandidateName != "Bob Builder" {
		t.Fatalf("lobby = %+v", lobby)
	}

	// Start now moves the clock; the first round is on.
	sp, err = f.sprints.StartNow(ctx, rec, sp.ID)
	if err != nil {
		t.Fatalf("start now: %v", err)
	}
	if !sp.StartsAt.Equal(f.now) {
		t.Fatalf("starts at %s, want %s", sp.StartsAt, f.now)
	}
	if state := sp.Clock().At(f.now.Add(10 * time.Second)); state.Phase != domain.PhaseRound || state.Round != 0 {
		t.Fatalf("state = %+v", state)
	}
	if _, err := f.sprints.StartNow(ctx, rec, sp.ID); !errors.Is(err, service.ErrSprintStarted) {
		t.Fatalf("starting twice: %v", err)
	}

	// Ratings: the right interviewer, once the round has started.
	round0 := sp.PairingsOf(f.vetterID)[0]
	round2 := sp.PairingsOf(f.vetterID)[2]
	if _, err := f.sprints.Rate(ctx, rec, round0.ID, service.RatingInput{Score: 4, Recommendation: "yes"}); !errors.Is(err, service.ErrNotInterviewer) {
		t.Fatalf("another interviewer rated: %v", err)
	}
	if _, err := f.sprints.Rate(ctx, f.vetter(), round2.ID, service.RatingInput{Score: 4, Recommendation: "yes"}); !errors.Is(err, service.ErrRatingTooEarly) {
		t.Fatalf("rated a future round: %v", err)
	}
	if _, err := f.sprints.Rate(ctx, f.vetter(), round0.ID, service.RatingInput{Score: 9, Recommendation: "yes"}); !errors.Is(err, service.ErrBadRating) {
		t.Fatalf("score 9 accepted: %v", err)
	}
	rated, err := f.sprints.Rate(ctx, f.vetter(), round0.ID, service.RatingInput{Score: 5, Recommendation: service.OverallStrongYes, Note: "sharp"})
	if err != nil {
		t.Fatalf("rate: %v", err)
	}
	if rated.Rating == nil || rated.Rating.Score != 5 || rated.Rating.Note != "sharp" {
		t.Fatalf("rated = %+v", rated.Rating)
	}
	// Move the clock to the end and rate the rest, unevenly.
	f.now = sp.EndsAt().Add(time.Minute)
	for _, pr := range sp.PairingsOf(f.userID) {
		score := 2
		if pr.ApplicationID == round0.ApplicationID {
			score = 4
		}
		if _, err := f.sprints.Rate(ctx, rec, pr.ID, service.RatingInput{Score: score, Recommendation: service.OverallNo}); err != nil {
			t.Fatalf("rate: %v", err)
		}
	}
	sum, err := f.sprints.Summary(ctx, rec, sp.ID)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if sum.Rows[0].Candidate.ApplicationID != round0.ApplicationID || sum.Rows[0].Mean != 4.5 || sum.Rows[0].StrongYes != 1 {
		t.Fatalf("top row = %+v", sum.Rows[0])
	}
	if sum.Rows[2].Rated != 1 || sum.Rows[2].Mean != 2 {
		t.Fatalf("last row = %+v", sum.Rows[2])
	}
	ratings, err := f.sprints.RatingsFor(ctx, rec, round0.ApplicationID)
	if err != nil || len(ratings) != 2 {
		t.Fatalf("ratings for application = %v, %v", ratings, err)
	}

	// Leaving the sprint stage needs a rating unless overridden.
	unrated := f.apps[2]
	for _, app := range f.apps {
		var n int
		_ = f.sys.QueryRow(ctx, `select count(*) from sprint_rating where application_id = $1`, app).Scan(&n)
		if n == 0 {
			unrated = app
		}
	}
	_ = unrated
	if _, err := f.move(t, rec, f.stages[domain.StageClientReview], service.MoveRequest{}); err != nil {
		t.Fatalf("advancing a rated candidate: %v", err)
	}
	f.appID = f.apps[1]
	var n int
	_ = f.sys.QueryRow(ctx, `select count(*) from sprint_rating where application_id = $1`, f.apps[1]).Scan(&n)
	if n == 0 {
		if _, err := f.move(t, rec, f.stages[domain.StageClientReview], service.MoveRequest{}); !errors.Is(err, domain.ErrPrereqMissing) {
			t.Fatalf("advancing an unrated candidate: %v", err)
		}
	}
}

func TestSprintCancelRevokesLinksAndClosesRooms(t *testing.T) {
	f := newSprintFixture(t)
	ctx := context.Background()
	rec := f.recruiter()
	sp, err := f.sprints.Create(ctx, rec, f.input(f.now.Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	if sp, err = f.sprints.Schedule(ctx, rec, sp.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.sprints.Cancel(ctx, rec, sp.ID); err != nil {
		t.Fatal(err)
	}
	var live int
	if err := f.sys.QueryRow(ctx, `select count(*) from magic_link where purpose = 'sprint' and org_id = $1 and revoked_at is null`, f.orgID).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 0 {
		t.Fatalf("%d links still live after cancel", live)
	}
	if _, err := f.sprints.Lobby(ctx, f.candidateLink(t, sp, f.apps[0])); !errors.Is(err, service.ErrSprintNotScheduled) {
		t.Fatalf("lobby of a cancelled sprint: %v", err)
	}
	if _, err := f.rooms.Open(ctx, rec, service.RoomKey{Kind: service.RoomPairing, ID: sp.Pairings[0].ID}); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("room of a cancelled sprint: %v", err)
	}
}

func TestPairingRoomOpensForItsTwoPeopleDuringItsRound(t *testing.T) {
	f := newSprintFixture(t)
	ctx := context.Background()
	rec := f.recruiter()
	sp, err := f.sprints.Create(ctx, rec, f.input(f.now))
	if err != nil {
		t.Fatal(err)
	}
	if sp, err = f.sprints.Schedule(ctx, rec, sp.ID); err != nil {
		t.Fatal(err)
	}
	pr := sp.PairingsOf(f.vetterID)[1] // round 1
	key := service.RoomKey{Kind: service.RoomPairing, ID: pr.ID}

	// Round 0 is running: the round-1 room describes itself but is shut.
	room, err := f.rooms.Open(ctx, f.vetter(), key)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if room.Open || room.Role != service.RoleInterviewer || room.Round != 1 || room.PairingID != pr.ID {
		t.Fatalf("room = %+v", room)
	}
	if _, _, err := f.rooms.Join(ctx, f.vetter(), key); !errors.Is(err, service.ErrRoomClosed) {
		t.Fatalf("joined a closed room: %v", err)
	}

	f.now = sp.Clock().RoundStart(1).Add(5 * time.Second)
	room, member, err := f.rooms.Join(ctx, f.vetter(), key)
	if err != nil || !room.Open {
		t.Fatalf("join during the round: %v %+v", err, room)
	}
	defer member.Leave()
	hello := <-member.Events
	if hello.Type != service.EventHello {
		t.Fatalf("first event = %s", hello.Type)
	}

	// The candidate of the pairing may join; another candidate may not; a
	// recruiter observes; the other interviewer is refused.
	cand, _, err := f.rooms.Join(ctx, f.candidateLink(t, sp, pr.ApplicationID), key)
	if err != nil || cand.Role != service.RoleCandidate {
		t.Fatalf("candidate join: %v %+v", err, cand)
	}
	var other uuid.UUID
	for _, app := range f.apps {
		if app != pr.ApplicationID {
			other = app
		}
	}
	if _, err := f.rooms.Open(ctx, f.candidateLink(t, sp, other), key); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("another candidate opened the room: %v", err)
	}
	obs, err := f.rooms.Open(ctx, rec, key)
	if err != nil || obs.Role != service.RoleObserver {
		t.Fatalf("recruiter: %v %+v", err, obs)
	}
	joined := <-member.Events
	if joined.Type != service.EventPeerJoined {
		t.Fatalf("after the candidate joined the interviewer saw %s", joined.Type)
	}

	// The editor persists and comes back on the next open.
	if err := f.rooms.SaveCode(ctx, f.vetter(), key, member.Peer.ID, "python", "print('hi')"); err != nil {
		t.Fatalf("save code: %v", err)
	}
	code, err := f.rooms.Code(ctx, rec, key)
	if err != nil || code.Source != "print('hi')" || code.Language != "python" {
		t.Fatalf("code = %+v %v", code, err)
	}
	if err := f.rooms.SaveCode(ctx, f.vetter(), key, member.Peer.ID, "cobol", "x"); !errors.Is(err, service.ErrRoomLanguage) {
		t.Fatalf("unknown language saved: %v", err)
	}
	// Another org's user sees nothing.
	stranger := service.Principal{Kind: service.PrincipalOrgUser, OrgID: uuid.New(), UserID: uuid.New(), Roles: []string{service.RoleAdmin}}
	if _, err := f.rooms.Open(ctx, stranger, key); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("another org opened the room: %v", err)
	}
}
