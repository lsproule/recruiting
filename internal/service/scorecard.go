package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"recruiting/internal/domain"
	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

// The verdict a scorecard closes with.
const (
	OverallStrongYes = "strong_yes"
	OverallYes       = "yes"
	OverallNo        = "no"
	OverallStrongNo  = "strong_no"
)

// Overalls are the verdicts in the order a form offers them, strongest first.
var Overalls = []string{OverallStrongYes, OverallYes, OverallNo, OverallStrongNo}

// EventScorecardStrongYes is the application_event a strong yes writes. It is
// the domain event the talent pool listens for; nothing else reacts to it.
const EventScorecardStrongYes = "scorecard_strong_yes"

// ScoreMin and ScoreMax bound every criterion score.
const (
	ScoreMin = 1
	ScoreMax = 5
)

var (
	ErrBadScore     = errors.New("service: every criterion needs a score from 1 to 5")
	ErrBadOverall   = errors.New("service: that is not one of the overall verdicts")
	ErrNotAuthor    = errors.New("service: only the interviewer who filed a scorecard may change it")
	ErrNotInterview = errors.New("service: only an interview stage takes a scorecard")
)

// Criterion is one line of a rubric: what the interviewer scores, and what
// they are being asked to judge.
type Criterion struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// Rubric is a stage's ordered criteria.
type Rubric struct {
	ID        uuid.UUID
	JobID     uuid.UUID
	StageID   uuid.UUID
	StageName string
	Name      string
	Criteria  []Criterion
}

// RubricInput replaces a stage's rubric.
type RubricInput struct {
	Name     string
	Criteria []Criterion
}

// CriterionScore is one criterion as an interviewer filled it in.
type CriterionScore struct {
	Name  string `json:"name"`
	Score int    `json:"score"`
	Notes string `json:"notes,omitempty"`
}

// Scorecard is one interviewer's verdict on one interview stage. Criteria is
// the rubric as it read when the card was filed, so later rubric edits leave
// the record alone.
type Scorecard struct {
	ID            uuid.UUID
	ApplicationID uuid.UUID
	StageID       uuid.UUID
	StageName     string
	VetterID      uuid.UUID
	VetterName    string
	Criteria      []Criterion
	Scores        []CriterionScore
	Overall       string
	Notes         string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// ScorecardInput is one submission. ID names the card being edited; zero
// creates the caller's card for the stage, or rewrites the one they filed.
type ScorecardInput struct {
	ID            uuid.UUID
	ApplicationID uuid.UUID
	StageID       uuid.UUID
	Scores        []CriterionScore
	Overall       string
	Notes         string
}

// ScorecardForm is the vetter's form: who they interviewed, what to score,
// and their own card when they have already filed one.
type ScorecardForm struct {
	ApplicationID uuid.UUID
	StageID       uuid.UUID
	CandidateName string
	JobTitle      string
	StageName     string
	Criteria      []Criterion
	Card          *Scorecard
}

// Assignment is one interview waiting on the signed-in vetter.
type Assignment struct {
	ApplicationID  uuid.UUID
	StageID        uuid.UUID
	CandidateName  string
	CandidateEmail string
	JobTitle       string
	StageName      string
	Submitted      bool
	Overall        string
}

// ScorecardService keeps the per-stage rubrics and the cards interviewers
// file against them.
type ScorecardService struct{ st *store.Store }

func NewScorecardService(st *store.Store) *ScorecardService { return &ScorecardService{st: st} }

// Rubric is the stage's criteria. Any org user may read them; the vetter's
// form and the recruiter's editor show the same list.
func (s *ScorecardService) Rubric(ctx context.Context, p Principal, jobID, stageID uuid.UUID) (Rubric, error) {
	if p.Kind != PrincipalOrgUser {
		return Rubric{}, ErrForbidden
	}
	var out Rubric
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		stage, err := interviewStageOf(ctx, tx, jobID, stageID)
		if err != nil {
			return err
		}
		out, err = rubricOf(ctx, tx, stage)
		return err
	})
	if err != nil {
		return Rubric{}, wrapScorecard("rubric", err)
	}
	return out, nil
}

// SetRubric replaces the stage's criteria. Cards already filed keep their own
// snapshot, so this only changes what the next interviewer is asked.
func (s *ScorecardService) SetRubric(ctx context.Context, p Principal, jobID, stageID uuid.UUID, in RubricInput) (Rubric, error) {
	if err := requireRecruiter(p); err != nil {
		return Rubric{}, err
	}
	criteria := cleanCriteria(in.Criteria)
	name := strings.TrimSpace(in.Name)
	var out Rubric
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		stage, err := interviewStageOf(ctx, tx, jobID, stageID)
		if err != nil {
			return err
		}
		if name == "" {
			name = stage.Name
		}
		encoded, err := json.Marshal(criteria)
		if err != nil {
			return err
		}
		var row db.ScorecardRubric
		if stage.ScorecardRubricID.Valid {
			row, err = tx.Q.UpdateScorecardRubric(ctx, db.UpdateScorecardRubricParams{
				ID: stage.ScorecardRubricID.UUID, Name: name, Criteria: encoded,
			})
			if err != nil {
				return err
			}
		} else {
			if row, err = tx.Q.CreateScorecardRubric(ctx, db.CreateScorecardRubricParams{OrgID: p.OrgID, Name: name, Criteria: encoded}); err != nil {
				return err
			}
			if err := tx.Q.SetStageRubric(ctx, db.SetStageRubricParams{
				ID: stage.ID, ScorecardRubricID: uuid.NullUUID{UUID: row.ID, Valid: true},
			}); err != nil {
				return err
			}
		}
		out = Rubric{ID: row.ID, JobID: jobID, StageID: stage.ID, StageName: stage.Name, Name: row.Name, Criteria: criteria}
		return nil
	})
	if err != nil {
		return Rubric{}, wrapScorecard("set rubric", err)
	}
	return out, nil
}

// Assignments are the interviews the signed-in vetter owns, newest first,
// each with the card they filed if any.
func (s *ScorecardService) Assignments(ctx context.Context, p Principal) ([]Assignment, error) {
	if err := requireVetter(p); err != nil {
		return nil, err
	}
	var out []Assignment
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		rows, err := tx.Q.ListVetterAssignments(ctx, p.UserID)
		if err != nil {
			return err
		}
		out = make([]Assignment, 0, len(rows))
		for _, r := range rows {
			out = append(out, Assignment{
				ApplicationID: r.ApplicationID, StageID: r.StageID,
				CandidateName: r.CandidateName, CandidateEmail: r.CandidateEmail,
				JobTitle: r.JobTitle, StageName: r.StageName,
				Submitted: r.ScorecardID.Valid, Overall: deref(r.Overall),
			})
		}
		return nil
	})
	if err != nil {
		return nil, wrapScorecard("assignments", err)
	}
	return out, nil
}

// Form is the vetter's scorecard form for one application and stage.
func (s *ScorecardService) Form(ctx context.Context, p Principal, applicationID, stageID uuid.UUID) (ScorecardForm, error) {
	if err := requireVetter(p); err != nil {
		return ScorecardForm{}, err
	}
	var out ScorecardForm
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		app, err := tx.Q.GetApplication(ctx, applicationID)
		if err != nil {
			return err
		}
		card, err := tx.Q.GetApplicationCard(ctx, applicationID)
		if err != nil {
			return err
		}
		stage, err := interviewStageOf(ctx, tx, app.JobID, stageID)
		if err != nil {
			return err
		}
		if err := requireAssigned(app, stage, p); err != nil {
			return err
		}
		rubric, err := rubricOf(ctx, tx, stage)
		if err != nil {
			return err
		}
		out = ScorecardForm{
			ApplicationID: applicationID, StageID: stageID,
			CandidateName: card.CandidateName, JobTitle: card.JobTitle, StageName: stage.Name,
			Criteria: rubric.Criteria,
		}
		row, err := tx.Q.GetScorecardForVetter(ctx, db.GetScorecardForVetterParams{
			ApplicationID: applicationID, StageID: stageID, VetterID: p.UserID,
		})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return nil
		case err != nil:
			return err
		}
		filed := toScorecard(row)
		filed.StageName = stage.Name
		// The card was filed against the criteria it carries, not today's.
		out.Criteria = filed.Criteria
		out.Card = &filed
		return nil
	})
	if err != nil {
		return ScorecardForm{}, wrapScorecard("scorecard form", err)
	}
	return out, nil
}

// Save files the caller's scorecard, or rewrites the one they filed. A card
// belongs to the interviewer who wrote it: nobody else may change it. A
// strong yes writes the application event the talent pool consumes.
func (s *ScorecardService) Save(ctx context.Context, p Principal, in ScorecardInput) (Scorecard, error) {
	if err := requireVetter(p); err != nil {
		return Scorecard{}, err
	}
	if !validOverall(in.Overall) {
		return Scorecard{}, ErrBadOverall
	}
	var out Scorecard
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		app, err := tx.Q.GetApplication(ctx, in.ApplicationID)
		if err != nil {
			return err
		}
		stage, err := interviewStageOf(ctx, tx, app.JobID, in.StageID)
		if err != nil {
			return err
		}
		if err := requireAssigned(app, stage, p); err != nil {
			return err
		}
		existing, err := s.existing(ctx, tx, p, in)
		if err != nil {
			return err
		}
		// An edit is judged against the criteria the interviewer answered;
		// a new card snapshots the rubric as it reads now.
		criteria := existing.criteria
		if existing.row == nil {
			rubric, err := rubricOf(ctx, tx, stage)
			if err != nil {
				return err
			}
			criteria = rubric.Criteria
			existing.rubricID = rubric.rubricID()
		}
		scores, err := alignScores(criteria, in.Scores)
		if err != nil {
			return err
		}
		encodedScores, err := json.Marshal(scores)
		if err != nil {
			return err
		}
		var row db.Scorecard
		if existing.row != nil {
			row, err = tx.Q.UpdateScorecardByAuthor(ctx, db.UpdateScorecardByAuthorParams{
				ID: existing.row.ID, VetterID: p.UserID, Scores: encodedScores,
				Overall: in.Overall, Notes: nullable(strings.TrimSpace(in.Notes)),
			})
		} else {
			encodedCriteria, mErr := json.Marshal(criteria)
			if mErr != nil {
				return mErr
			}
			row, err = tx.Q.UpsertScorecard(ctx, db.UpsertScorecardParams{
				OrgID: p.OrgID, ApplicationID: in.ApplicationID, StageID: in.StageID, VetterID: p.UserID,
				RubricID: existing.rubricID, Criteria: encodedCriteria, Scores: encodedScores,
				Overall: in.Overall, Notes: nullable(strings.TrimSpace(in.Notes)),
			})
		}
		if err != nil {
			return err
		}
		out = toScorecard(row)
		out.StageName = stage.Name
		// Only the crossing into a strong yes is the event; re-saving one
		// already filed must not tell the pool twice.
		was := existing.row != nil && existing.row.Overall == OverallStrongYes
		if in.Overall != OverallStrongYes || was {
			return nil
		}
		payload, err := json.Marshal(map[string]any{"scorecard_id": row.ID, "overall": row.Overall})
		if err != nil {
			return err
		}
		_, err = tx.Q.CreateApplicationEvent(ctx, db.CreateApplicationEventParams{
			OrgID: p.OrgID, ApplicationID: in.ApplicationID, ActorKind: actorKind(p),
			ActorID:     uuid.NullUUID{UUID: p.UserID, Valid: p.UserID != uuid.Nil},
			Kind:        EventScorecardStrongYes,
			FromStageID: uuid.NullUUID{UUID: in.StageID, Valid: true},
			Payload:     payload,
		})
		return err
	})
	if err != nil {
		return Scorecard{}, wrapScorecard("save scorecard", err)
	}
	return out, nil
}

// List is every scorecard on an application, in pipeline order: the summary
// the recruiter reads on the application page.
func (s *ScorecardService) List(ctx context.Context, p Principal, applicationID uuid.UUID) ([]Scorecard, error) {
	if p.Kind != PrincipalOrgUser {
		return nil, ErrForbidden
	}
	var out []Scorecard
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		rows, err := tx.Q.ListScorecardsForApplication(ctx, applicationID)
		if err != nil {
			return err
		}
		out = make([]Scorecard, 0, len(rows))
		for _, r := range rows {
			card := toScorecard(db.Scorecard{
				ID: r.ID, ApplicationID: r.ApplicationID, StageID: r.StageID, VetterID: r.VetterID,
				RubricID: r.RubricID, Criteria: r.Criteria, Scores: r.Scores, Overall: r.Overall,
				Notes: r.Notes, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
			})
			card.VetterName, card.StageName = r.VetterName, r.StageName
			out = append(out, card)
		}
		return nil
	})
	if err != nil {
		return nil, wrapScorecard("list scorecards", err)
	}
	return out, nil
}

// filed is the card a save is about to rewrite, if there is one.
type filed struct {
	row      *db.Scorecard
	criteria []Criterion
	rubricID uuid.NullUUID
}

// existing finds the caller's card for the save: the one the input names, or
// the one they already filed on the stage. A card another interviewer wrote
// is refused rather than silently forked into a second card.
func (s *ScorecardService) existing(ctx context.Context, tx *store.Tx, p Principal, in ScorecardInput) (filed, error) {
	var out filed
	var row db.Scorecard
	var err error
	if in.ID != uuid.Nil {
		row, err = tx.Q.GetScorecard(ctx, in.ID)
	} else {
		row, err = tx.Q.GetScorecardForVetter(ctx, db.GetScorecardForVetterParams{
			ApplicationID: in.ApplicationID, StageID: in.StageID, VetterID: p.UserID,
		})
	}
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if in.ID != uuid.Nil {
			return out, ErrNotFound
		}
		return out, nil
	case err != nil:
		return out, err
	}
	if row.VetterID != p.UserID {
		return out, ErrNotAuthor
	}
	if row.ApplicationID != in.ApplicationID || row.StageID != in.StageID {
		return out, ErrNotFound
	}
	out.row, out.rubricID = &row, row.RubricID
	out.criteria = decodeCriteria(row.Criteria)
	return out, nil
}

func (r Rubric) rubricID() uuid.NullUUID {
	return uuid.NullUUID{UUID: r.ID, Valid: r.ID != uuid.Nil}
}

// rubricOf reads the stage's criteria. A stage without a rubric scores
// nothing: the interviewer still records notes and a verdict.
func rubricOf(ctx context.Context, tx *store.Tx, stage db.Stage) (Rubric, error) {
	out := Rubric{JobID: stage.JobID, StageID: stage.ID, StageName: stage.Name, Name: stage.Name, Criteria: []Criterion{}}
	if !stage.ScorecardRubricID.Valid {
		return out, nil
	}
	row, err := tx.Q.GetScorecardRubric(ctx, stage.ScorecardRubricID.UUID)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return Rubric{}, err
	}
	out.ID, out.Name, out.Criteria = row.ID, row.Name, decodeCriteria(row.Criteria)
	return out, nil
}

// stageOf refuses a stage id belonging to another job, so a guessed id cannot
// reach someone else's pipeline.
func stageOf(ctx context.Context, tx *store.Tx, jobID, stageID uuid.UUID) (db.Stage, error) {
	stage, err := tx.Q.GetStage(ctx, stageID)
	if err != nil {
		return db.Stage{}, err
	}
	if stage.JobID != jobID {
		return db.Stage{}, ErrNotFound
	}
	return stage, nil
}

// requireAssigned refuses an interviewer the application is not waiting on.
// The application's own vetter decides; until a candidate books, and so fixes
// one, the stage's default stands in.
func requireAssigned(app db.Application, stage db.Stage, p Principal) error {
	assigned := app.VetterID
	if !assigned.Valid {
		assigned = stage.DefaultVetterID
	}
	if !assigned.Valid || assigned.UUID != p.UserID {
		return ErrForbidden
	}
	return nil
}

func interviewStageOf(ctx context.Context, tx *store.Tx, jobID, stageID uuid.UUID) (db.Stage, error) {
	stage, err := stageOf(ctx, tx, jobID, stageID)
	if err != nil {
		return db.Stage{}, err
	}
	if domain.StageKind(stage.Kind) != domain.StageInterview {
		return db.Stage{}, ErrNotInterview
	}
	return stage, nil
}

// alignScores puts the submitted scores in rubric order and refuses anything
// missing, unknown, or off the 1–5 scale.
func alignScores(criteria []Criterion, in []CriterionScore) ([]CriterionScore, error) {
	given := make(map[string]CriterionScore, len(in))
	for _, s := range in {
		given[s.Name] = s
	}
	out := make([]CriterionScore, 0, len(criteria))
	for _, c := range criteria {
		s, ok := given[c.Name]
		if !ok {
			return nil, fmt.Errorf("%w: %s is unscored", ErrBadScore, c.Name)
		}
		if s.Score < ScoreMin || s.Score > ScoreMax {
			return nil, fmt.Errorf("%w: %s scored %d", ErrBadScore, c.Name, s.Score)
		}
		delete(given, c.Name)
		out = append(out, CriterionScore{Name: c.Name, Score: s.Score, Notes: strings.TrimSpace(s.Notes)})
	}
	for name := range given {
		return nil, fmt.Errorf("%w: %s is not on this rubric", ErrBadScore, name)
	}
	return out, nil
}

func cleanCriteria(in []Criterion) []Criterion {
	out := make([]Criterion, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, c := range in {
		c.Name, c.Description = strings.TrimSpace(c.Name), strings.TrimSpace(c.Description)
		if c.Name == "" || seen[c.Name] {
			continue
		}
		seen[c.Name] = true
		out = append(out, c)
	}
	return out
}

func validOverall(s string) bool {
	for _, o := range Overalls {
		if s == o {
			return true
		}
	}
	return false
}

func decodeCriteria(raw []byte) []Criterion {
	out := []Criterion{}
	_ = json.Unmarshal(raw, &out)
	return out
}

func toScorecard(row db.Scorecard) Scorecard {
	scores := []CriterionScore{}
	_ = json.Unmarshal(row.Scores, &scores)
	return Scorecard{
		ID: row.ID, ApplicationID: row.ApplicationID, StageID: row.StageID, VetterID: row.VetterID,
		Criteria: decodeCriteria(row.Criteria), Scores: scores, Overall: row.Overall, Notes: deref(row.Notes),
		CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}
}

func wrapScorecard(what string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pgx.ErrNoRows):
		return ErrNotFound
	case errors.Is(err, ErrBadScore), errors.Is(err, ErrBadOverall), errors.Is(err, ErrNotAuthor),
		errors.Is(err, ErrNotInterview), errors.Is(err, ErrNotFound), errors.Is(err, ErrForbidden):
		return err
	}
	return fmt.Errorf("%s: %w", what, err)
}
