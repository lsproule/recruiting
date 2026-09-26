package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"recruiting/internal/mail"
	"recruiting/internal/queue"
	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

// SearchLimit caps a candidate search; the list screen is a working view, not
// an export.
const SearchLimit = 200

// Application event kinds this package writes.
const eventApplied = "applied"

var (
	ErrAlreadyApplied = errors.New("service: you have already applied to this job")
	ErrNoStages       = errors.New("service: that job has no pipeline stages yet")
)

// Candidate is one person the org has in play.
type Candidate struct {
	ID               uuid.UUID
	Email            string
	Name             string
	Phone            string
	Links            []string
	ApplicationCount int
	// Pipeline is where each of their applications stands, newest first,
	// as one line per role ("Backend Engineer · Take-home").
	Pipeline []string
	// Headline, Skills, Location, and InNetwork come from their talent
	// network profile when they have one; empty otherwise.
	Headline  string
	Skills    []string
	Location  string
	InNetwork bool
	CreatedAt time.Time
}

// NetworkProfile is the candidate's own talent-network profile as the
// recruiter's page shows it.
type NetworkProfile struct {
	Headline, Seniority, Location, RemotePolicy string
	Skills                                      []string
	JoinedAt                                    time.Time
	Withdrawn                                   bool
}

// CandidateApplication is one of a candidate's applications, named by the job
// and stage it sits in.
type CandidateApplication struct {
	ID        uuid.UUID
	JobID     uuid.UUID
	JobTitle  string
	JobSlug   string
	StageName string
	Status    string
}

// CandidateDetail is the candidate screen: the person, their applications,
// and the resumes they have sent.
type CandidateDetail struct {
	Candidate    Candidate
	Applications []CandidateApplication
	Resumes      []Resume
	// Network is their talent-network profile; nil when they never joined.
	Network *NetworkProfile
}

// PublicJob is what the unauthenticated apply page may show. It deliberately
// omits the client company, which blind jobs must not reveal.
type PublicJob struct {
	ID           uuid.UUID
	Title        string
	Slug         string
	OrgSlug      string
	Description  string
	Location     string
	RemotePolicy string
	Seniority    string
}

// ApplyInput is the public apply form.
type ApplyInput struct {
	Name   string
	Email  string
	Phone  string
	Links  []string
	Resume ResumeUpload
}

// NewCandidate is the recruiter's manual add. JobID and Resume are optional:
// a candidate may be filed before there is a role for them.
type NewCandidate struct {
	Name   string
	Email  string
	Phone  string
	Links  []string
	JobID  uuid.UUID
	Resume *ResumeUpload
}

// CandidateService owns candidates, their applications, and the public apply
// flow.
type CandidateService struct {
	st      *store.Store
	resumes *ResumeService
	q       Enqueuer
}

// NewCandidateService wires the store, the resume service, and the queue the
// apply acknowledgement goes to. A nil queue disables the acknowledgement.
func NewCandidateService(st *store.Store, resumes *ResumeService, q Enqueuer) *CandidateService {
	return &CandidateService{st: st, resumes: resumes, q: q}
}

// PublicJob loads the job behind an apply URL. Only open jobs are visible, so
// a draft or closed role is indistinguishable from a typo.
func (s *CandidateService) PublicJob(ctx context.Context, orgSlug, jobSlug string) (PublicJob, error) {
	row, err := s.publicJobRow(ctx, orgSlug, jobSlug)
	if err != nil {
		return PublicJob{}, err
	}
	return PublicJob{
		ID: row.ID, Title: row.Title, Slug: row.Slug, OrgSlug: strings.ToLower(strings.TrimSpace(orgSlug)),
		Description: row.Description,
		Location:    deref(row.Location), RemotePolicy: deref(row.RemotePolicy),
		Seniority: deref(row.Seniority),
	}, nil
}

func (s *CandidateService) publicJobRow(ctx context.Context, orgSlug, jobSlug string) (db.Job, error) {
	orgSlug = strings.TrimSpace(strings.ToLower(orgSlug))
	jobSlug = strings.TrimSpace(strings.ToLower(jobSlug))
	if orgSlug == "" || jobSlug == "" {
		return db.Job{}, ErrNotFound
	}
	var row db.Job
	err := s.st.WithPublicJobTx(ctx, orgSlug, jobSlug, func(ctx context.Context, tx *store.Tx) error {
		var err error
		row, err = tx.Q.FindPublicJobBySlug(ctx, db.FindPublicJobBySlugParams{OrgSlug: orgSlug, JobSlug: jobSlug})
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, store.ErrNoPublicJob) {
		return db.Job{}, ErrNotFound
	}
	if err != nil {
		return db.Job{}, fmt.Errorf("public job: %w", err)
	}
	return row, nil
}

// Apply is the unauthenticated intake. The applicant has no session, so the
// org comes from the job the URL named. The object store is written first
// because it cannot join the transaction; everything in Postgres — the
// candidate, the resume row, the application, its event, and the queued
// acknowledgement email — commits or rolls back together, and a rollback
// takes the object with it.
func (s *CandidateService) Apply(ctx context.Context, orgSlug, jobSlug string, in ApplyInput) (Candidate, error) {
	job, err := s.publicJobRow(ctx, orgSlug, jobSlug)
	if err != nil {
		return Candidate{}, err
	}
	who, err := cleanPerson(in.Name, in.Email, in.Phone, in.Links)
	if err != nil {
		return Candidate{}, err
	}
	who.public = true
	p := Principal{Kind: PrincipalSystem, OrgID: job.OrgID}
	stored, err := s.resumes.prepare(ctx, job.OrgID, in.Resume)
	if err != nil {
		return Candidate{}, err
	}
	cand, err := s.write(ctx, p, who, &job, &stored, "candidate", uuid.Nil)
	if err != nil {
		s.resumes.discard(context.WithoutCancel(ctx), stored.key)
		return Candidate{}, err
	}
	return cand, nil
}

// Add is the recruiter's manual entry: the same write, with the application
// and the resume both optional.
func (s *CandidateService) Add(ctx context.Context, p Principal, in NewCandidate) (Candidate, error) {
	if err := requireRecruiter(p); err != nil {
		return Candidate{}, err
	}
	who, err := cleanPerson(in.Name, in.Email, in.Phone, in.Links)
	if err != nil {
		return Candidate{}, err
	}
	var job *db.Job
	if in.JobID != uuid.Nil {
		row, err := s.job(ctx, p, in.JobID)
		if err != nil {
			return Candidate{}, err
		}
		job = &row
	}
	var stored *storedResume
	if in.Resume != nil {
		r, err := s.resumes.prepare(ctx, p.OrgID, *in.Resume)
		if err != nil {
			return Candidate{}, err
		}
		stored = &r
	}
	cand, err := s.write(ctx, p, who, job, stored, "org_user", p.UserID)
	if err != nil {
		if stored != nil {
			s.resumes.discard(context.WithoutCancel(ctx), stored.key)
		}
		return Candidate{}, err
	}
	return cand, nil
}

// Search lists the org's candidates, newest first, or the matches for query
// ranked by relevance across name and resume text.
func (s *CandidateService) Search(ctx context.Context, p Principal, query string) ([]Candidate, error) {
	if p.Kind != PrincipalOrgUser {
		return nil, ErrForbidden
	}
	var out []Candidate
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		rows, err := tx.Q.SearchCandidates(ctx, db.SearchCandidatesParams{
			OrgID: p.OrgID, Query: strings.TrimSpace(query), RowLimit: SearchLimit,
		})
		if err != nil {
			return err
		}
		out = make([]Candidate, 0, len(rows))
		for _, r := range rows {
			c := Candidate{
				ID: r.ID, Email: r.Email, Name: r.Name, Phone: deref(r.Phone),
				Links: decodeLinks(r.Links), ApplicationCount: int(r.ApplicationCount),
				Headline: r.Headline, Skills: r.Skills, Location: r.Location, InNetwork: r.InNetwork,
				CreatedAt: r.CreatedAt.Time.UTC(),
			}
			if r.Pipeline != "" {
				c.Pipeline = strings.Split(r.Pipeline, "; ")
			}
			out = append(out, c)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("search candidates: %w", err)
	}
	return out, nil
}

// Detail loads one candidate with their applications and resumes.
func (s *CandidateService) Detail(ctx context.Context, p Principal, id uuid.UUID) (CandidateDetail, error) {
	if p.Kind != PrincipalOrgUser {
		return CandidateDetail{}, ErrForbidden
	}
	var out CandidateDetail
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		row, err := tx.Q.GetCandidate(ctx, id)
		if err != nil {
			return err
		}
		out.Candidate = Candidate{
			ID: row.ID, Email: row.Email, Name: row.Name, Phone: deref(row.Phone),
			Links: decodeLinks(row.Links), CreatedAt: row.CreatedAt.Time.UTC(),
		}
		switch profile, err := tx.Q.GetCandidateNetworkProfile(ctx, id); {
		case err == nil:
			out.Network = &NetworkProfile{
				Headline: profile.Headline, Seniority: deref(profile.Seniority), Location: deref(profile.Location),
				RemotePolicy: deref(profile.RemotePolicy), Skills: profile.Skills,
				JoinedAt: profile.ConsentAt.Time.UTC(), Withdrawn: profile.WithdrawnAt.Valid,
			}
			out.Candidate.Headline, out.Candidate.Skills, out.Candidate.Location = profile.Headline, profile.Skills, deref(profile.Location)
			out.Candidate.InNetwork = !profile.WithdrawnAt.Valid
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		apps, err := tx.Q.ListCandidateApplications(ctx, id)
		if err != nil {
			return err
		}
		out.Applications = make([]CandidateApplication, 0, len(apps))
		for _, a := range apps {
			out.Applications = append(out.Applications, CandidateApplication{
				ID: a.ID, JobID: a.JobID, JobTitle: a.JobTitle, JobSlug: a.JobSlug,
				StageName: a.StageName, Status: a.Status,
			})
		}
		out.Candidate.ApplicationCount = len(out.Applications)
		resumes, err := tx.Q.ListResumesByCandidate(ctx, id)
		if err != nil {
			return err
		}
		out.Resumes = make([]Resume, 0, len(resumes))
		for _, r := range resumes {
			out.Resumes = append(out.Resumes, toResume(r))
		}
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return CandidateDetail{}, ErrNotFound
	}
	if err != nil {
		return CandidateDetail{}, fmt.Errorf("get candidate: %w", err)
	}
	return out, nil
}

// ResumeURL signs a download link for one of the org's resumes.
func (s *CandidateService) ResumeURL(ctx context.Context, p Principal, candidateID, resumeID uuid.UUID) (string, error) {
	return s.resumes.DownloadURL(ctx, p, candidateID, resumeID)
}

// job loads a job the caller may file an application against.
func (s *CandidateService) job(ctx context.Context, p Principal, id uuid.UUID) (db.Job, error) {
	var row db.Job
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		var err error
		row, err = tx.Q.GetJob(ctx, id)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Job{}, ErrNotFound
	}
	if err != nil {
		return db.Job{}, fmt.Errorf("get job: %w", err)
	}
	return row, nil
}

// person is a validated, normalised applicant.
type person struct {
	name   string
	email  string
	phone  *string
	links  []byte
	public bool // came from the apply form, which may not rename anyone
}

// write commits the whole intake in one transaction: the candidate upsert,
// the resume row, the application, and the event that filed it. Anything that
// would make the application impossible — a job with no stages, a repeat
// application — is checked before the first write.
func (s *CandidateService) write(ctx context.Context, p Principal, who person, job *db.Job, resume *storedResume, actorKind string, actorID uuid.UUID) (Candidate, error) {
	var out Candidate
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		var stage db.Stage
		if job != nil {
			var err error
			stage, err = tx.Q.FirstStage(ctx, job.ID)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNoStages
			}
			if err != nil {
				return err
			}
		}
		cand, err := upsertCandidate(ctx, tx, p.OrgID, who)
		if err != nil {
			return err
		}
		if job != nil {
			n, err := tx.Q.CountCandidateApplications(ctx, db.CountCandidateApplicationsParams{JobID: job.ID, CandidateID: cand.ID})
			if err != nil {
				return err
			}
			if n > 0 {
				return ErrAlreadyApplied
			}
		}
		if resume != nil {
			if _, err := insertResume(ctx, tx, p.OrgID, cand.ID, *resume); err != nil {
				return err
			}
		}
		if job != nil {
			app, err := tx.Q.CreateApplication(ctx, db.CreateApplicationParams{
				OrgID: p.OrgID, JobID: job.ID, CandidateID: cand.ID,
				ClientCompanyID: job.ClientCompanyID, StageID: stage.ID, ScreeningAnswers: []byte("{}"),
			})
			if err != nil {
				return err
			}
			_, err = tx.Q.CreateApplicationEvent(ctx, db.CreateApplicationEventParams{
				OrgID: p.OrgID, ApplicationID: app.ID, ActorKind: actorKind,
				ActorID: uuid.NullUUID{UUID: actorID, Valid: actorID != uuid.Nil},
				Kind:    eventApplied, ToStageID: uuid.NullUUID{UUID: stage.ID, Valid: true},
				Payload: []byte("{}"),
			})
			if err != nil {
				return err
			}
			if who.public {
				if err := s.acknowledge(ctx, tx, p.OrgID, cand, job.Title); err != nil {
					return err
				}
			}
		}
		out = cand
		return nil
	})
	if err != nil {
		return Candidate{}, wrapApply("save candidate", err)
	}
	return out, nil
}

// acknowledge queues the apply_received email to the applicant inside the
// apply transaction, so a rolled-back application sends nothing.
func (s *CandidateService) acknowledge(ctx context.Context, tx *store.Tx, orgID uuid.UUID, cand Candidate, jobTitle string) error {
	if s.q == nil {
		return nil
	}
	org, err := tx.Q.GetOrg(ctx, orgID)
	if err != nil {
		return fmt.Errorf("apply acknowledgement org: %w", err)
	}
	return enqueued(s.q.Enqueue(ctx, tx, queue.KindEmailSend, queue.EmailPayload{
		Template: mail.TemplateApplyReceived, To: cand.Email, OrgID: orgID,
		Data: map[string]any{"CandidateName": cand.Name, "JobTitle": jobTitle, "OrgName": org.Name},
	}))
}

// upsertCandidate writes the candidate keyed on (org, lowercased email). The
// public form uses the variant that keeps the name already on file and merges
// the links, so a stranger who knows an address cannot rewrite the record.
func upsertCandidate(ctx context.Context, tx *store.Tx, orgID uuid.UUID, who person) (Candidate, error) {
	if who.public {
		row, err := tx.Q.UpsertCandidateFromApply(ctx, db.UpsertCandidateFromApplyParams{
			OrgID: orgID, Email: who.email, Name: who.name, Phone: who.phone, Links: who.links,
		})
		if err != nil {
			return Candidate{}, err
		}
		return Candidate{ID: row.ID, Email: row.Email, Name: row.Name, Phone: deref(row.Phone), Links: decodeLinks(row.Links)}, nil
	}
	row, err := tx.Q.UpsertCandidate(ctx, db.UpsertCandidateParams{
		OrgID: orgID, Email: who.email, Name: who.name, Phone: who.phone, Links: who.links,
	})
	if err != nil {
		return Candidate{}, err
	}
	return Candidate{ID: row.ID, Email: row.Email, Name: row.Name, Phone: deref(row.Phone), Links: decodeLinks(row.Links)}, nil
}

// cleanPerson trims and validates the shared candidate fields.
func cleanPerson(name, email, phone string, links []string) (person, error) {
	name = strings.TrimSpace(name)
	email = strings.ToLower(strings.TrimSpace(email))
	if err := requireNameEmail(name, email); err != nil {
		return person{}, err
	}
	out := person{name: name, email: email}
	if phone = strings.TrimSpace(phone); phone != "" {
		out.phone = &phone
	}
	encoded, err := json.Marshal(cleanLinks(links))
	if err != nil {
		return person{}, fmt.Errorf("%w: those links could not be read", ErrInvalidJob)
	}
	out.links = encoded
	return out, nil
}

// cleanLinks keeps the http(s) URLs an applicant listed, one per line or
// comma, and drops anything that is not one.
func cleanLinks(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, raw := range in {
		for _, field := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' || r == ' ' || r == '\t' }) {
			u, err := url.Parse(field)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || seen[field] {
				continue
			}
			seen[field] = true
			out = append(out, field)
		}
	}
	return out
}

func decodeLinks(raw []byte) []string {
	var out []string
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// wrapApply turns a racing duplicate application into the same friendly
// answer the pre-check gives.
func wrapApply(what string, err error) error {
	if errors.Is(err, ErrAlreadyApplied) || errors.Is(err, ErrNoStages) || errors.Is(err, ErrForbidden) {
		return err
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation && strings.HasPrefix(pgErr.ConstraintName, "application_job_id_candidate_id") {
		return ErrAlreadyApplied
	}
	return fmt.Errorf("%s: %w", what, err)
}
