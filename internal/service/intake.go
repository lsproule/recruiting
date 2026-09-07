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
	"recruiting/internal/mail"
	"recruiting/internal/queue"
	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

// The intake's five steps, in the order the wizard walks them.
const (
	IntakeStepClient = iota + 1
	IntakeStepJob
	IntakeStepSkills
	IntakeStepQuestion
	IntakeStepReview
)

// IntakeStepNames labels each step for the rail; index by step - 1.
var IntakeStepNames = []string{"Client", "Job", "Skills to test", "Coding question", "Review"}

// Where step four's problems come from.
const (
	// IntakeSourceDefault fills each slot from the bank by skill overlap.
	IntakeSourceDefault = "default"
	// IntakeSourceBank is a set the recruiter assembled themselves.
	IntakeSourceBank = "bank"
	// IntakeSourceAuthor is a set built around a problem written for this job.
	IntakeSourceAuthor = "author"
)

// DefaultAssessmentMinutes sizes an intake's assessment when the problems
// recommend nothing of their own.
const DefaultAssessmentMinutes = 60

var (
	// ErrIntakeInvalid refuses a step, or a create, the intake cannot honour.
	ErrIntakeInvalid = errors.New("service: the intake is not ready")
	// ErrTemplateNoAssessment refuses a pipeline the intake has nowhere to
	// hang the assessment on.
	ErrTemplateNoAssessment = errors.New("service: that pipeline template has no assessment stage, so the intake has nowhere to attach the assessment")
)

// IntakeClient is step one: who the client is and what they asked for.
type IntakeClient struct {
	Company          string `json:"company"`
	Industry         string `json:"industry"`
	ContactName      string `json:"contact_name"`
	ContactEmail     string `json:"contact_email"`
	ShortlistSLADays int    `json:"shortlist_sla_days"`
	Brief            string `json:"brief"`
}

// IntakeJob is step two: the role and the pipeline it will run.
type IntakeJob struct {
	Title        string    `json:"title"`
	Seniority    string    `json:"seniority"`
	Location     string    `json:"location"`
	RemotePolicy string    `json:"remote_policy"`
	SalaryMin    int       `json:"salary_min"`
	SalaryMax    int       `json:"salary_max"`
	TemplateID   uuid.UUID `json:"template_id"`
}

// IntakeAssessment is step four: where the coding question comes from and
// which problems ended up in the set.
type IntakeAssessment struct {
	Source string `json:"source"`
	// ProblemIDs is the set in order. Under IntakeSourceDefault each entry
	// is the slot of the same index, and the nil id means the slot still
	// holds whatever the default picker chose.
	ProblemIDs      []uuid.UUID `json:"problem_ids"`
	DurationMinutes int         `json:"duration_minutes"`
}

// IntakePayload is everything the five steps have collected so far.
type IntakePayload struct {
	Client     IntakeClient     `json:"client"`
	Job        IntakeJob        `json:"job"`
	Skills     []string         `json:"skills"`
	Assessment IntakeAssessment `json:"assessment"`
}

// IntakeDraft is one recruiter's intake in progress.
type IntakeDraft struct {
	ID        uuid.UUID
	Step      int
	Payload   IntakePayload
	UpdatedAt time.Time
}

// IntakeTemplate is a pipeline template the intake may build the job from.
type IntakeTemplate struct {
	ID   uuid.UUID
	Name string
	// Stages names the template's stages in order, which is the pipeline the
	// job will run.
	Stages []string
	// HasAssessment reports whether the pipeline has a stage the assessment
	// can attach to; without one the intake cannot finish.
	HasAssessment bool
}

// IntakeSlot is one place in the recommended set: the difficulty it has to
// fill, the problem holding it, and everything the recruiter may swap in.
type IntakeSlot struct {
	Difficulty string
	Chosen     Problem
	Choices    []Problem
}

// IntakeResult is what one intake created.
type IntakeResult struct {
	ClientCompanyID uuid.UUID
	ClientUserID    uuid.UUID
	JobID           uuid.UUID
	AssessmentID    uuid.UUID
	// InviteLink is the client contact's one-time password-set link; the
	// same link is mailed to them.
	InviteLink string
}

// IntakeService runs the client intake: a draft the recruiter can leave and
// come back to, and one transaction at the end of it that either creates the
// client, its first user, the job, and the assessment together or none of
// them.
type IntakeService struct {
	st      *store.Store
	q       Enqueuer
	baseURL string
}

// NewIntakeService builds the service. baseURL is where the client contact's
// invite link points.
func NewIntakeService(st *store.Store, q Enqueuer, baseURL string) *IntakeService {
	return &IntakeService{st: st, q: q, baseURL: baseURL}
}

// Open returns the caller's draft, starting one when they have none. One
// recruiter has one intake in flight, so coming back always resumes it.
func (s *IntakeService) Open(ctx context.Context, p Principal) (IntakeDraft, error) {
	if err := requireRecruiter(p); err != nil {
		return IntakeDraft{}, err
	}
	var out IntakeDraft
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		row, err := tx.Q.GetOpenIntakeDraft(ctx, p.UserID)
		if errors.Is(err, pgx.ErrNoRows) {
			row, err = tx.Q.CreateIntakeDraft(ctx, db.CreateIntakeDraftParams{
				OrgID: p.OrgID, CreatedBy: p.UserID, Step: IntakeStepClient, Payload: []byte(`{}`),
			})
		}
		if err != nil {
			return err
		}
		out, err = toIntakeDraft(row)
		return err
	})
	if err != nil {
		return IntakeDraft{}, wrapIntake("open intake", err)
	}
	return out, nil
}

// Draft loads one of the org's drafts.
func (s *IntakeService) Draft(ctx context.Context, p Principal, id uuid.UUID) (IntakeDraft, error) {
	if err := requireRecruiter(p); err != nil {
		return IntakeDraft{}, err
	}
	var out IntakeDraft
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		row, err := tx.Q.GetIntakeDraft(ctx, id)
		if err != nil {
			return err
		}
		out, err = toIntakeDraft(row)
		return err
	})
	if err != nil {
		return IntakeDraft{}, wrapIntake("read intake draft", err)
	}
	return out, nil
}

// SaveStep stores what step holds and moves the draft to goTo. Moving forward
// validates the step first; moving back only stores, so a recruiter is never
// trapped on a step they cannot finish yet.
func (s *IntakeService) SaveStep(ctx context.Context, p Principal, id uuid.UUID, step int, in IntakePayload, goTo int) (IntakeDraft, error) {
	if err := requireRecruiter(p); err != nil {
		return IntakeDraft{}, err
	}
	if step < IntakeStepClient || step > IntakeStepReview {
		return IntakeDraft{}, fmt.Errorf("%w: %d is not one of its steps", ErrIntakeInvalid, step)
	}
	var out IntakeDraft
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		row, err := tx.Q.GetIntakeDraft(ctx, id)
		if err != nil {
			return err
		}
		stored, err := toIntakeDraft(row)
		if err != nil {
			return err
		}
		merged := mergeIntakeStep(stored.Payload, step, in)
		if goTo > step {
			if err := validateIntakeStep(ctx, tx, p, step, merged); err != nil {
				return err
			}
		}
		payload, err := json.Marshal(merged)
		if err != nil {
			return err
		}
		saved, err := tx.Q.SaveIntakeDraft(ctx, db.SaveIntakeDraftParams{
			ID: id, Step: int32(clampIntakeStep(goTo)), Payload: payload,
		})
		if err != nil {
			return err
		}
		out, err = toIntakeDraft(saved)
		return err
	})
	if err != nil {
		return IntakeDraft{}, wrapIntake("save intake step", err)
	}
	return out, nil
}

// Discard throws the draft away.
func (s *IntakeService) Discard(ctx context.Context, p Principal, id uuid.UUID) error {
	if err := requireRecruiter(p); err != nil {
		return err
	}
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		n, err := tx.Q.DeleteIntakeDraft(ctx, id)
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		return nil
	})
	return wrapIntake("discard intake draft", err)
}

// Templates lists the pipelines the job may be built from, the org's default
// first, each with the stages it would give the job.
func (s *IntakeService) Templates(ctx context.Context, p Principal) ([]IntakeTemplate, error) {
	if err := requireRecruiter(p); err != nil {
		return nil, err
	}
	var out []IntakeTemplate
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		var err error
		out, err = listIntakeTemplates(ctx, tx, p.OrgID)
		return err
	})
	if err != nil {
		return nil, wrapIntake("list pipeline templates", err)
	}
	return out, nil
}

// Set is step four's recommended set: one slot per difficulty the job's
// seniority calls for, each holding a problem and every swap for it.
func (s *IntakeService) Set(ctx context.Context, p Principal, id uuid.UUID) ([]IntakeSlot, error) {
	if err := requireRecruiter(p); err != nil {
		return nil, err
	}
	var out []IntakeSlot
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		row, err := tx.Q.GetIntakeDraft(ctx, id)
		if err != nil {
			return err
		}
		draft, err := toIntakeDraft(row)
		if err != nil {
			return err
		}
		out, err = intakeSlots(ctx, tx, draft.Payload)
		return err
	})
	if err != nil {
		return nil, wrapIntake("build the recommended set", err)
	}
	return out, nil
}

// Create runs the whole intake in one transaction: the client company, its
// first user with an invite on the way, the job with its pipeline, and the
// assessment attached to the pipeline's assessment stage. Anything the
// database or the assessment rules refuse leaves none of it behind, and the
// draft survives so the recruiter can fix what was wrong.
func (s *IntakeService) Create(ctx context.Context, p Principal, id uuid.UUID) (IntakeResult, error) {
	if err := requireRecruiter(p); err != nil {
		return IntakeResult{}, err
	}
	var out IntakeResult
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		row, err := tx.Q.GetIntakeDraft(ctx, id)
		if err != nil {
			return err
		}
		draft, err := toIntakeDraft(row)
		if err != nil {
			return err
		}
		in := draft.Payload
		for step := IntakeStepClient; step <= IntakeStepQuestion; step++ {
			if err := validateIntakeStep(ctx, tx, p, step, in); err != nil {
				return err
			}
		}
		problems, err := intakeProblems(ctx, tx, in)
		if err != nil {
			return err
		}

		company, err := tx.Q.CreateClientCompany(ctx, db.CreateClientCompanyParams{
			OrgID: p.OrgID, Name: strings.TrimSpace(in.Client.Company),
			Industry:         strings.TrimSpace(in.Client.Industry),
			ShortlistSlaDays: nullableInt(in.Client.ShortlistSLADays),
			Brief:            strings.TrimSpace(in.Client.Brief),
		})
		if err != nil {
			return err
		}
		out.ClientCompanyID = company.ID

		if err := s.inviteContact(ctx, tx, p, company.ID, in.Client, &out); err != nil {
			return err
		}

		job, err := tx.Q.CreateJob(ctx, db.CreateJobParams{
			OrgID: p.OrgID, ClientCompanyID: company.ID,
			Title: strings.TrimSpace(in.Job.Title), Slug: Slugify(in.Job.Title),
			Description: intakeDescription(in), Skills: cleanTags(in.Skills),
			Seniority: nullable(in.Job.Seniority), Location: nullable(strings.TrimSpace(in.Job.Location)),
			RemotePolicy: nullable(in.Job.RemotePolicy),
			SalaryMin:    nullableInt(in.Job.SalaryMin), SalaryMax: nullableInt(in.Job.SalaryMax),
			Status:    JobDraft,
			CreatedBy: uuid.NullUUID{UUID: p.UserID, Valid: p.UserID != uuid.Nil},
		})
		if err != nil {
			return err
		}
		out.JobID = job.ID
		if err := copyTemplateStages(ctx, tx, p.OrgID, job.ID, in.Job.TemplateID); err != nil {
			return err
		}
		stages, err := listStages(ctx, tx, job.ID)
		if err != nil {
			return err
		}
		stageID := firstAssessmentStage(stages)
		if stageID == uuid.Nil {
			return ErrTemplateNoAssessment
		}

		assessmentID, err := s.createAssessment(ctx, tx, p, in, job.Title, problems)
		if err != nil {
			return err
		}
		out.AssessmentID = assessmentID
		if _, err := tx.Q.SetStageAssessment(ctx, db.SetStageAssessmentParams{
			ID: stageID, JobID: job.ID, AssessmentID: uuid.NullUUID{UUID: assessmentID, Valid: true},
		}); err != nil {
			return err
		}
		_, err = tx.Q.DeleteIntakeDraft(ctx, id)
		return err
	})
	if err != nil {
		return IntakeResult{}, wrapIntake("create intake", err)
	}
	return out, nil
}

// inviteContact adds the client's first portal user and queues their invite
// in the same transaction, so a client company can never exist with nobody
// able to sign in to it.
func (s *IntakeService) inviteContact(ctx context.Context, tx *store.Tx, p Principal, companyID uuid.UUID, in IntakeClient, out *IntakeResult) error {
	user, err := tx.Q.CreateClientUser(ctx, db.CreateClientUserParams{
		OrgID: p.OrgID, ClientCompanyID: companyID,
		Email: strings.TrimSpace(in.ContactEmail), Name: strings.TrimSpace(in.ContactName), Timezone: "UTC",
	})
	if err != nil {
		return err
	}
	out.ClientUserID = user.ID
	token, _, err := issuePasswordSet(ctx, tx, p.OrgID, uuid.NullUUID{}, uuid.NullUUID{UUID: user.ID, Valid: true})
	if err != nil {
		return err
	}
	// The link lands on the client portal, which is the only surface this
	// account can sign in to.
	out.InviteLink = strings.TrimRight(s.baseURL, "/") + "/client/reset/" + token
	return enqueued(s.q.Enqueue(ctx, tx, queue.KindEmailSend, queue.EmailPayload{
		Template: mail.TemplatePasswordReset,
		To:       user.Email,
		OrgID:    p.OrgID,
		Data:     map[string]any{"ResetURL": out.InviteLink, "BaseURL": s.baseURL},
	}))
}

// createAssessment stores the intake's problem set as the job's assessment.
func (s *IntakeService) createAssessment(ctx context.Context, tx *store.Tx, p Principal, in IntakePayload, jobTitle string, problems []Problem) (uuid.UUID, error) {
	integrity, err := json.Marshal(IntegritySettings{})
	if err != nil {
		return uuid.Nil, err
	}
	ids := make([]uuid.UUID, 0, len(problems))
	for _, pr := range problems {
		ids = append(ids, pr.ID)
	}
	row, err := tx.Q.CreateAssessment(ctx, db.CreateAssessmentParams{
		OrgID: p.OrgID, Name: jobTitle + " assessment",
		DurationMinutes:  int32(intakeDuration(in, problems)),
		LanguageOverride: nil,
		InviteWindowDays: int32(DefaultSettings().AssessmentInviteDays),
		AllowedLanguages: []string{},
		Integrity:        integrity,
	})
	if err != nil {
		return uuid.Nil, err
	}
	if err := writeAssessmentProblems(ctx, tx, p.OrgID, row.ID, nil, ids); err != nil {
		return uuid.Nil, err
	}
	return row.ID, nil
}

// intakeDuration is how long the sitting runs: what the recruiter set, else
// what the problems recommend between them, else the platform default.
func intakeDuration(in IntakePayload, problems []Problem) int {
	if in.Assessment.DurationMinutes > 0 {
		return in.Assessment.DurationMinutes
	}
	total := 0
	for _, p := range problems {
		total += p.RecommendedMinutes
	}
	if total <= 0 {
		return DefaultAssessmentMinutes
	}
	return total
}

// intakeDescription is the job description the intake writes: the client's
// own words, which is the only prose the wizard collects.
func intakeDescription(in IntakePayload) string {
	return strings.TrimSpace(in.Client.Brief)
}

// intakeProblems resolves the set the assessment will carry. The recommended
// set is recomputed from the bank so a slot nobody swapped still holds the
// current best answer.
func intakeProblems(ctx context.Context, tx *store.Tx, in IntakePayload) ([]Problem, error) {
	if in.Assessment.Source != IntakeSourceDefault {
		out := make([]Problem, 0, len(in.Assessment.ProblemIDs))
		for _, id := range in.Assessment.ProblemIDs {
			row, err := tx.Q.GetProblem(ctx, id)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, fmt.Errorf("%w: a problem in the set no longer exists", ErrIntakeInvalid)
			}
			if err != nil {
				return nil, err
			}
			out = append(out, toProblem(row))
		}
		return out, nil
	}
	slots, err := intakeSlots(ctx, tx, in)
	if err != nil {
		return nil, err
	}
	out := make([]Problem, 0, len(slots))
	for _, slot := range slots {
		if slot.Chosen.ID != uuid.Nil {
			out = append(out, slot.Chosen)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: the bank has nothing this job's seniority can be tested with", ErrIntakeInvalid)
	}
	return out, nil
}

// intakeSlots builds the recommended set: the difficulties the seniority asks
// for, each holding the recruiter's swap when they made one and the picker's
// choice when they did not.
func intakeSlots(ctx context.Context, tx *store.Tx, in IntakePayload) ([]IntakeSlot, error) {
	bank, err := intakeBank(ctx, tx)
	if err != nil {
		return nil, err
	}
	byID := make(map[uuid.UUID]Problem, len(bank))
	set := make([]domain.SetProblem, 0, len(bank))
	for _, p := range bank {
		byID[p.ID] = p
		set = append(set, domain.SetProblem{ID: p.ID, Title: p.Title, Difficulty: p.Difficulty, Tags: p.Tags, Quality: p.Quality})
	}
	defaults := domain.DefaultSet(set, in.Skills, in.Job.Seniority)
	difficulties := domain.DefaultSetDifficulties(in.Job.Seniority)

	out := make([]IntakeSlot, 0, len(difficulties))
	for i, difficulty := range difficulties {
		slot := IntakeSlot{Difficulty: difficulty}
		for _, choice := range domain.RankSetProblems(set, in.Skills, difficulty) {
			slot.Choices = append(slot.Choices, byID[choice.ID])
		}
		if picked := intakeSlotPick(in.Assessment, i); picked != uuid.Nil {
			if p, ok := byID[picked]; ok && p.Difficulty == difficulty {
				slot.Chosen = p
			}
		}
		if slot.Chosen.ID == uuid.Nil && i < len(defaults) {
			slot.Chosen = byID[defaults[i].ID]
		}
		out = append(out, slot)
	}
	return out, nil
}

// intakeSlotPick is the problem the recruiter swapped into slot i, or nil.
func intakeSlotPick(in IntakeAssessment, i int) uuid.UUID {
	if in.Source != IntakeSourceDefault || i >= len(in.ProblemIDs) {
		return uuid.Nil
	}
	return in.ProblemIDs[i]
}

// intakeBank is every problem the org may build a set from: its own and the
// platform seed, code problems only, since a set is what a candidate codes.
func intakeBank(ctx context.Context, tx *store.Tx) ([]Problem, error) {
	rows, err := tx.Q.FilterProblems(ctx, db.FilterProblemsParams{Kind: domain.ProblemKindCode, RowLimit: ProblemListLimit})
	if err != nil {
		return nil, err
	}
	out := make([]Problem, 0, len(rows))
	for _, r := range rows {
		p := toProblem(db.Problem{
			ID: r.ID, OrgID: r.OrgID, Kind: r.Kind, Title: r.Title, Statement: r.Statement,
			Difficulty: r.Difficulty, Tags: r.Tags, AllowedLanguages: r.AllowedLanguages,
			TimeLimitMs: r.TimeLimitMs, MemoryLimitKb: r.MemoryLimitKb,
			SqlSchema: r.SqlSchema, SqlSeed: r.SqlSeed,
			RecommendedMinutes: r.RecommendedMinutes, Guidelines: r.Guidelines,
			OriginProblemID: r.OriginProblemID, Quality: r.Quality,
			ProvenLanguages: r.ProvenLanguages,
			CreatedAt:       r.CreatedAt, UpdatedAt: r.UpdatedAt,
		})
		p.CaseCount = int(r.CaseCount)
		out = append(out, p)
	}
	return out, nil
}

func listIntakeTemplates(ctx context.Context, tx *store.Tx, orgID uuid.UUID) ([]IntakeTemplate, error) {
	rows, err := tx.Q.ListPipelineTemplates(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]IntakeTemplate, 0, len(rows))
	for _, row := range rows {
		stages, err := tx.Q.ListPipelineTemplateStages(ctx, row.ID)
		if err != nil {
			return nil, err
		}
		t := IntakeTemplate{ID: row.ID, Name: row.Name}
		for _, st := range stages {
			t.Stages = append(t.Stages, st.Name)
			if domain.StageKind(st.Kind) == domain.StageAssessment {
				t.HasAssessment = true
			}
		}
		out = append(out, t)
	}
	return out, nil
}

// firstAssessmentStage is the stage the intake's assessment attaches to: the
// earliest one a candidate reaches.
func firstAssessmentStage(stages []domain.Stage) uuid.UUID {
	for _, st := range stages {
		if st.Kind == domain.StageAssessment {
			return st.ID
		}
	}
	return uuid.Nil
}

// mergeIntakeStep folds one step's answers into the draft, leaving every
// other step as it was: a post carries its own panel, never the whole wizard.
func mergeIntakeStep(stored IntakePayload, step int, in IntakePayload) IntakePayload {
	switch step {
	case IntakeStepClient:
		stored.Client = in.Client
	case IntakeStepJob:
		stored.Job = in.Job
	case IntakeStepSkills:
		stored.Skills = cleanTags(in.Skills)
	case IntakeStepQuestion:
		stored.Assessment = in.Assessment
	}
	return stored
}

// validateIntakeStep refuses what the step cannot hand on: the create at the
// end of the wizard must not be the first place a missing answer surfaces.
func validateIntakeStep(ctx context.Context, tx *store.Tx, p Principal, step int, in IntakePayload) error {
	var problems []string
	switch step {
	case IntakeStepClient:
		if strings.TrimSpace(in.Client.Company) == "" {
			problems = append(problems, "the client company needs a name")
		}
		if strings.TrimSpace(in.Client.ContactName) == "" {
			problems = append(problems, "the hiring contact needs a name")
		}
		if email := strings.TrimSpace(in.Client.ContactEmail); email == "" || !strings.Contains(email, "@") {
			problems = append(problems, "the hiring contact needs an email address to be invited at")
		}
		if in.Client.ShortlistSLADays < 0 {
			problems = append(problems, "a shortlist SLA cannot be negative")
		}
	case IntakeStepJob:
		if strings.TrimSpace(in.Job.Title) == "" {
			problems = append(problems, "the job needs a title")
		} else if Slugify(in.Job.Title) == "" {
			problems = append(problems, "the title has no characters a URL slug can use")
		}
		if in.Job.Seniority != "" && !containsString(Seniorities, in.Job.Seniority) {
			problems = append(problems, "unknown seniority "+in.Job.Seniority)
		}
		if in.Job.RemotePolicy != "" && !containsString(RemotePolicies, in.Job.RemotePolicy) {
			problems = append(problems, "unknown remote policy "+in.Job.RemotePolicy)
		}
		if in.Job.SalaryMin < 0 || in.Job.SalaryMax < 0 {
			problems = append(problems, "a salary cannot be negative")
		}
		if in.Job.SalaryMin > 0 && in.Job.SalaryMax > 0 && in.Job.SalaryMin > in.Job.SalaryMax {
			problems = append(problems, "the salary band starts above where it ends")
		}
		if len(problems) == 0 {
			return intakeTemplateUsable(ctx, tx, p.OrgID, in.Job.TemplateID)
		}
	case IntakeStepSkills:
		if len(cleanTags(in.Skills)) == 0 {
			problems = append(problems, "pick at least one skill the assessment has to prove")
		}
	case IntakeStepQuestion:
		switch in.Assessment.Source {
		case IntakeSourceDefault:
		case IntakeSourceBank, IntakeSourceAuthor:
			if len(in.Assessment.ProblemIDs) == 0 {
				problems = append(problems, "pick at least one problem for the assessment")
			}
		default:
			problems = append(problems, "choose where the coding question comes from")
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrIntakeInvalid, strings.Join(problems, "; "))
	}
	return nil
}

// intakeTemplateUsable refuses a pipeline with nowhere to hang the
// assessment, at the step that chose it rather than at the create.
func intakeTemplateUsable(ctx context.Context, tx *store.Tx, orgID, templateID uuid.UUID) error {
	tmpl, err := pipelineTemplate(ctx, tx, orgID, templateID)
	if err != nil {
		return err
	}
	stages, err := tx.Q.ListPipelineTemplateStages(ctx, tmpl.ID)
	if err != nil {
		return err
	}
	for _, st := range stages {
		if domain.StageKind(st.Kind) == domain.StageAssessment {
			return nil
		}
	}
	return fmt.Errorf("%w: %q runs %d stages, none of them an assessment", ErrTemplateNoAssessment, tmpl.Name, len(stages))
}

func clampIntakeStep(step int) int {
	if step < IntakeStepClient {
		return IntakeStepClient
	}
	if step > IntakeStepReview {
		return IntakeStepReview
	}
	return step
}

func toIntakeDraft(row db.IntakeDraft) (IntakeDraft, error) {
	out := IntakeDraft{ID: row.ID, Step: clampIntakeStep(int(row.Step)), UpdatedAt: row.UpdatedAt.Time}
	if len(row.Payload) > 0 {
		if err := json.Unmarshal(row.Payload, &out.Payload); err != nil {
			return IntakeDraft{}, fmt.Errorf("%w: the stored draft cannot be read", ErrIntakeInvalid)
		}
	}
	return out, nil
}

// wrapIntake keeps the errors a step shows inline unwrapped; anything else
// gains the operation that failed.
func wrapIntake(what string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pgx.ErrNoRows):
		return ErrNotFound
	case errors.Is(err, ErrIntakeInvalid), errors.Is(err, ErrTemplateNoAssessment), errors.Is(err, ErrNoTemplate),
		errors.Is(err, ErrNotFound), errors.Is(err, ErrForbidden), errors.Is(err, ErrEmailTaken),
		errors.Is(err, ErrProblemQuality), errors.Is(err, ErrNoLanguage), errors.Is(err, ErrAssessmentInvalid),
		errors.Is(err, domain.ErrInvalidPipeline):
		return err
	}
	if mapped := wrapCreate(what, err); errors.Is(mapped, ErrEmailTaken) {
		return mapped
	}
	if mapped := wrapJob(what, err); errors.Is(mapped, ErrSlugTaken) {
		return mapped
	}
	return fmt.Errorf("%s: %w", what, err)
}
