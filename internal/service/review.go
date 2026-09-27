package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"recruiting/internal/domain/signals"
	"recruiting/internal/runner/server"
	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

// The verdicts a vetter closes an assessment attempt with. They mirror the
// review.verdict check, and the client portal shows the same word.
const (
	VerdictPass       = "pass"
	VerdictBorderline = "borderline"
	VerdictFail       = "fail"
)

// Verdicts are the verdicts in the order a form offers them, strongest first.
var Verdicts = []string{VerdictPass, VerdictBorderline, VerdictFail}

var (
	ErrBadVerdict  = errors.New("service: that is not one of the review verdicts")
	ErrReviewFiled = errors.New("service: another vetter has already reviewed this attempt")
	ErrNotScored   = errors.New("service: the attempt has not been scored yet")
)

// Review is the vetter's verdict on one attempt.
type Review struct {
	ID         uuid.UUID
	AttemptID  uuid.UUID
	VetterID   uuid.UUID
	VetterName string
	Verdict    string
	Notes      string
	CreatedAt  time.Time
}

// ReviewInput is one filed verdict.
type ReviewInput struct {
	AttemptID uuid.UUID
	Verdict   string
	Notes     string
}

// ReviewAssignment is one attempt waiting on the signed-in vetter.
type ReviewAssignment struct {
	AttemptID       uuid.UUID
	ApplicationID   uuid.UUID
	StageID         uuid.UUID
	CandidateName   string
	CandidateEmail  string
	JobTitle        string
	StageName       string
	Status          string
	Score           *float64
	RiskScore       *float64
	ErrorCount      int
	RecordingStatus string
	FinishedAt      time.Time
	Verdict         string
	Reviewed        bool
}

// AttemptSignal is one stored integrity signal with the evidence behind it.
type AttemptSignal struct {
	Name       string
	Value      float64
	Weight     float64
	Confidence string
	Evidence   []signals.Evidence
}

// ReviewProblem is one problem of the attempt with what it scored, the code
// it was scored on, and how each case went.
type ReviewProblem struct {
	ID         uuid.UUID
	Title      string
	Difficulty string
	Score      ProblemScore
	Language   string
	// FinalSource is the code of the last submit, or the last text the
	// editor synced when the problem was never submitted.
	FinalSource string
	Tests       []ReviewTest
}

// ReviewTest is one case of a problem's scored submit. The reviewer sees the
// hidden cases go by; their inputs and expected outputs stay server-side.
type ReviewTest struct {
	Position   int
	Name       string
	Class      string
	Visibility string
	Status     string
	TimeMs     int64
	Weight     float64
}

// ReviewSubmission is one run or submit as the reviewer sees it.
type ReviewSubmission struct {
	ID           uuid.UUID
	ProblemID    uuid.UUID
	ProblemTitle string
	Kind         string
	Language     string
	Status       string
	Score        *float64
	CreatedAt    time.Time
	// RunnerStatus is the runner's own verdict, empty while the submission
	// is still queued or the runner never answered.
	RunnerStatus string
	TestsPassed  int
	TestsTotal   int
	TimedOut     bool
}

// AttemptReview is the review screen: the score, the breakdown, the
// integrity signals with their evidence, the submissions, and the verdict if
// one is filed.
type AttemptReview struct {
	AttemptID       uuid.UUID
	ApplicationID   uuid.UUID
	StageID         uuid.UUID
	CandidateName   string
	JobTitle        string
	StageName       string
	Status          string
	Score           *float64
	RiskScore       *float64
	ErrorCount      int
	RecordingStatus string
	// RecordingTruncated is set when the sitting produced more events than
	// one attempt may keep, so the replay stops before the candidate did.
	RecordingTruncated bool
	StartedAt          time.Time
	FinishedAt         time.Time
	Problems           []ReviewProblem
	ProblemScores      []ProblemScore
	Signals            []AttemptSignal
	Submissions        []ReviewSubmission
	Review             *Review
	// CanReview is whether the caller may file the verdict. A recruiter or
	// an admin reads the screen; only the assigned vetter judges it.
	CanReview bool
}

// AttemptSummary is one attempt's outcome on an application: what the
// recruiter's application page shows without opening the review screen.
type AttemptSummary struct {
	AttemptID   uuid.UUID
	StageID     uuid.UUID
	StageName   string
	Status      string
	Score       *float64
	RiskScore   *float64
	ErrorCount  int
	FinishedAt  time.Time
	Verdict     string
	ReviewNotes string
	VetterName  string
	ReviewedAt  time.Time
}

// ReviewService is the vetter's side of an assessment: the queue of attempts
// waiting on them, what each one recorded, and the verdict they file. A
// passing verdict at or above the org's threshold hands the candidate to the
// talent pool in the same transaction as the verdict.
type ReviewService struct {
	st   *store.Store
	pool *PoolService
	blob BlobReader
	// Logger notes a recording read from Postgres instead of object storage.
	// Nil disables it.
	Logger *slog.Logger
}

// NewReviewService wires the store, the pool the passing verdicts feed, and
// object storage the recordings are read from. A nil pool files no entries;
// a nil reader replays from the attempt_event rows.
func NewReviewService(st *store.Store, pool *PoolService, b BlobReader) *ReviewService {
	return &ReviewService{st: st, pool: pool, blob: b}
}

// Assignments are the closed attempts the signed-in vetter owns, newest
// first, each with their verdict if it is in.
func (s *ReviewService) Assignments(ctx context.Context, p Principal) ([]ReviewAssignment, error) {
	if err := requireVetter(p); err != nil {
		return nil, err
	}
	var out []ReviewAssignment
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		rows, err := tx.Q.ListVetterAttemptReviews(ctx, uuid.NullUUID{UUID: p.UserID, Valid: true})
		if err != nil {
			return err
		}
		out = make([]ReviewAssignment, 0, len(rows))
		for _, r := range rows {
			out = append(out, ReviewAssignment{
				AttemptID: r.AttemptID, ApplicationID: r.ApplicationID, StageID: r.StageID,
				CandidateName: r.CandidateName, CandidateEmail: r.CandidateEmail,
				JobTitle: r.JobTitle, StageName: r.StageName, Status: r.Status,
				Score: numericPtr(r.Score), RiskScore: numericPtr(r.RiskScore),
				ErrorCount: int(r.ErrorCount), RecordingStatus: r.RecordingStatus,
				FinishedAt: r.FinishedAt.Time.UTC(), Verdict: deref(r.Verdict), Reviewed: r.Verdict != nil,
			})
		}
		return nil
	})
	if err != nil {
		return nil, wrapReview("review queue", err)
	}
	return out, nil
}

// Attempt is the review screen for one attempt.
func (s *ReviewService) Attempt(ctx context.Context, p Principal, attemptID uuid.UUID) (AttemptReview, error) {
	var out AttemptReview
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		att, app, err := s.reviewable(ctx, tx, p, attemptID)
		if err != nil {
			return err
		}
		card, err := tx.Q.GetApplicationCard(ctx, att.ApplicationID.UUID)
		if err != nil {
			return err
		}
		stage, err := tx.Q.GetStage(ctx, att.StageID.UUID)
		if err != nil {
			return err
		}
		canReview := requireVetter(p) == nil && requireAssigned(app, stage, p) == nil
		out = AttemptReview{
			AttemptID: att.ID, ApplicationID: att.ApplicationID.UUID, StageID: att.StageID.UUID,
			CandidateName: card.CandidateName, JobTitle: card.JobTitle, StageName: stage.Name,
			Status: att.Status, Score: numericPtr(att.Score), RiskScore: numericPtr(att.RiskScore),
			ErrorCount: int(att.ErrorCount), RecordingStatus: att.RecordingStatus, RecordingTruncated: att.RecordingTruncated,
			StartedAt: att.StartedAt.Time.UTC(), FinishedAt: att.FinishedAt.Time.UTC(),
			CanReview: canReview,
		}
		if err := json.Unmarshal(att.ProblemScores, &out.ProblemScores); err != nil {
			return fmt.Errorf("problem scores of attempt %s: %w", att.ID, err)
		}
		a, err := loadAssessment(ctx, tx, att.AssessmentID)
		if err != nil {
			return err
		}
		scored := make(map[uuid.UUID]ProblemScore, len(out.ProblemScores))
		for _, ps := range out.ProblemScores {
			scored[ps.ProblemID] = ps
		}
		titles := make(map[uuid.UUID]string, len(a.Problems))
		byID := make(map[uuid.UUID]Problem, len(a.Problems))
		for _, problem := range a.Problems {
			titles[problem.ID] = problem.Title
			byID[problem.ID] = problem
			out.Problems = append(out.Problems, ReviewProblem{
				ID: problem.ID, Title: problem.Title, Difficulty: problem.Difficulty, Score: scored[problem.ID],
			})
		}
		if out.Signals, err = attemptSignals(ctx, tx, attemptID); err != nil {
			return err
		}
		subs, err := tx.Q.ListSubmissions(ctx, attemptID)
		if err != nil {
			return err
		}
		for _, sub := range subs {
			rs := ReviewSubmission{
				ID: sub.ID, ProblemID: sub.ProblemID, ProblemTitle: titles[sub.ProblemID], Kind: sub.Kind,
				Language: sub.Language, Status: sub.Status, Score: numericPtr(sub.Score),
				CreatedAt: sub.CreatedAt.Time.UTC(),
			}
			rs.RunnerStatus, rs.TestsPassed, rs.TestsTotal, rs.TimedOut = submissionOutcome(sub.Result)
			out.Submissions = append(out.Submissions, rs)
		}
		sources, err := tx.Q.ListAttemptSources(ctx, attemptID)
		if err != nil {
			return err
		}
		for i := range out.Problems {
			problem := byID[out.Problems[i].ID]
			out.Problems[i].Language, out.Problems[i].FinalSource = finalSource(problem.ID, subs, sources)
			out.Problems[i].Tests = reviewTests(scoredSubmit(problem.ID, subs), problem.TestCases)
		}
		filed, err := tx.Q.GetReviewForAttempt(ctx, attemptID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return nil
		case err != nil:
			return err
		}
		out.Review = &Review{
			ID: filed.ID, AttemptID: filed.AttemptID, VetterID: filed.VetterID, VetterName: filed.VetterName,
			Verdict: filed.Verdict, Notes: deref(filed.Notes), CreatedAt: filed.CreatedAt.Time.UTC(),
		}
		return nil
	})
	if err != nil {
		return AttemptReview{}, wrapReview("attempt review", err)
	}
	return out, nil
}

// Save files the vetter's verdict. There is one review per attempt: the
// vetter who filed it may amend it, nobody else may overwrite it. A pass at
// or above the org's pool threshold files the candidate in the pool in the
// same transaction, so the entry commits with the verdict that earned it.
//
// Amending a pass down to a borderline or a fail withdraws the pool entry
// that pass earned, unless a recruiter has since flagged the candidate: a
// flag is the recruiter's own judgement and only they remove it.
func (s *ReviewService) Save(ctx context.Context, p Principal, in ReviewInput) (Review, error) {
	if err := requireVetter(p); err != nil {
		return Review{}, err
	}
	if !validVerdict(in.Verdict) {
		return Review{}, ErrBadVerdict
	}
	var out Review
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		att, err := tx.Q.GetAttemptForUpdate(ctx, in.AttemptID)
		if err != nil {
			return err
		}
		if att.Preview {
			return ErrNotFound
		}
		app, err := tx.Q.GetApplication(ctx, att.ApplicationID.UUID)
		if err != nil {
			return err
		}
		stage, err := tx.Q.GetStage(ctx, att.StageID.UUID)
		if err != nil {
			return err
		}
		if err := requireAssigned(app, stage, p); err != nil {
			return err
		}
		switch att.Status {
		case AttemptScored, AttemptReviewed:
		default:
			return ErrNotScored
		}
		row, err := tx.Q.UpsertReviewByAuthor(ctx, db.UpsertReviewByAuthorParams{
			OrgID: p.OrgID, AttemptID: in.AttemptID, VetterID: p.UserID,
			Verdict: in.Verdict, Notes: nullable(strings.TrimSpace(in.Notes)),
		})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			// The conflicting row belongs to another vetter.
			return ErrReviewFiled
		case err != nil:
			return err
		}
		if err := tx.Q.SetAttemptReviewed(ctx, in.AttemptID); err != nil {
			return err
		}
		out = Review{
			ID: row.ID, AttemptID: row.AttemptID, VetterID: row.VetterID, Verdict: row.Verdict,
			Notes: deref(row.Notes), CreatedAt: row.CreatedAt.Time.UTC(),
		}
		if s.pool == nil {
			return nil
		}
		if in.Verdict != VerdictPass {
			return s.pool.RetractReviewPass(ctx, tx, p.OrgID, att.ApplicationID.UUID)
		}
		threshold, err := poolThreshold(ctx, tx, p.OrgID)
		if err != nil {
			return err
		}
		score, err := att.Score.Float64Value()
		if err != nil {
			return err
		}
		return s.pool.OnReviewPass(ctx, tx, p.OrgID, att.ApplicationID.UUID, score.Float64, threshold)
	})
	if err != nil {
		return Review{}, wrapReview("save review", err)
	}
	return out, nil
}

// Summaries are the attempts on an application with their score and verdict:
// the recruiter's view of the assessment without the recording. A recruiter
// or an admin reads any application; a vetter reads only one assigned to
// them, by the rule the review screen uses.
func (s *ReviewService) Summaries(ctx context.Context, p Principal, applicationID uuid.UUID) ([]AttemptSummary, error) {
	var out []AttemptSummary
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		app, err := tx.Q.GetApplication(ctx, applicationID)
		if err != nil {
			return err
		}
		stage, err := tx.Q.GetStage(ctx, app.StageID)
		if err != nil {
			return err
		}
		if err := requireReviewer(p, app, stage); err != nil {
			return err
		}
		rows, err := tx.Q.ListAttemptSummariesForApplication(ctx, applicationID)
		if err != nil {
			return err
		}
		out = make([]AttemptSummary, 0, len(rows))
		for _, r := range rows {
			out = append(out, AttemptSummary{
				AttemptID: r.AttemptID, StageID: r.StageID, StageName: r.StageName, Status: r.Status,
				Score: numericPtr(r.Score), RiskScore: numericPtr(r.RiskScore), ErrorCount: int(r.ErrorCount),
				FinishedAt: r.FinishedAt.Time.UTC(), Verdict: deref(r.Verdict), ReviewNotes: deref(r.ReviewNotes),
				VetterName: deref(r.VetterName), ReviewedAt: r.ReviewedAt.Time.UTC(),
			})
		}
		return nil
	})
	if err != nil {
		return nil, wrapReview("attempt summaries", err)
	}
	return out, nil
}

// reviewable loads the attempt and refuses a caller it is not for.
func (s *ReviewService) reviewable(ctx context.Context, tx *store.Tx, p Principal, attemptID uuid.UUID) (db.Attempt, db.Application, error) {
	att, err := tx.Q.GetAttempt(ctx, attemptID)
	if err != nil {
		return db.Attempt{}, db.Application{}, err
	}
	if att.Preview {
		// A recruiter's own sitting is not a candidate to review.
		return db.Attempt{}, db.Application{}, ErrNotFound
	}
	app, err := tx.Q.GetApplication(ctx, att.ApplicationID.UUID)
	if err != nil {
		return db.Attempt{}, db.Application{}, err
	}
	stage, err := tx.Q.GetStage(ctx, att.StageID.UUID)
	if err != nil {
		return db.Attempt{}, db.Application{}, err
	}
	if err := requireReviewer(p, app, stage); err != nil {
		return db.Attempt{}, db.Application{}, err
	}
	return att, app, nil
}

// requireReviewer refuses an org user the attempt is not theirs to see. A
// recruiter or an admin oversees every attempt; a vetter reads only the ones
// assigned to them, by the rule the scorecard screens use.
func requireReviewer(p Principal, app db.Application, stage db.Stage) error {
	if p.Kind != PrincipalOrgUser {
		return ErrForbidden
	}
	if p.HasRole(RoleRecruiter) || p.HasRole(RoleAdmin) {
		return nil
	}
	if !p.HasRole(RoleVetter) {
		return ErrForbidden
	}
	return requireAssigned(app, stage, p)
}

// attemptSignals reads the stored signals and decodes their evidence.
func attemptSignals(ctx context.Context, tx *store.Tx, attemptID uuid.UUID) ([]AttemptSignal, error) {
	rows, err := tx.Q.ListIntegritySignals(ctx, attemptID)
	if err != nil {
		return nil, err
	}
	out := make([]AttemptSignal, 0, len(rows))
	for _, r := range rows {
		sig := AttemptSignal{Name: r.Name, Confidence: r.Confidence, Evidence: []signals.Evidence{}}
		if v, err := r.Value.Float64Value(); err == nil && v.Valid {
			sig.Value = v.Float64
		}
		if w, err := r.Weight.Float64Value(); err == nil && w.Valid {
			sig.Weight = w.Float64
		}
		if err := json.Unmarshal(r.Evidence, &sig.Evidence); err != nil {
			return nil, fmt.Errorf("evidence of signal %s: %w", r.Name, err)
		}
		out = append(out, sig)
	}
	return out, nil
}

// poolThreshold is the org's pool score threshold, the default filling in
// for an org that never set one.
func poolThreshold(ctx context.Context, tx *store.Tx, orgID uuid.UUID) (int, error) {
	raw, err := tx.Q.GetOrgSetting(ctx, db.GetOrgSettingParams{OrgID: orgID, Key: SettingPoolScoreThreshold})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return DefaultSettings().PoolScoreThreshold, nil
	case err != nil:
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return 0, fmt.Errorf("%w: %s is not a whole number", ErrInvalidSettings, SettingPoolScoreThreshold)
	}
	return n, nil
}

func validVerdict(s string) bool {
	for _, v := range Verdicts {
		if s == v {
			return true
		}
	}
	return false
}

// numericPtr reads a nullable numeric column; a null or unreadable value is
// no number rather than a zero, which reads as a real score.
func numericPtr(n pgtype.Numeric) *float64 {
	f, err := n.Float64Value()
	if err != nil || !f.Valid {
		return nil
	}
	return &f.Float64
}

func wrapReview(what string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pgx.ErrNoRows):
		return ErrNotFound
	case errors.Is(err, ErrBadVerdict), errors.Is(err, ErrReviewFiled), errors.Is(err, ErrNotScored),
		errors.Is(err, ErrNotFound), errors.Is(err, ErrForbidden):
		return err
	}
	return fmt.Errorf("%s: %w", what, err)
}

// submissionOutcome reads the runner's answer off a stored submission: its
// verdict, how many cases passed of how many ran, and whether anything ran
// out of time. A submission the runner never answered for reports nothing,
// which is what keeps it off the replay's jump chips.
func submissionOutcome(raw json.RawMessage) (status string, passed, total int, timedOut bool) {
	if len(raw) == 0 {
		return "", 0, 0, false
	}
	var res server.Response
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", 0, 0, false
	}
	timedOut = res.Status == server.StatusTimeout
	for _, r := range res.Results {
		total++
		switch r.Status {
		case server.TestPass:
			passed++
		case server.TestTimeout:
			timedOut = true
		}
	}
	return res.Status, passed, total, timedOut
}

// scoredSubmit is the submit a problem's score came from: the last one, the
// same reading finalSource takes.
func scoredSubmit(problemID uuid.UUID, subs []db.Submission) json.RawMessage {
	var out json.RawMessage
	for _, sub := range subs {
		if sub.ProblemID == problemID && sub.Kind == SubmissionSubmit {
			out = sub.Result // ordered by created_at, so the last wins
		}
	}
	return out
}

// reviewTests projects a stored runner response onto the problem's cases.
// Cases the response says nothing about are still listed, so a compile error
// reads as every case unrun rather than as a problem with no cases.
func reviewTests(raw json.RawMessage, cases []ProblemTestCase) []ReviewTest {
	var res server.Response
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &res)
	}
	byID := make(map[string]string, len(res.Results))
	timeByID := make(map[string]int64, len(res.Results))
	for _, r := range res.Results {
		byID[r.TestID] = r.Status
		timeByID[r.TestID] = r.TimeMs
	}
	out := make([]ReviewTest, 0, len(cases))
	for _, c := range cases {
		out = append(out, ReviewTest{
			Position: c.Position, Name: c.Name, Class: c.Class, Visibility: c.Visibility,
			Status: byID[c.ID.String()], TimeMs: timeByID[c.ID.String()], Weight: c.Weight,
		})
	}
	return out
}
