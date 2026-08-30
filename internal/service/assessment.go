package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"recruiting/internal/domain"
	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

var (
	ErrAssessmentInvalid = errors.New("service: invalid assessment")
	// ErrAssessmentInUse refuses deleting an assessment attempts refer to.
	ErrAssessmentInUse = errors.New("service: candidates have attempted this assessment; it cannot be deleted")
	// ErrStageNotAssessment refuses attaching to a stage of another kind.
	ErrStageNotAssessment = errors.New("service: only an assessment stage can carry an assessment")
)

// Assessment is an ordered set of problems a candidate solves within a time
// limit. The language override, when set, narrows every problem to that one
// language.
type Assessment struct {
	ID               uuid.UUID
	OrgID            uuid.UUID
	Name             string
	DurationMinutes  int
	LanguageOverride string
	InviteWindowDays int
	Problems         []Problem // in order; scalar fields only on List
	ProblemCount     int
}

// AssessmentInput is what the recruiter's form posts.
type AssessmentInput struct {
	Name             string
	DurationMinutes  int
	LanguageOverride string
	InviteWindowDays int
	ProblemIDs       []uuid.UUID
}

func (in AssessmentInput) clean() (AssessmentInput, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.LanguageOverride = strings.ToLower(strings.TrimSpace(in.LanguageOverride))
	var problems []string
	if in.Name == "" {
		problems = append(problems, "name is required")
	}
	if in.DurationMinutes < 1 || in.DurationMinutes > 24*60 {
		problems = append(problems, "duration must be between 1 minute and 24 hours")
	}
	if in.InviteWindowDays < 1 {
		problems = append(problems, "invite window must be at least 1 day")
	}
	if in.LanguageOverride != "" && !containsString(domain.ProblemLanguages, in.LanguageOverride) {
		problems = append(problems, "unknown language "+in.LanguageOverride)
	}
	if len(in.ProblemIDs) == 0 {
		problems = append(problems, "pick at least one problem")
	}
	seen := make(map[uuid.UUID]bool, len(in.ProblemIDs))
	for _, id := range in.ProblemIDs {
		if seen[id] {
			problems = append(problems, "a problem is listed twice")
			break
		}
		seen[id] = true
	}
	if len(problems) > 0 {
		return in, fmt.Errorf("%w: %s", ErrAssessmentInvalid, strings.Join(problems, "; "))
	}
	return in, nil
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// AssessmentService owns assessments and their attachment to stages.
type AssessmentService struct{ st *store.Store }

func NewAssessmentService(st *store.Store) *AssessmentService { return &AssessmentService{st: st} }

// List is the org's assessments by name, with problem counts.
func (s *AssessmentService) List(ctx context.Context, p Principal) ([]Assessment, error) {
	if err := requireRecruiter(p); err != nil {
		return nil, err
	}
	var out []Assessment
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		rows, err := tx.Q.ListAssessments(ctx, p.OrgID)
		if err != nil {
			return err
		}
		counts, err := tx.Q.CountAssessmentProblems(ctx, p.OrgID)
		if err != nil {
			return err
		}
		n := make(map[uuid.UUID]int, len(counts))
		for _, c := range counts {
			n[c.AssessmentID] = int(c.N)
		}
		out = make([]Assessment, 0, len(rows))
		for _, r := range rows {
			a := toAssessment(r)
			a.ProblemCount = n[r.ID]
			out = append(out, a)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list assessments: %w", err)
	}
	return out, nil
}

// Get loads one assessment with its problems in order.
func (s *AssessmentService) Get(ctx context.Context, p Principal, id uuid.UUID) (Assessment, error) {
	if err := requireRecruiter(p); err != nil {
		return Assessment{}, err
	}
	var out Assessment
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		var err error
		out, err = loadAssessment(ctx, tx, id)
		return err
	})
	if err != nil {
		return Assessment{}, wrapAssessment("get assessment", err)
	}
	return out, nil
}

// Create stores a new assessment.
func (s *AssessmentService) Create(ctx context.Context, p Principal, in AssessmentInput) (Assessment, error) {
	if err := requireRecruiter(p); err != nil {
		return Assessment{}, err
	}
	in, err := in.clean()
	if err != nil {
		return Assessment{}, err
	}
	var out Assessment
	err = s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		row, err := tx.Q.CreateAssessment(ctx, db.CreateAssessmentParams{
			OrgID: p.OrgID, Name: in.Name, DurationMinutes: int32(in.DurationMinutes),
			LanguageOverride: nullText(in.LanguageOverride), InviteWindowDays: int32(in.InviteWindowDays),
		})
		if err != nil {
			return err
		}
		if err := writeAssessmentProblems(ctx, tx, p.OrgID, row.ID, in.ProblemIDs); err != nil {
			return err
		}
		out, err = loadAssessment(ctx, tx, row.ID)
		return err
	})
	if err != nil {
		return Assessment{}, wrapAssessment("create assessment", err)
	}
	return out, nil
}

// Update replaces an assessment's fields and problem list.
func (s *AssessmentService) Update(ctx context.Context, p Principal, id uuid.UUID, in AssessmentInput) (Assessment, error) {
	if err := requireRecruiter(p); err != nil {
		return Assessment{}, err
	}
	in, err := in.clean()
	if err != nil {
		return Assessment{}, err
	}
	var out Assessment
	err = s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		if _, err := tx.Q.UpdateAssessment(ctx, db.UpdateAssessmentParams{
			ID: id, OrgID: p.OrgID, Name: in.Name, DurationMinutes: int32(in.DurationMinutes),
			LanguageOverride: nullText(in.LanguageOverride), InviteWindowDays: int32(in.InviteWindowDays),
		}); err != nil {
			return err
		}
		if err := tx.Q.DeleteAssessmentProblems(ctx, id); err != nil {
			return err
		}
		if err := writeAssessmentProblems(ctx, tx, p.OrgID, id, in.ProblemIDs); err != nil {
			return err
		}
		out, err = loadAssessment(ctx, tx, id)
		return err
	})
	if err != nil {
		return Assessment{}, wrapAssessment("update assessment", err)
	}
	return out, nil
}

// Delete removes an assessment nobody has attempted; stages pointing at it
// are detached.
func (s *AssessmentService) Delete(ctx context.Context, p Principal, id uuid.UUID) error {
	if err := requireRecruiter(p); err != nil {
		return err
	}
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		n, err := tx.Q.CountAttemptsForAssessment(ctx, id)
		if err != nil {
			return err
		}
		if n > 0 {
			return ErrAssessmentInUse
		}
		rows, err := tx.Q.DeleteAssessment(ctx, db.DeleteAssessmentParams{ID: id, OrgID: p.OrgID})
		if err != nil {
			return err
		}
		if rows == 0 {
			return ErrNotFound
		}
		return nil
	})
	return wrapAssessment("delete assessment", err)
}

// AttachToStage points a job's assessment stage at the assessment; the nil
// id detaches. Entering the stage then invites the candidate to it.
func (s *AssessmentService) AttachToStage(ctx context.Context, p Principal, jobID, stageID, assessmentID uuid.UUID) error {
	if err := requireRecruiter(p); err != nil {
		return err
	}
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		stage, err := stageOfJob(ctx, tx, jobID, stageID)
		if err != nil {
			return err
		}
		if stage.Kind != domain.StageAssessment {
			return ErrStageNotAssessment
		}
		if assessmentID != uuid.Nil {
			if _, err := tx.Q.GetAssessment(ctx, assessmentID); err != nil {
				return err
			}
		}
		n, err := tx.Q.SetStageAssessment(ctx, db.SetStageAssessmentParams{
			ID: stageID, JobID: jobID, AssessmentID: uuid.NullUUID{UUID: assessmentID, Valid: assessmentID != uuid.Nil},
		})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		return nil
	})
	return wrapAssessment("attach assessment", err)
}

// StageAssessment is the assessment a stage carries, or nil.
func (s *AssessmentService) StageAssessment(ctx context.Context, p Principal, stageID uuid.UUID) (*Assessment, error) {
	if p.Kind != PrincipalOrgUser {
		return nil, ErrForbidden
	}
	var out *Assessment
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		stage, err := tx.Q.GetStage(ctx, stageID)
		if err != nil {
			return err
		}
		if !stage.AssessmentID.Valid {
			return nil
		}
		a, err := loadAssessment(ctx, tx, stage.AssessmentID.UUID)
		if err != nil {
			return err
		}
		out = &a
		return nil
	})
	if err != nil {
		return nil, wrapAssessment("stage assessment", err)
	}
	return out, nil
}

func writeAssessmentProblems(ctx context.Context, tx *store.Tx, orgID, id uuid.UUID, problemIDs []uuid.UUID) error {
	for i, pid := range problemIDs {
		// The read proves the problem is visible to the org (its own or seed).
		if _, err := tx.Q.GetProblem(ctx, pid); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("%w: problem %s does not exist", ErrAssessmentInvalid, pid)
			}
			return err
		}
		if err := tx.Q.CreateAssessmentProblem(ctx, db.CreateAssessmentProblemParams{
			AssessmentID: id, OrgID: orgID, ProblemID: pid, Position: int32(i + 1),
		}); err != nil {
			return err
		}
	}
	return nil
}

// loadAssessment reads an assessment and its problems with their test cases;
// the candidate session and the recruiter detail both need them.
func loadAssessment(ctx context.Context, tx *store.Tx, id uuid.UUID) (Assessment, error) {
	row, err := tx.Q.GetAssessment(ctx, id)
	if err != nil {
		return Assessment{}, err
	}
	out := toAssessment(row)
	rows, err := tx.Q.ListAssessmentProblems(ctx, id)
	if err != nil {
		return Assessment{}, err
	}
	for _, r := range rows {
		p, err := loadProblem(ctx, tx, r.ID)
		if err != nil {
			return Assessment{}, err
		}
		out.Problems = append(out.Problems, p)
	}
	out.ProblemCount = len(out.Problems)
	return out, nil
}

func toAssessment(r db.Assessment) Assessment {
	return Assessment{
		ID: r.ID, OrgID: r.OrgID, Name: r.Name, DurationMinutes: int(r.DurationMinutes),
		LanguageOverride: text(r.LanguageOverride), InviteWindowDays: int(r.InviteWindowDays),
	}
}

func wrapAssessment(what string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pgx.ErrNoRows):
		return ErrNotFound
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrForbidden), errors.Is(err, ErrAssessmentInvalid),
		errors.Is(err, ErrAssessmentInUse), errors.Is(err, ErrStageNotAssessment):
		return err
	}
	return fmt.Errorf("%s: %w", what, err)
}
