package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"recruiting/internal/domain"
	"recruiting/internal/mail"
	"recruiting/internal/queue"
	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

// Sprint statuses; they mirror the sprint status check. Live and done are
// not statuses: the clock says which.
const (
	SprintDraft     = "draft"
	SprintScheduled = "scheduled"
	SprintCancelled = "cancelled"
)

// LinkSprint is the magic link purpose a sprint candidate's lobby link
// carries; its subject is the sprint_candidate row.
const LinkSprint = "sprint"

// sprintLobbyPath is where a sprint link lands; the sprint surface serves it.
const sprintLobbyPath = "/sprint/"

// SprintLinkGrace is how long after a sprint ends its links still open the
// thank-you page.
const SprintLinkGrace = 24 * time.Hour

// Sprint recommendations reuse the scorecard's verdicts.
var SprintRecommendations = Overalls

var (
	ErrSprintNotDraft     = errors.New("service: only a draft sprint can be changed")
	ErrSprintNotScheduled = errors.New("service: the sprint is not scheduled")
	ErrSprintEmpty        = errors.New("service: a sprint needs at least one candidate and one interviewer")
	ErrSprintStage        = errors.New("service: a sprint belongs to a sprint stage of its job")
	ErrSprintCandidate    = errors.New("service: every candidate must be an active application on the sprint's job")
	ErrSprintInterviewer  = errors.New("service: every interviewer must be a vetter or recruiter of the org")
	ErrSprintStarted      = errors.New("service: the sprint has already started")
	ErrSprintName         = errors.New("service: a sprint needs a name")
	ErrNotInterviewer     = errors.New("service: only the interviewer of a conversation may rate it")
	ErrRatingTooEarly     = errors.New("service: a conversation cannot be rated before it starts")
	ErrBadRating          = errors.New("service: a rating needs a score from 1 to 5 and a recommendation")
	ErrNotSprintMember    = errors.New("service: you are not part of this sprint")
)

// SprintInput is the setup form: where the sprint belongs, when it runs,
// how long each conversation is, and who takes part.
type SprintInput struct {
	JobID          uuid.UUID
	StageID        uuid.UUID
	Name           string
	StartsAt       time.Time
	RoundSeconds   int
	BreakSeconds   int
	InterviewerIDs []uuid.UUID
	ApplicationIDs []uuid.UUID
}

// SprintInterviewer is one interviewer of a sprint.
type SprintInterviewer struct {
	ID    uuid.UUID
	Name  string
	Email string
}

// SprintCandidate is one candidate of a sprint. ID is the sprint's own row
// for the candidate, which their lobby link addresses.
type SprintCandidate struct {
	ID            uuid.UUID
	ApplicationID uuid.UUID
	Name          string
	Email         string
	Status        string
}

// SprintRating is what an interviewer filed after one conversation.
type SprintRating struct {
	Score          int
	Recommendation string
	Note           string
	RatedAt        time.Time
}

// SprintPairing is one conversation: who meets whom in which round, and
// the rating once filed.
type SprintPairing struct {
	ID              uuid.UUID
	Round           int
	InterviewerID   uuid.UUID
	InterviewerName string
	ApplicationID   uuid.UUID
	CandidateName   string
	CandidateEmail  string
	Rating          *SprintRating
}

// Sprint is one screening sprint with everything the screens read.
type Sprint struct {
	ID           uuid.UUID
	JobID        uuid.UUID
	JobTitle     string
	StageID      uuid.UUID
	StageName    string
	Name         string
	Status       string
	StartsAt     time.Time
	RoundSeconds int
	BreakSeconds int
	CreatedBy    uuid.UUID
	Interviewers []SprintInterviewer
	Candidates   []SprintCandidate
	Pairings     []SprintPairing
}

// Rounds is how many rounds the sprint has.
func (s Sprint) Rounds() int {
	return domain.SprintRounds(len(s.Candidates), len(s.Interviewers))
}

// Clock is the sprint's timetable.
func (s Sprint) Clock() domain.SprintClock {
	return domain.SprintClock{StartsAt: s.StartsAt, Rounds: s.Rounds(), RoundSeconds: s.RoundSeconds, BreakSeconds: s.BreakSeconds}
}

// EndsAt is when the last conversation ends.
func (s Sprint) EndsAt() time.Time { return s.Clock().EndsAt() }

// Duration is how long the whole sprint takes.
func (s Sprint) Duration() time.Duration { return s.EndsAt().Sub(s.StartsAt) }

// PairingsOf is one interviewer's conversations, in round order.
func (s Sprint) PairingsOf(interviewerID uuid.UUID) []SprintPairing {
	out := []SprintPairing{}
	for _, p := range s.Pairings {
		if p.InterviewerID == interviewerID {
			out = append(out, p)
		}
	}
	return out
}

// PairingsFor is one candidate's conversations, in round order.
func (s Sprint) PairingsFor(applicationID uuid.UUID) []SprintPairing {
	out := []SprintPairing{}
	for _, p := range s.Pairings {
		if p.ApplicationID == applicationID {
			out = append(out, p)
		}
	}
	return out
}

// Pairing finds one conversation.
func (s Sprint) Pairing(id uuid.UUID) (SprintPairing, bool) {
	for _, p := range s.Pairings {
		if p.ID == id {
			return p, true
		}
	}
	return SprintPairing{}, false
}

// IsInterviewer reports whether the user interviews in the sprint.
func (s Sprint) IsInterviewer(userID uuid.UUID) bool {
	for _, i := range s.Interviewers {
		if i.ID == userID {
			return true
		}
	}
	return false
}

// SprintSummaryRow is one candidate of a finished (or running) sprint as
// the summary ranks them: the mean score and each interviewer's rating.
type SprintSummaryRow struct {
	Candidate SprintCandidate
	Ratings   []SprintPairing // one per interviewer, in interviewer order; Rating nil when unfiled
	Rated     int
	Mean      float64
	StrongYes int
	Yes       int
	No        int
	StrongNo  int
}

// SprintSummary is the sprint ranked for the recruiter's decision.
type SprintSummary struct {
	Sprint Sprint
	Rows   []SprintSummaryRow
}

// SprintListItem is one sprint in a list.
type SprintListItem struct {
	ID           uuid.UUID
	JobID        uuid.UUID
	JobTitle     string
	StageID      uuid.UUID
	StageName    string
	Name         string
	Status       string
	StartsAt     time.Time
	RoundSeconds int
	BreakSeconds int
	Candidates   int
	Interviewers int
}

// Rounds is how many rounds the sprint takes.
func (i SprintListItem) Rounds() int { return domain.SprintRounds(i.Candidates, i.Interviewers) }

// Clock is the sprint's timetable.
func (i SprintListItem) Clock() domain.SprintClock {
	return domain.SprintClock{StartsAt: i.StartsAt, Rounds: i.Rounds(), RoundSeconds: i.RoundSeconds, BreakSeconds: i.BreakSeconds}
}

// RatingInput is the rating card.
type RatingInput struct {
	Score          int
	Recommendation string
	Note           string
}

// SprintLobby is what a candidate sees: their own conversations and nothing
// about the other candidates.
type SprintLobby struct {
	Sprint        Sprint // Candidates and other candidates' pairings stripped
	CandidateID   uuid.UUID
	ApplicationID uuid.UUID
	CandidateName string
	Pairings      []SprintPairing
}

// ApplicationSprintRating is one rating as the application page lists them.
type ApplicationSprintRating struct {
	SprintID        uuid.UUID
	SprintName      string
	InterviewerName string
	Score           int
	Recommendation  string
	Note            string
	RatedAt         time.Time
}

// SprintService plans, schedules, and scores screening sprints.
type SprintService struct {
	st      *store.Store
	q       *queue.Client
	baseURL string
	// Now is the clock; tests move it.
	Now func() time.Time
}

// NewSprintService wires the store, the queue invites go to, and the base
// URL lobby links are built on. A nil queue sends nothing.
func NewSprintService(st *store.Store, q *queue.Client, baseURL string) *SprintService {
	return &SprintService{st: st, q: q, baseURL: strings.TrimRight(baseURL, "/"), Now: time.Now}
}

// Create plans a sprint as a draft: nothing is sent until it is scheduled.
func (s *SprintService) Create(ctx context.Context, p Principal, in SprintInput) (Sprint, error) {
	if err := requireRecruiter(p); err != nil {
		return Sprint{}, err
	}
	in, err := cleanSprint(in)
	if err != nil {
		return Sprint{}, err
	}
	var out Sprint
	err = s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		if err := s.checkSprintInput(ctx, tx, p, in); err != nil {
			return err
		}
		row, err := tx.Q.CreateSprint(ctx, db.CreateSprintParams{
			OrgID: p.OrgID, JobID: in.JobID, StageID: in.StageID, Name: in.Name, StartsAt: ts(in.StartsAt),
			RoundSeconds: int32(in.RoundSeconds), BreakSeconds: int32(in.BreakSeconds),
			CreatedBy: uuid.NullUUID{UUID: p.UserID, Valid: p.UserID != uuid.Nil},
		})
		if err != nil {
			return err
		}
		if err := s.writeParticipants(ctx, tx, p.OrgID, row.ID, in); err != nil {
			return err
		}
		out, err = loadSprint(ctx, tx, row.ID)
		return err
	})
	if err != nil {
		return Sprint{}, wrapSprint("create sprint", err)
	}
	return out, nil
}

// Update replaces a draft's timing and participants and plans it again.
func (s *SprintService) Update(ctx context.Context, p Principal, id uuid.UUID, in SprintInput) (Sprint, error) {
	if err := requireRecruiter(p); err != nil {
		return Sprint{}, err
	}
	in, err := cleanSprint(in)
	if err != nil {
		return Sprint{}, err
	}
	var out Sprint
	err = s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		row, err := tx.Q.GetSprintForUpdate(ctx, id)
		if err != nil {
			return err
		}
		if row.Status != SprintDraft {
			return ErrSprintNotDraft
		}
		in.JobID, in.StageID = row.JobID, row.StageID
		if err := s.checkSprintInput(ctx, tx, p, in); err != nil {
			return err
		}
		if _, err := tx.Q.UpdateSprintDraft(ctx, db.UpdateSprintDraftParams{
			ID: id, Name: in.Name, StartsAt: ts(in.StartsAt),
			RoundSeconds: int32(in.RoundSeconds), BreakSeconds: int32(in.BreakSeconds),
		}); err != nil {
			return err
		}
		if err := s.writeParticipants(ctx, tx, p.OrgID, id, in); err != nil {
			return err
		}
		out, err = loadSprint(ctx, tx, id)
		return err
	})
	if err != nil {
		return Sprint{}, wrapSprint("update sprint", err)
	}
	return out, nil
}

// checkSprintInput refuses a stage that is not a sprint stage of the job,
// a candidate that is not an active application of the job, and an
// interviewer who cannot interview.
func (s *SprintService) checkSprintInput(ctx context.Context, tx *store.Tx, p Principal, in SprintInput) error {
	stages, err := listStages(ctx, tx, in.JobID)
	if err != nil {
		return err
	}
	stage, ok := findStage(stages, in.StageID)
	if !ok || stage.Kind != domain.StageSprint {
		return ErrSprintStage
	}
	for _, id := range in.ApplicationIDs {
		app, err := tx.Q.GetApplicationCard(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrSprintCandidate
		}
		if err != nil {
			return err
		}
		if app.JobID != in.JobID || app.Status != string(domain.StatusActive) {
			return ErrSprintCandidate
		}
	}
	for _, id := range in.InterviewerIDs {
		if _, err := tx.Q.GetOrgUser(ctx, db.GetOrgUserParams{ID: id, OrgID: p.OrgID}); errors.Is(err, pgx.ErrNoRows) {
			return ErrSprintInterviewer
		} else if err != nil {
			return err
		}
		roles, err := tx.Q.ListOrgUserRoles(ctx, id)
		if err != nil {
			return err
		}
		if pr := (Principal{Roles: roles}); !pr.HasRole(RoleVetter) && !pr.HasRole(RoleRecruiter) && !pr.HasRole(RoleAdmin) {
			return ErrSprintInterviewer
		}
	}
	return nil
}

// writeParticipants replaces the interviewers, candidates, and the plan.
func (s *SprintService) writeParticipants(ctx context.Context, tx *store.Tx, orgID, sprintID uuid.UUID, in SprintInput) error {
	if err := tx.Q.DeleteSprintPairings(ctx, sprintID); err != nil {
		return err
	}
	if err := tx.Q.DeleteSprintInterviewers(ctx, sprintID); err != nil {
		return err
	}
	if err := tx.Q.DeleteSprintCandidates(ctx, sprintID); err != nil {
		return err
	}
	for i, id := range in.InterviewerIDs {
		if err := tx.Q.AddSprintInterviewer(ctx, db.AddSprintInterviewerParams{SprintID: sprintID, OrgID: orgID, UserID: id, Position: int32(i)}); err != nil {
			return err
		}
	}
	for i, id := range in.ApplicationIDs {
		if _, err := tx.Q.AddSprintCandidate(ctx, db.AddSprintCandidateParams{SprintID: sprintID, OrgID: orgID, ApplicationID: id, Position: int32(i)}); err != nil {
			return err
		}
	}
	for _, pair := range domain.PlanRounds(in.ApplicationIDs, in.InterviewerIDs) {
		if _, err := tx.Q.CreateSprintPairing(ctx, db.CreateSprintPairingParams{
			SprintID: sprintID, OrgID: orgID, Round: int32(pair.Round), InterviewerID: pair.InterviewerID, ApplicationID: pair.ApplicationID,
		}); err != nil {
			return err
		}
	}
	return nil
}

// Get loads a sprint. Any org user may read one; the console refuses
// non-members itself.
func (s *SprintService) Get(ctx context.Context, p Principal, id uuid.UUID) (Sprint, error) {
	if p.Kind != PrincipalOrgUser {
		return Sprint{}, ErrForbidden
	}
	var out Sprint
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		var err error
		out, err = loadSprint(ctx, tx, id)
		return err
	})
	if err != nil {
		return Sprint{}, wrapSprint("get sprint", err)
	}
	return out, nil
}

// Console is the sprint as one interviewer, recruiter, or admin opens it.
// Anyone else is refused.
func (s *SprintService) Console(ctx context.Context, p Principal, id uuid.UUID) (Sprint, error) {
	sp, err := s.Get(ctx, p, id)
	if err != nil {
		return Sprint{}, err
	}
	if !sp.IsInterviewer(p.UserID) && !p.HasRole(RoleRecruiter) && !p.HasRole(RoleAdmin) {
		return Sprint{}, ErrNotSprintMember
	}
	return sp, nil
}

// List is every sprint of the org, newest start first.
func (s *SprintService) List(ctx context.Context, p Principal) ([]SprintListItem, error) {
	if p.Kind != PrincipalOrgUser {
		return nil, ErrForbidden
	}
	var out []SprintListItem
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		rows, err := tx.Q.ListSprints(ctx, p.OrgID)
		if err != nil {
			return err
		}
		out = make([]SprintListItem, 0, len(rows))
		for _, r := range rows {
			out = append(out, SprintListItem{
				ID: r.ID, JobID: r.JobID, JobTitle: r.JobTitle, StageID: r.StageID, StageName: r.StageName,
				Name: r.Name, Status: r.Status, StartsAt: r.StartsAt.Time.UTC(),
				RoundSeconds: int(r.RoundSeconds), BreakSeconds: int(r.BreakSeconds),
				Candidates: int(r.Candidates), Interviewers: int(r.Interviewers),
			})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list sprints: %w", err)
	}
	return out, nil
}

// ForInterviewer is the scheduled sprints the signed-in user interviews in,
// soonest first, for their interviews screen.
func (s *SprintService) ForInterviewer(ctx context.Context, p Principal) ([]SprintListItem, error) {
	if p.Kind != PrincipalOrgUser {
		return nil, ErrForbidden
	}
	var out []SprintListItem
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		rows, err := tx.Q.ListSprintsForInterviewer(ctx, p.UserID)
		if err != nil {
			return err
		}
		out = make([]SprintListItem, 0, len(rows))
		for _, r := range rows {
			sp, err := loadSprint(ctx, tx, r.ID)
			if err != nil {
				return err
			}
			out = append(out, SprintListItem{
				ID: sp.ID, JobID: sp.JobID, JobTitle: sp.JobTitle, StageID: sp.StageID, StageName: sp.StageName,
				Name: sp.Name, Status: sp.Status, StartsAt: sp.StartsAt,
				RoundSeconds: sp.RoundSeconds, BreakSeconds: sp.BreakSeconds,
				Candidates: len(sp.Candidates), Interviewers: len(sp.Interviewers),
			})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list sprints for interviewer: %w", err)
	}
	return out, nil
}

// Schedule fixes a draft: every candidate gets a lobby link by email, and
// the sprint runs on its clock from then on.
func (s *SprintService) Schedule(ctx context.Context, p Principal, id uuid.UUID) (Sprint, error) {
	if err := requireRecruiter(p); err != nil {
		return Sprint{}, err
	}
	var out Sprint
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		row, err := tx.Q.GetSprintForUpdate(ctx, id)
		if err != nil {
			return err
		}
		if row.Status != SprintDraft {
			return ErrSprintNotDraft
		}
		sp, err := loadSprint(ctx, tx, id)
		if err != nil {
			return err
		}
		if len(sp.Candidates) == 0 || len(sp.Interviewers) == 0 {
			return ErrSprintEmpty
		}
		if _, err := tx.Q.SetSprintStatus(ctx, db.SetSprintStatusParams{ID: id, Status: SprintScheduled}); err != nil {
			return err
		}
		if err := s.invite(ctx, tx, p.OrgID, sp); err != nil {
			return err
		}
		out, err = loadSprint(ctx, tx, id)
		return err
	})
	if err != nil {
		return Sprint{}, wrapSprint("schedule sprint", err)
	}
	return out, nil
}

// invite issues one link per candidate and queues their invitation.
func (s *SprintService) invite(ctx context.Context, tx *store.Tx, orgID uuid.UUID, sp Sprint) error {
	if err := tx.Q.RevokeSprintLinks(ctx, sp.ID); err != nil {
		return err
	}
	expires := sp.EndsAt().Add(SprintLinkGrace)
	for _, c := range sp.Candidates {
		token, hash, err := newToken()
		if err != nil {
			return err
		}
		if _, err := tx.Q.CreateMagicLink(ctx, db.CreateMagicLinkParams{
			OrgID: orgID, TokenHash: hash, Purpose: LinkSprint, SubjectID: c.ID, ExpiresAt: ts(expires),
		}); err != nil {
			return err
		}
		if s.q == nil {
			continue
		}
		mine := sp.PairingsFor(c.ApplicationID)
		if err := enqueued(s.q.Enqueue(ctx, tx, queue.KindEmailSend, queue.EmailPayload{
			Template: mail.TemplateSprintInvite, To: c.Email, OrgID: orgID,
			Data: map[string]any{
				"CandidateName": c.Name,
				"JobTitle":      sp.JobTitle,
				"StartsAt":      sp.StartsAt.UTC().Format("Mon 2 Jan 2006 15:04 UTC"),
				"Conversations": len(mine),
				"Minutes":       roundMinutes(sp.RoundSeconds),
				"LobbyURL":      s.baseURL + sprintLobbyPath + token,
			},
		})); err != nil {
			return err
		}
	}
	return nil
}

// roundMinutes reads a round length in whole minutes for an email, never
// rounding a short round down to nothing.
func roundMinutes(seconds int) int {
	if seconds < 60 {
		return 1
	}
	return (seconds + 30) / 60
}

// StartNow moves a scheduled sprint's start to this minute, for a recruiter
// with everyone already in the lobby.
func (s *SprintService) StartNow(ctx context.Context, p Principal, id uuid.UUID) (Sprint, error) {
	if err := requireRecruiter(p); err != nil {
		return Sprint{}, err
	}
	var out Sprint
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		row, err := tx.Q.GetSprintForUpdate(ctx, id)
		if err != nil {
			return err
		}
		if row.Status != SprintScheduled {
			return ErrSprintNotScheduled
		}
		now := s.Now()
		if !now.Before(row.StartsAt.Time) {
			return ErrSprintStarted
		}
		if _, err := tx.Q.SetSprintStartsAt(ctx, db.SetSprintStartsAtParams{ID: id, StartsAt: ts(now)}); err != nil {
			return err
		}
		out, err = loadSprint(ctx, tx, id)
		return err
	})
	if err != nil {
		return Sprint{}, wrapSprint("start sprint", err)
	}
	return out, nil
}

// Cancel voids the sprint and every candidate's link.
func (s *SprintService) Cancel(ctx context.Context, p Principal, id uuid.UUID) error {
	if err := requireRecruiter(p); err != nil {
		return err
	}
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		if _, err := tx.Q.GetSprintForUpdate(ctx, id); err != nil {
			return err
		}
		if _, err := tx.Q.SetSprintStatus(ctx, db.SetSprintStatusParams{ID: id, Status: SprintCancelled}); err != nil {
			return err
		}
		return tx.Q.RevokeSprintLinks(ctx, id)
	})
	if err != nil {
		return wrapSprint("cancel sprint", err)
	}
	return nil
}

// Rate files the interviewer's rating of one conversation, or rewrites it.
// Only the conversation's own interviewer may, and not before it starts.
func (s *SprintService) Rate(ctx context.Context, p Principal, pairingID uuid.UUID, in RatingInput) (SprintPairing, error) {
	if p.Kind != PrincipalOrgUser {
		return SprintPairing{}, ErrForbidden
	}
	in.Note = strings.TrimSpace(in.Note)
	in.Recommendation = strings.TrimSpace(in.Recommendation)
	if in.Score < ScoreMin || in.Score > ScoreMax || !containsString(SprintRecommendations, in.Recommendation) {
		return SprintPairing{}, ErrBadRating
	}
	var out SprintPairing
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		pair, err := tx.Q.GetSprintPairing(ctx, pairingID)
		if err != nil {
			return err
		}
		if pair.InterviewerID != p.UserID {
			return ErrNotInterviewer
		}
		sp, err := loadSprint(ctx, tx, pair.SprintID)
		if err != nil {
			return err
		}
		if sp.Status != SprintScheduled {
			return ErrSprintNotScheduled
		}
		if s.Now().Before(sp.Clock().RoundStart(int(pair.Round))) {
			return ErrRatingTooEarly
		}
		if _, err := tx.Q.UpsertSprintRating(ctx, db.UpsertSprintRatingParams{
			OrgID: p.OrgID, SprintID: pair.SprintID, PairingID: pair.ID, InterviewerID: p.UserID, ApplicationID: pair.ApplicationID,
			Score: int16(in.Score), Recommendation: in.Recommendation, Note: in.Note,
		}); err != nil {
			return err
		}
		sp, err = loadSprint(ctx, tx, pair.SprintID)
		if err != nil {
			return err
		}
		out, _ = sp.Pairing(pairingID)
		return nil
	})
	if err != nil {
		return SprintPairing{}, wrapSprint("rate", err)
	}
	return out, nil
}

// Summary ranks the sprint's candidates by mean score, best first; ties
// break on the count of strong yeses, then the name.
func (s *SprintService) Summary(ctx context.Context, p Principal, id uuid.UUID) (SprintSummary, error) {
	sp, err := s.Get(ctx, p, id)
	if err != nil {
		return SprintSummary{}, err
	}
	return summarize(sp), nil
}

func summarize(sp Sprint) SprintSummary {
	out := SprintSummary{Sprint: sp}
	for _, c := range sp.Candidates {
		row := SprintSummaryRow{Candidate: c}
		total := 0
		for _, i := range sp.Interviewers {
			var found SprintPairing
			for _, pr := range sp.Pairings {
				if pr.ApplicationID == c.ApplicationID && pr.InterviewerID == i.ID {
					found = pr
					break
				}
			}
			row.Ratings = append(row.Ratings, found)
			if found.Rating == nil {
				continue
			}
			row.Rated++
			total += found.Rating.Score
			switch found.Rating.Recommendation {
			case OverallStrongYes:
				row.StrongYes++
			case OverallYes:
				row.Yes++
			case OverallNo:
				row.No++
			case OverallStrongNo:
				row.StrongNo++
			}
		}
		if row.Rated > 0 {
			row.Mean = float64(total) / float64(row.Rated)
		}
		out.Rows = append(out.Rows, row)
	}
	sort.SliceStable(out.Rows, func(i, j int) bool {
		a, b := out.Rows[i], out.Rows[j]
		if (a.Rated == 0) != (b.Rated == 0) {
			return a.Rated > 0
		}
		if a.Mean != b.Mean {
			return a.Mean > b.Mean
		}
		if a.StrongYes != b.StrongYes {
			return a.StrongYes > b.StrongYes
		}
		return a.Candidate.Name < b.Candidate.Name
	})
	return out
}

// Lobby is the candidate's side of a scheduled sprint, through their link.
func (s *SprintService) Lobby(ctx context.Context, p Principal) (SprintLobby, error) {
	if p.Kind != PrincipalMagicLink || p.MagicPurpose != LinkSprint {
		return SprintLobby{}, ErrForbidden
	}
	var out SprintLobby
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		c, err := tx.Q.GetSprintCandidate(ctx, p.SubjectID)
		if err != nil {
			return err
		}
		sp, err := loadSprint(ctx, tx, c.SprintID)
		if err != nil {
			return err
		}
		if sp.Status != SprintScheduled {
			return ErrSprintNotScheduled
		}
		out = SprintLobby{CandidateID: c.ID, ApplicationID: c.ApplicationID, CandidateName: c.CandidateName, Pairings: sp.PairingsFor(c.ApplicationID)}
		// The other candidates are none of this candidate's business.
		sp.Candidates = nil
		sp.Pairings = nil
		out.Sprint = sp
		return nil
	})
	if err != nil {
		return SprintLobby{}, wrapSprint("lobby", err)
	}
	return out, nil
}

// RatingsFor is every sprint rating filed on an application, for its page.
func (s *SprintService) RatingsFor(ctx context.Context, p Principal, applicationID uuid.UUID) ([]ApplicationSprintRating, error) {
	if p.Kind != PrincipalOrgUser {
		return nil, ErrForbidden
	}
	var out []ApplicationSprintRating
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		rows, err := tx.Q.ListSprintRatingsForApplication(ctx, applicationID)
		if err != nil {
			return err
		}
		out = make([]ApplicationSprintRating, 0, len(rows))
		for _, r := range rows {
			out = append(out, ApplicationSprintRating{
				SprintID: r.SprintID, SprintName: r.SprintName, InterviewerName: r.InterviewerName,
				Score: int(r.Score), Recommendation: r.Recommendation, Note: r.Note, RatedAt: r.UpdatedAt.Time.UTC(),
			})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("sprint ratings: %w", err)
	}
	return out, nil
}

// loadSprint reads a sprint with everyone in it.
func loadSprint(ctx context.Context, tx *store.Tx, id uuid.UUID) (Sprint, error) {
	row, err := tx.Q.GetSprint(ctx, id)
	if err != nil {
		return Sprint{}, err
	}
	job, err := tx.Q.GetJob(ctx, row.JobID)
	if err != nil {
		return Sprint{}, err
	}
	stage, err := tx.Q.GetStage(ctx, row.StageID)
	if err != nil {
		return Sprint{}, err
	}
	out := Sprint{
		ID: row.ID, JobID: row.JobID, JobTitle: job.Title, StageID: row.StageID, StageName: stage.Name,
		Name: row.Name, Status: row.Status, StartsAt: row.StartsAt.Time.UTC(),
		RoundSeconds: int(row.RoundSeconds), BreakSeconds: int(row.BreakSeconds), CreatedBy: row.CreatedBy.UUID,
	}
	interviewers, err := tx.Q.ListSprintInterviewers(ctx, id)
	if err != nil {
		return Sprint{}, err
	}
	for _, i := range interviewers {
		out.Interviewers = append(out.Interviewers, SprintInterviewer{ID: i.UserID, Name: i.Name, Email: i.Email})
	}
	candidates, err := tx.Q.ListSprintCandidates(ctx, id)
	if err != nil {
		return Sprint{}, err
	}
	for _, c := range candidates {
		out.Candidates = append(out.Candidates, SprintCandidate{
			ID: c.ID, ApplicationID: c.ApplicationID, Name: c.CandidateName, Email: c.CandidateEmail, Status: c.ApplicationStatus,
		})
	}
	pairings, err := tx.Q.ListSprintPairings(ctx, id)
	if err != nil {
		return Sprint{}, err
	}
	for _, pr := range pairings {
		pairing := SprintPairing{
			ID: pr.ID, Round: int(pr.Round), InterviewerID: pr.InterviewerID, InterviewerName: pr.InterviewerName,
			ApplicationID: pr.ApplicationID, CandidateName: pr.CandidateName, CandidateEmail: pr.CandidateEmail,
		}
		if pr.Score != nil && pr.Recommendation != nil {
			pairing.Rating = &SprintRating{Score: int(*pr.Score), Recommendation: *pr.Recommendation, Note: deref(pr.Note), RatedAt: pr.RatedAt.Time.UTC()}
		}
		out.Pairings = append(out.Pairings, pairing)
	}
	return out, nil
}

func cleanSprint(in SprintInput) (SprintInput, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return in, ErrSprintName
	}
	if in.RoundSeconds == 0 {
		in.RoundSeconds = domain.DefaultRoundSeconds
	}
	if in.RoundSeconds < domain.MinRoundSeconds || in.RoundSeconds > domain.MaxRoundSeconds || in.BreakSeconds < 0 || in.BreakSeconds > domain.MaxBreakSeconds {
		return in, fmt.Errorf("%w: rounds must last between %d seconds and an hour, breaks between zero and an hour", domain.ErrInvalidPipeline, domain.MinRoundSeconds)
	}
	in.InterviewerIDs = dedupe(in.InterviewerIDs)
	in.ApplicationIDs = dedupe(in.ApplicationIDs)
	if len(in.InterviewerIDs) == 0 || len(in.ApplicationIDs) == 0 {
		return in, ErrSprintEmpty
	}
	in.StartsAt = in.StartsAt.UTC().Truncate(time.Second)
	return in, nil
}

func dedupe(ids []uuid.UUID) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(ids))
	seen := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		if id == uuid.Nil || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func wrapSprint(what string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pgx.ErrNoRows):
		return ErrNotFound
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrForbidden),
		errors.Is(err, ErrSprintNotDraft), errors.Is(err, ErrSprintNotScheduled), errors.Is(err, ErrSprintEmpty),
		errors.Is(err, ErrSprintStage), errors.Is(err, ErrSprintCandidate), errors.Is(err, ErrSprintInterviewer),
		errors.Is(err, ErrSprintStarted), errors.Is(err, ErrSprintName), errors.Is(err, ErrNotInterviewer),
		errors.Is(err, ErrRatingTooEarly), errors.Is(err, ErrBadRating), errors.Is(err, ErrNotSprintMember),
		errors.Is(err, domain.ErrInvalidPipeline):
		return err
	}
	return fmt.Errorf("%s: %w", what, err)
}
