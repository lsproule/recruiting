package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"recruiting/internal/domain"
	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

// Seniority levels a job may ask for.
const (
	SeniorityJunior = "junior"
	SeniorityMid    = "mid"
	SenioritySenior = "senior"
	SeniorityStaff  = "staff"
)

// Remote policies a job may offer.
const (
	RemoteRemote = "remote"
	RemoteHybrid = "hybrid"
	RemoteOnsite = "onsite"
)

// Job lifecycle states.
const (
	JobDraft  = "draft"
	JobOpen   = "open"
	JobClosed = "closed"
)

// Ordered choice lists for the job form.
var (
	Seniorities    = []string{SeniorityJunior, SeniorityMid, SenioritySenior, SeniorityStaff}
	RemotePolicies = []string{RemoteRemote, RemoteHybrid, RemoteOnsite}
	JobStatuses    = []string{JobDraft, JobOpen, JobClosed}
)

var (
	ErrTitleRequired = errors.New("service: a job needs a title")
	ErrSlugTaken     = errors.New("service: another job already uses that URL slug")
	ErrInvalidJob    = errors.New("service: the job details are not valid")
	ErrStageOccupied = errors.New("service: that stage still holds applications")
	ErrNoTemplate    = errors.New("service: the org has no default pipeline template")
)

// Job is one open role the org recruits for.
type Job struct {
	ID                uuid.UUID
	ClientCompanyID   uuid.UUID
	ClientCompanyName string
	Title             string
	Slug              string
	Description       string // markdown
	Skills            []string
	Seniority         string
	Location          string
	RemotePolicy      string
	SalaryMin         int
	SalaryMax         int
	BlindMode         bool
	Status            string
}

// NewJob is the job form: the same fields create and update a job. Slug is
// derived from Title when empty.
type NewJob struct {
	ClientCompanyID uuid.UUID
	// TemplateID is the process the pipeline is copied from; Nil takes the
	// org's default.
	TemplateID   uuid.UUID
	Title        string
	Slug         string
	Description  string
	Skills       []string
	Seniority    string
	Location     string
	RemotePolicy string
	SalaryMin    int
	SalaryMax    int
	BlindMode    bool
	Status       string
}

// StageInput is one stage of the per-job pipeline editor.
type StageInput struct {
	Name     string
	Kind     domain.StageKind
	Terminal domain.ApplicationStatus // required on terminal stages, empty otherwise
	Unblind  bool
	// DefaultVetterID names the interview stage's default interviewer; Nil
	// clears it. It is ignored on stages of any other kind.
	DefaultVetterID uuid.UUID
	// InterviewFormat and DurationMinutes are an interview stage's; zero
	// values take the domain defaults. Ignored on other kinds.
	InterviewFormat string
	DurationMinutes int
	// RoundSeconds and BreakSeconds are a sprint stage's clock; zero values
	// take the domain defaults. Ignored on other kinds.
	RoundSeconds int
	BreakSeconds int
}

// stage is the input as the domain reads it, normalised for its kind.
func (in StageInput) stage() domain.Stage {
	return domain.NormalizeStage(domain.Stage{
		Name: in.Name, Kind: in.Kind, Terminal: in.Terminal, Unblind: in.Unblind, DefaultVetterID: in.DefaultVetterID,
		InterviewFormat: in.InterviewFormat, DurationMinutes: in.DurationMinutes,
		RoundSeconds: in.RoundSeconds, BreakSeconds: in.BreakSeconds,
	})
}

// stageInputOf is the editor's view of a stored stage.
func stageInputOf(s domain.Stage) StageInput {
	return StageInput{
		Name: s.Name, Kind: s.Kind, Terminal: s.Terminal, Unblind: s.Unblind, DefaultVetterID: s.DefaultVetterID,
		InterviewFormat: s.InterviewFormat, DurationMinutes: s.DurationMinutes,
		RoundSeconds: s.RoundSeconds, BreakSeconds: s.BreakSeconds,
	}
}

// JobService is the recruiter surface: jobs and their pipelines.
type JobService struct{ st *store.Store }

func NewJobService(st *store.Store) *JobService { return &JobService{st: st} }

// requireRecruiter allows the roles that own hiring: recruiters and admins.
func requireRecruiter(p Principal) error {
	if p.Kind != PrincipalOrgUser || (!p.HasRole(RoleRecruiter) && !p.HasRole(RoleAdmin)) {
		return ErrForbidden
	}
	return nil
}

// ListJobs returns the org's jobs, newest first, with their client company.
func (s *JobService) ListJobs(ctx context.Context, p Principal) ([]Job, error) {
	if p.Kind != PrincipalOrgUser {
		return nil, ErrForbidden
	}
	var out []Job
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		names, err := companyNames(ctx, tx, p.OrgID)
		if err != nil {
			return err
		}
		rows, err := tx.Q.ListJobs(ctx, p.OrgID)
		if err != nil {
			return err
		}
		out = make([]Job, 0, len(rows))
		for _, row := range rows {
			out = append(out, toJob(row, names[row.ClientCompanyID]))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	return out, nil
}

// ClientCompanies lists the org's client companies for the job form. Managing
// them stays an admin screen; choosing one to recruit for is a recruiter's job.
func (s *JobService) ClientCompanies(ctx context.Context, p Principal) ([]ClientCompany, error) {
	if err := requireRecruiter(p); err != nil {
		return nil, err
	}
	var out []ClientCompany
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		rows, err := tx.Q.ListClientCompanies(ctx, p.OrgID)
		if err != nil {
			return err
		}
		out = make([]ClientCompany, 0, len(rows))
		for _, c := range rows {
			out = append(out, ClientCompany{ID: c.ID, Name: c.Name})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list client companies: %w", err)
	}
	return out, nil
}

// Job loads one of the org's jobs.
func (s *JobService) Job(ctx context.Context, p Principal, id uuid.UUID) (Job, error) {
	if p.Kind != PrincipalOrgUser {
		return Job{}, ErrForbidden
	}
	var out Job
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		row, err := tx.Q.GetJob(ctx, id)
		if err != nil {
			return err
		}
		names, err := companyNames(ctx, tx, p.OrgID)
		if err != nil {
			return err
		}
		out = toJob(row, names[row.ClientCompanyID])
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, fmt.Errorf("get job: %w", err)
	}
	return out, nil
}

// CreateJob writes the job and copies the org's default pipeline template into
// the job's own stage rows, so later template edits never reach applications.
func (s *JobService) CreateJob(ctx context.Context, p Principal, in NewJob) (Job, error) {
	if err := requireRecruiter(p); err != nil {
		return Job{}, err
	}
	in, err := cleanJob(in)
	if err != nil {
		return Job{}, err
	}
	var out Job
	err = s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		company, err := tx.Q.GetClientCompany(ctx, db.GetClientCompanyParams{ID: in.ClientCompanyID, OrgID: p.OrgID})
		if err != nil {
			return err
		}
		row, err := tx.Q.CreateJob(ctx, db.CreateJobParams{
			OrgID: p.OrgID, ClientCompanyID: company.ID, Title: in.Title, Slug: in.Slug,
			Description: in.Description, Skills: in.Skills,
			Seniority: nullable(in.Seniority), Location: nullable(in.Location),
			RemotePolicy: nullable(in.RemotePolicy),
			SalaryMin:    nullableInt(in.SalaryMin), SalaryMax: nullableInt(in.SalaryMax),
			BlindMode: in.BlindMode, Status: in.Status,
			CreatedBy: uuid.NullUUID{UUID: p.UserID, Valid: p.UserID != uuid.Nil},
		})
		if err != nil {
			return err
		}
		if _, err := copyTemplateStages(ctx, tx, p.OrgID, row.ID, in.TemplateID); err != nil {
			return err
		}
		out = toJob(row, company.Name)
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrCompanyRequired
	}
	if err != nil {
		return Job{}, wrapJob("create job", err)
	}
	return out, nil
}

// UpdateJob replaces the job's editable fields.
func (s *JobService) UpdateJob(ctx context.Context, p Principal, id uuid.UUID, in NewJob) (Job, error) {
	if err := requireRecruiter(p); err != nil {
		return Job{}, err
	}
	in, err := cleanJob(in)
	if err != nil {
		return Job{}, err
	}
	var out Job
	err = s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		company, err := tx.Q.GetClientCompany(ctx, db.GetClientCompanyParams{ID: in.ClientCompanyID, OrgID: p.OrgID})
		if err != nil {
			return err
		}
		row, err := tx.Q.UpdateJob(ctx, db.UpdateJobParams{
			ID: id, ClientCompanyID: company.ID, Title: in.Title, Slug: in.Slug,
			Description: in.Description, Skills: in.Skills,
			Seniority: nullable(in.Seniority), Location: nullable(in.Location),
			RemotePolicy: nullable(in.RemotePolicy),
			SalaryMin:    nullableInt(in.SalaryMin), SalaryMax: nullableInt(in.SalaryMax),
			BlindMode: in.BlindMode, Status: in.Status,
		})
		if err != nil {
			return err
		}
		out = toJob(row, company.Name)
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, wrapJob("update job", err)
	}
	return out, nil
}

// cleanJob trims the form, derives the slug, and rejects unknown enum values.
func cleanJob(in NewJob) (NewJob, error) {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return in, ErrTitleRequired
	}
	if in.ClientCompanyID == uuid.Nil {
		return in, ErrCompanyRequired
	}
	in.Slug = Slugify(firstNonEmpty(strings.TrimSpace(in.Slug), in.Title))
	if in.Slug == "" {
		return in, fmt.Errorf("%w: the title has no characters a URL slug can use", ErrInvalidJob)
	}
	in.Description = strings.TrimSpace(in.Description)
	in.Location = strings.TrimSpace(in.Location)
	in.Skills = cleanTags(in.Skills)
	if in.Status == "" {
		in.Status = JobDraft
	}
	for _, f := range []struct {
		name    string
		value   string
		allowed []string
		blankOK bool
	}{
		{"seniority", in.Seniority, Seniorities, true},
		{"remote policy", in.RemotePolicy, RemotePolicies, true},
		{"status", in.Status, JobStatuses, false},
	} {
		if f.value == "" && f.blankOK {
			continue
		}
		if !containsRole(f.allowed, f.value) {
			return in, fmt.Errorf("%w: %q is not a %s", ErrInvalidJob, f.value, f.name)
		}
	}
	if in.SalaryMin < 0 || in.SalaryMax < 0 {
		return in, fmt.Errorf("%w: a salary cannot be negative", ErrInvalidJob)
	}
	if in.SalaryMin > 0 && in.SalaryMax > 0 && in.SalaryMin > in.SalaryMax {
		return in, fmt.Errorf("%w: the salary band starts above where it ends", ErrInvalidJob)
	}
	return in, nil
}

// cleanTags trims, lowercases, and de-duplicates skill tags, keeping order.
func cleanTags(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, raw := range in {
		for _, tag := range strings.Split(raw, ",") {
			tag = strings.ToLower(strings.TrimSpace(tag))
			if tag == "" || seen[tag] {
				continue
			}
			seen[tag] = true
			out = append(out, tag)
		}
	}
	return out
}

func companyNames(ctx context.Context, tx *store.Tx, orgID uuid.UUID) (map[uuid.UUID]string, error) {
	rows, err := tx.Q.ListClientCompanies(ctx, orgID)
	if err != nil {
		return nil, err
	}
	names := make(map[uuid.UUID]string, len(rows))
	for _, c := range rows {
		names[c.ID] = c.Name
	}
	return names, nil
}

func toJob(row db.Job, companyName string) Job {
	return Job{
		ID: row.ID, ClientCompanyID: row.ClientCompanyID, ClientCompanyName: companyName,
		Title: row.Title, Slug: row.Slug, Description: row.Description, Skills: row.Skills,
		Seniority: deref(row.Seniority), Location: deref(row.Location),
		RemotePolicy: deref(row.RemotePolicy),
		SalaryMin:    derefInt(row.SalaryMin), SalaryMax: derefInt(row.SalaryMax),
		BlindMode: row.BlindMode, Status: row.Status,
	}
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nullableInt(n int) *int32 {
	if n == 0 {
		return nil
	}
	v := int32(n)
	return &v
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func derefInt(n *int32) int {
	if n == nil {
		return 0
	}
	return int(*n)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// wrapJob maps a duplicate slug onto ErrSlugTaken.
func wrapJob(what string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation && pgErr.ConstraintName == "job_org_slug_idx" {
		return ErrSlugTaken
	}
	return fmt.Errorf("%s: %w", what, err)
}
