package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"recruiting/internal/domain"
	"recruiting/internal/mail"
	"recruiting/internal/queue"
	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

// queueEmailKind and emailPayload name the queue job an email is.
const queueEmailKind = queue.KindEmailSend

func emailPayload(template, to string, orgID uuid.UUID, data map[string]any) queue.EmailPayload {
	return queue.EmailPayload{Template: template, To: to, OrgID: orgID, Data: data}
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation
}

// Link purposes of the talent network. A profile link is a member's own
// page, long-lived and re-issued on request; an opportunity link is the
// one-click answer to a role a recruiter sends.
const (
	LinkProfile     = "profile"
	LinkOpportunity = "opportunity"
)

// How long each link stays valid.
const (
	ProfileLinkTTL     = 180 * 24 * time.Hour
	OpportunityLinkTTL = 30 * 24 * time.Hour
)

// Where the candidate-facing pages live; the service builds the links the
// emails carry.
const (
	profilePath     = "/talent/profile/"
	opportunityPath = "/opportunity/"
	talentAppPath   = "/app/talent/requests/"
)

// Talent request and introduction statuses.
const (
	TalentRequestOpen   = "open"
	TalentRequestClosed = "closed"

	IntroRequested = "requested"
	IntroSent      = "sent"
	IntroAccepted  = "accepted"
	IntroDeclined  = "declined"
	IntroDismissed = "dismissed"
)

// TalentMatchLimit bounds the matching read; like the pool's ranking pass it
// reads the whole network rather than a page, and the cap guards the read.
const TalentMatchLimit = 10000

// TalentListLimit caps the recruiter's network listing.
const TalentListLimit = 200

var (
	ErrTalentConsent       = errors.New("service: joining the network needs your consent to be contacted about roles")
	ErrTalentSkills        = errors.New("service: name at least one skill")
	ErrTalentTitle         = errors.New("service: a talent request needs a title")
	ErrTalentRequestClosed = errors.New("service: that request is closed")
	ErrIntroRequested      = errors.New("service: an introduction to that person was already requested")
	ErrIntroNotWaiting     = errors.New("service: that introduction is not waiting to be sent")
	ErrIntroNotSent        = errors.New("service: that opportunity has already been answered or was withdrawn")
	ErrIntroJob            = errors.New("service: the job must be one of the requesting company's open jobs")
	ErrTalentJobRequired   = errors.New("service: pick the job the introduction opens an application on")
)

// PublicOrg is what the join page knows about the org: enough to say whose
// network it is.
type PublicOrg struct {
	ID   uuid.UUID
	Name string
	Slug string
}

// TalentProfileInput is the join form: who the person is, what they do, and
// what they want. Consent must be given; the rest is what they choose to
// say. Resume is optional but is what most of the matching reads.
type TalentProfileInput struct {
	Name, Email, Phone string
	Links              []string
	Headline           string
	Skills             []string
	Seniority          string
	Roles              []string
	Location           string
	RemotePolicy       string
	SalaryMin          int
	AvailableFrom      *time.Time
	Consent            bool
	Resume             *ResumeUpload
}

// TalentProfile is one member of the network as the org and the member see
// it. The company never sees this type; it sees a TalentMatch.
type TalentProfile struct {
	ID            uuid.UUID
	CandidateID   uuid.UUID
	Name          string
	Email         string
	Phone         string
	Links         []string
	Headline      string
	Skills        []string
	Seniority     string
	Roles         []string
	Location      string
	RemotePolicy  string
	SalaryMin     int
	AvailableFrom *time.Time
	ConsentAt     time.Time
	Withdrawn     bool
	HasResume     bool
	UpdatedAt     time.Time
}

// TalentRequestInput is what a company files: the role in a few fields, and
// optionally the job an accepted introduction lands on.
type TalentRequestInput struct {
	Title        string
	Skills       []string
	Seniority    string
	Location     string
	RemotePolicy string
	Note         string
	JobID        uuid.UUID
}

// TalentRequest is one filed request with its counts.
type TalentRequest struct {
	ID                uuid.UUID
	orgID             uuid.UUID
	ClientCompanyID   uuid.UUID
	ClientCompanyName string
	JobID             uuid.UUID
	JobTitle          string
	Title             string
	Skills            []string
	Seniority         string
	Location          string
	RemotePolicy      string
	Note              string
	Status            string
	CreatedAt         time.Time
	IntroCount        int
	WaitingCount      int
}

// Open reports whether the request still takes introductions.
func (r TalentRequest) Open() bool { return r.Status == TalentRequestOpen }

// TalentMatch is one person the matcher found for a request, as the company
// sees them: what they do and want, never who they are. ID is the handle an
// introduction is requested by; it is the profile's or pool entry's id, which
// names nobody outside this service.
type TalentMatch struct {
	ID            uuid.UUID
	Source        string
	Score         float64
	Breakdown     domain.TalentBreakdown
	SharedSkills  []string
	Skills        []string
	Seniority     string
	Location      string
	RemotePolicy  string
	Headline      string
	AvailableFrom *time.Time
	HasResume     bool
	// IntroStatus is set when the company already asked to meet this
	// person, so the card shows where that stands instead of the button.
	IntroStatus string
	// candidateID is kept for the introduction; it never leaves the service.
	candidateID uuid.UUID
}

// TalentIntro is one introduction. The org's view carries the candidate's
// name; the company's (ClientIntro) does not until they accept.
type TalentIntro struct {
	ID                uuid.UUID
	RequestID         uuid.UUID
	RequestTitle      string
	ClientCompanyName string
	CandidateID       uuid.UUID
	CandidateName     string
	CandidateEmail    string
	Source            string
	Score             float64
	Status            string
	JobID             uuid.UUID
	ApplicationID     uuid.UUID
	RequestedAt       time.Time
	SentAt            *time.Time
	AnsweredAt        *time.Time
}

// ClientIntro is an introduction as the company reads it. Label stands in
// for the name until the person accepts; then the application is theirs to
// read like any released one.
type ClientIntro struct {
	ID            uuid.UUID
	MatchID       uuid.UUID
	Label         string
	Source        string
	Score         float64
	Status        string
	ApplicationID uuid.UUID
	RequestedAt   time.Time
	SentAt        *time.Time
	AnsweredAt    *time.Time
}

// Opportunity is the page a candidate answers from: the role, who it is
// with, and where their answer stands.
type Opportunity struct {
	IntroID           uuid.UUID
	CandidateName     string
	OrgName           string
	ClientCompanyName string
	RequestTitle      string
	JobTitle          string
	JobDescription    string
	Status            string
	ApplicationID     uuid.UUID
}

// TalentService is the talent network: people who joined it, what companies
// ask of it, and the introductions between the two.
type TalentService struct {
	st      *store.Store
	resumes *ResumeService
	links   *MagicLinkService
	q       Enqueuer
	baseURL string
	// Now is the clock; tests move it.
	Now func() time.Time
}

// NewTalentService wires the store, the résumé store, the link issuer, and
// the queue the emails go through. A nil queue drops the emails.
func NewTalentService(st *store.Store, resumes *ResumeService, links *MagicLinkService, q Enqueuer, baseURL string) *TalentService {
	return &TalentService{st: st, resumes: resumes, links: links, q: q, baseURL: strings.TrimRight(baseURL, "/"), Now: time.Now}
}

// Org resolves the join page's org slug. An unknown slug is ErrNotFound: the
// page must not say which orgs exist.
func (s *TalentService) Org(ctx context.Context, orgSlug string) (PublicOrg, error) {
	var out PublicOrg
	err := s.st.WithPublicOrgTx(ctx, orgSlug, func(ctx context.Context, tx *store.Tx) error {
		row, err := tx.Q.GetOrgBySlug(ctx, orgSlug)
		if err != nil {
			return err
		}
		out = PublicOrg{ID: row.ID, Name: row.Name, Slug: row.Slug}
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, store.ErrNoPublicOrg) {
		return PublicOrg{}, ErrNotFound
	}
	if err != nil {
		return PublicOrg{}, fmt.Errorf("talent org: %w", err)
	}
	return out, nil
}

// Join files a profile from the public page: the candidate is created or
// found by email (the public form never renames anyone), the résumé stored,
// the profile written with fresh consent, and the member mailed the link to
// their own page. A member who joins again gets their details replaced and
// a new link; the old one keeps working until it expires.
func (s *TalentService) Join(ctx context.Context, orgSlug string, in TalentProfileInput) (TalentProfile, error) {
	org, err := s.Org(ctx, orgSlug)
	if err != nil {
		return TalentProfile{}, err
	}
	if !in.Consent {
		return TalentProfile{}, ErrTalentConsent
	}
	who, err := cleanPerson(in.Name, in.Email, in.Phone, in.Links)
	if err != nil {
		return TalentProfile{}, err
	}
	who.public = true
	fields, err := cleanTalentFields(in)
	if err != nil {
		return TalentProfile{}, err
	}
	p := orgScoped(org.ID)
	var stored *storedResume
	if in.Resume != nil && !in.Resume.Empty() {
		if s.resumes == nil {
			return TalentProfile{}, ErrNoBlobStore
		}
		r, err := s.resumes.prepare(ctx, org.ID, *in.Resume)
		if err != nil {
			return TalentProfile{}, err
		}
		stored = &r
	}
	var out TalentProfile
	var profileID uuid.UUID
	err = s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		cand, err := upsertCandidate(ctx, tx, org.ID, who)
		if err != nil {
			return err
		}
		if stored != nil {
			if _, err := insertResume(ctx, tx, org.ID, cand.ID, *stored); err != nil {
				return err
			}
		}
		row, err := tx.Q.UpsertTalentProfile(ctx, db.UpsertTalentProfileParams{
			OrgID: org.ID, CandidateID: cand.ID, Headline: fields.headline, Skills: fields.skills,
			Seniority: fields.seniority, Roles: fields.roles, Location: fields.location,
			RemotePolicy: fields.remote, SalaryMin: fields.salary, AvailableFrom: fields.available,
		})
		if err != nil {
			return err
		}
		profileID = row.ID
		return nil
	})
	if err != nil {
		if stored != nil {
			s.resumes.discard(context.WithoutCancel(ctx), stored.key)
		}
		return TalentProfile{}, wrapTalent("join talent network", err)
	}
	if err := s.mailProfileLink(ctx, p, profileID, org.Name); err != nil {
		return TalentProfile{}, err
	}
	out, err = s.profile(ctx, p, profileID)
	if err != nil {
		return TalentProfile{}, err
	}
	return out, nil
}

// mailProfileLink issues a fresh profile link and queues the welcome email
// that carries it.
func (s *TalentService) mailProfileLink(ctx context.Context, p Principal, profileID uuid.UUID, orgName string) error {
	if s.links == nil || s.q == nil {
		return nil
	}
	token, _, err := s.links.Issue(ctx, p, LinkProfile, profileID, ProfileLinkTTL)
	if err != nil {
		return fmt.Errorf("talent profile link: %w", err)
	}
	err = s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		row, err := tx.Q.GetTalentProfile(ctx, profileID)
		if err != nil {
			return err
		}
		return enqueued(s.q.Enqueue(ctx, tx, queueEmailKind, emailPayload(mail.TemplateTalentWelcome, row.CandidateEmail, p.OrgID, map[string]any{
			"CandidateName": row.CandidateName,
			"OrgName":       orgName,
			"ProfileURL":    s.baseURL + profilePath + token,
		})))
	})
	if err != nil {
		return fmt.Errorf("talent welcome email: %w", err)
	}
	return nil
}

// Profile is the member's own page, reached by their profile link.
func (s *TalentService) Profile(ctx context.Context, p Principal) (TalentProfile, error) {
	if err := requireProfileLink(p); err != nil {
		return TalentProfile{}, err
	}
	return s.profile(ctx, orgScoped(p.OrgID), p.SubjectID)
}

// TalentProfileEdit is what a member may change from their page: everything
// but who they are, which the org's record owns.
type TalentProfileEdit struct {
	Phone         string
	Links         []string
	Headline      string
	Skills        []string
	Seniority     string
	Roles         []string
	Location      string
	RemotePolicy  string
	SalaryMin     int
	AvailableFrom *time.Time
	Resume        *ResumeUpload
}

// UpdateProfile rewrites the member's preferences, and their résumé when a
// new one is sent.
func (s *TalentService) UpdateProfile(ctx context.Context, p Principal, in TalentProfileEdit) (TalentProfile, error) {
	if err := requireProfileLink(p); err != nil {
		return TalentProfile{}, err
	}
	fields, err := cleanTalentFields(TalentProfileInput{
		Headline: in.Headline, Skills: in.Skills, Seniority: in.Seniority, Roles: in.Roles,
		Location: in.Location, RemotePolicy: in.RemotePolicy, SalaryMin: in.SalaryMin, AvailableFrom: in.AvailableFrom,
	})
	if err != nil {
		return TalentProfile{}, err
	}
	scope := orgScoped(p.OrgID)
	var stored *storedResume
	if in.Resume != nil && !in.Resume.Empty() {
		if s.resumes == nil {
			return TalentProfile{}, ErrNoBlobStore
		}
		r, err := s.resumes.prepare(ctx, p.OrgID, *in.Resume)
		if err != nil {
			return TalentProfile{}, err
		}
		stored = &r
	}
	err = s.st.WithTx(ctx, scope, func(ctx context.Context, tx *store.Tx) error {
		row, err := tx.Q.GetTalentProfile(ctx, p.SubjectID)
		if err != nil {
			return err
		}
		if _, err := tx.Q.UpdateTalentProfile(ctx, db.UpdateTalentProfileParams{
			ID: row.ID, Headline: fields.headline, Skills: fields.skills, Seniority: fields.seniority,
			Roles: fields.roles, Location: fields.location, RemotePolicy: fields.remote,
			SalaryMin: fields.salary, AvailableFrom: fields.available,
		}); err != nil {
			return err
		}
		who, err := cleanPerson(row.CandidateName, row.CandidateEmail, in.Phone, in.Links)
		if err != nil {
			return err
		}
		if _, err := tx.Q.UpsertCandidate(ctx, db.UpsertCandidateParams{
			OrgID: p.OrgID, Email: who.email, Name: who.name, Phone: who.phone, Links: who.links,
		}); err != nil {
			return err
		}
		if stored != nil {
			if _, err := insertResume(ctx, tx, p.OrgID, row.CandidateID, *stored); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if stored != nil {
			s.resumes.discard(context.WithoutCancel(ctx), stored.key)
		}
		return TalentProfile{}, wrapTalent("update talent profile", err)
	}
	return s.profile(ctx, scope, p.SubjectID)
}

// Withdraw takes the member out of every match. Their record stays so a
// later Rejoin, or a fresh join, finds it.
func (s *TalentService) Withdraw(ctx context.Context, p Principal) error {
	if err := requireProfileLink(p); err != nil {
		return err
	}
	err := s.st.WithTx(ctx, orgScoped(p.OrgID), func(ctx context.Context, tx *store.Tx) error {
		_, err := tx.Q.WithdrawTalentProfile(ctx, p.SubjectID)
		return err
	})
	if err != nil {
		return fmt.Errorf("withdraw talent profile: %w", err)
	}
	return nil
}

// Rejoin reverses a withdrawal with fresh consent.
func (s *TalentService) Rejoin(ctx context.Context, p Principal) error {
	if err := requireProfileLink(p); err != nil {
		return err
	}
	err := s.st.WithTx(ctx, orgScoped(p.OrgID), func(ctx context.Context, tx *store.Tx) error {
		_, err := tx.Q.RejoinTalentProfile(ctx, p.SubjectID)
		return err
	})
	if err != nil {
		return fmt.Errorf("rejoin talent network: %w", err)
	}
	return nil
}

// Profiles is the recruiter's view of the network, narrowed by a search.
func (s *TalentService) Profiles(ctx context.Context, p Principal, query string, includeWithdrawn bool) ([]TalentProfile, error) {
	if err := requireRecruiter(p); err != nil {
		return nil, err
	}
	var out []TalentProfile
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		rows, err := tx.Q.ListTalentProfiles(ctx, db.ListTalentProfilesParams{
			OrgID: p.OrgID, Query: strings.TrimSpace(query), IncludeWithdrawn: includeWithdrawn, RowLimit: TalentListLimit,
		})
		if err != nil {
			return err
		}
		out = make([]TalentProfile, 0, len(rows))
		for _, r := range rows {
			out = append(out, talentProfileFromList(r))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list talent profiles: %w", err)
	}
	return out, nil
}

// ProfileByID is one member as a recruiter reads them.
func (s *TalentService) ProfileByID(ctx context.Context, p Principal, id uuid.UUID) (TalentProfile, error) {
	if err := requireRecruiter(p); err != nil {
		return TalentProfile{}, err
	}
	return s.profile(ctx, p, id)
}

func (s *TalentService) profile(ctx context.Context, p Principal, id uuid.UUID) (TalentProfile, error) {
	var out TalentProfile
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		row, err := tx.Q.GetTalentProfile(ctx, id)
		if err != nil {
			return err
		}
		out = talentProfile(row)
		return nil
	})
	if err != nil {
		return TalentProfile{}, wrapTalent("talent profile", err)
	}
	return out, nil
}

// CreateRequest files what a company is looking for.
func (s *TalentService) CreateRequest(ctx context.Context, p Principal, in TalentRequestInput) (TalentRequest, error) {
	if err := requireClient(p); err != nil {
		return TalentRequest{}, err
	}
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return TalentRequest{}, ErrTalentTitle
	}
	in.Skills = cleanTags(in.Skills)
	if len(in.Skills) == 0 {
		return TalentRequest{}, ErrTalentSkills
	}
	if err := checkVocabulary(in.Seniority, Seniorities); err != nil {
		return TalentRequest{}, err
	}
	if err := checkVocabulary(in.RemotePolicy, RemotePolicies); err != nil {
		return TalentRequest{}, err
	}
	if err := checkRunes(in.Note, MaxMessageRunes); err != nil {
		return TalentRequest{}, err
	}
	var id uuid.UUID
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		params := db.CreateTalentRequestParams{
			OrgID: p.OrgID, ClientCompanyID: p.ClientCompanyID,
			ClientUserID: uuid.NullUUID{UUID: p.UserID, Valid: true},
			Title:        in.Title, Skills: in.Skills, Seniority: nullable(strings.ToLower(in.Seniority)),
			Location: nullable(strings.TrimSpace(in.Location)), RemotePolicy: nullable(strings.ToLower(in.RemotePolicy)),
			Note: strings.TrimSpace(in.Note),
		}
		if in.JobID != uuid.Nil {
			// The client scope sees only its own jobs, so a stranger's id is
			// simply not found.
			job, err := tx.Q.GetJob(ctx, in.JobID)
			if err != nil {
				return errIfNoRows(err, ErrIntroJob)
			}
			if job.Status != JobOpen {
				return ErrIntroJob
			}
			params.JobID = uuid.NullUUID{UUID: job.ID, Valid: true}
		}
		row, err := tx.Q.CreateTalentRequest(ctx, params)
		if err != nil {
			return err
		}
		id = row.ID
		return nil
	})
	if err != nil {
		return TalentRequest{}, wrapTalent("create talent request", err)
	}
	return s.request(ctx, p, id)
}

// Requests is the company's own requests, newest first.
func (s *TalentService) Requests(ctx context.Context, p Principal) ([]TalentRequest, error) {
	if err := requireClient(p); err != nil {
		return nil, err
	}
	var out []TalentRequest
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		rows, err := tx.Q.ListTalentRequestsByCompany(ctx, p.ClientCompanyID)
		if err != nil {
			return err
		}
		out = make([]TalentRequest, 0, len(rows))
		for _, r := range rows {
			out = append(out, talentRequest(db.ListTalentRequestsRow{
				ID: r.ID, OrgID: r.OrgID, ClientCompanyID: r.ClientCompanyID, ClientUserID: r.ClientUserID, JobID: r.JobID,
				Title: r.Title, Skills: r.Skills, Seniority: r.Seniority, Location: r.Location, RemotePolicy: r.RemotePolicy,
				Note: r.Note, Status: r.Status, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
				ClientCompanyName: r.ClientCompanyName, JobTitle: r.JobTitle, IntroCount: r.IntroCount,
			}))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list talent requests: %w", err)
	}
	return out, nil
}

// Request is one of the company's requests; anyone else's is not found.
func (s *TalentService) Request(ctx context.Context, p Principal, id uuid.UUID) (TalentRequest, error) {
	if err := requireClient(p); err != nil {
		return TalentRequest{}, err
	}
	return s.request(ctx, p, id)
}

// request reads one request in the caller's own scope, which is what
// proves a company may see it, and counts its introductions in the org
// scope: an introduction names a candidate the company cannot yet read.
func (s *TalentService) request(ctx context.Context, p Principal, id uuid.UUID) (TalentRequest, error) {
	var out TalentRequest
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		row, err := tx.Q.GetTalentRequest(ctx, id)
		if err != nil {
			return err
		}
		out = talentRequest(db.ListTalentRequestsRow{
			ID: row.ID, OrgID: row.OrgID, ClientCompanyID: row.ClientCompanyID, ClientUserID: row.ClientUserID, JobID: row.JobID,
			Title: row.Title, Skills: row.Skills, Seniority: row.Seniority, Location: row.Location, RemotePolicy: row.RemotePolicy,
			Note: row.Note, Status: row.Status, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
			ClientCompanyName: row.ClientCompanyName, JobTitle: row.JobTitle,
		})
		return nil
	})
	if err != nil {
		return TalentRequest{}, wrapTalent("talent request", err)
	}
	intros, err := s.introsOf(ctx, p.OrgID, id)
	if err != nil {
		return TalentRequest{}, err
	}
	out.IntroCount = len(intros)
	for _, i := range intros {
		if i.Status == IntroRequested {
			out.WaitingCount++
		}
	}
	return out, nil
}

// introsOf lists a request's introductions in the org scope. Callers have
// already established the request is theirs to read.
func (s *TalentService) introsOf(ctx context.Context, orgID, requestID uuid.UUID) ([]db.ListTalentIntrosByRequestRow, error) {
	var rows []db.ListTalentIntrosByRequestRow
	err := s.st.WithTx(ctx, orgScoped(orgID), func(ctx context.Context, tx *store.Tx) error {
		var err error
		rows, err = tx.Q.ListTalentIntrosByRequest(ctx, requestID)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("talent introductions: %w", err)
	}
	return rows, nil
}

// CloseRequest stops a request taking introductions; those already asked
// for run their course.
func (s *TalentService) CloseRequest(ctx context.Context, p Principal, id uuid.UUID) error {
	if err := requireClient(p); err != nil {
		return err
	}
	var n int64
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		if _, err := tx.Q.GetTalentRequest(ctx, id); err != nil {
			return err
		}
		var err error
		n, err = tx.Q.CloseTalentRequest(ctx, id)
		return err
	})
	if err != nil {
		return wrapTalent("close talent request", err)
	}
	if n == 0 {
		return ErrTalentRequestClosed
	}
	return nil
}

// Intros is the company's introductions on one request, unnamed until
// accepted.
func (s *TalentService) Intros(ctx context.Context, p Principal, requestID uuid.UUID) ([]ClientIntro, error) {
	if err := requireClient(p); err != nil {
		return nil, err
	}
	if _, err := s.request(ctx, p, requestID); err != nil {
		return nil, err
	}
	rows, err := s.introsOf(ctx, p.OrgID, requestID)
	if err != nil {
		return nil, err
	}
	out := make([]ClientIntro, 0, len(rows))
	for _, r := range rows {
		out = append(out, clientIntro(r))
	}
	return out, nil
}

// Matches ranks the network and the pool against one of the company's
// requests and returns them anonymised. The request is read in the client's
// own scope, which proves it is theirs; the matching reads people in the
// org scope, and only the fields a company may see come back.
func (s *TalentService) Matches(ctx context.Context, p Principal, requestID uuid.UUID) ([]TalentMatch, error) {
	if err := requireClient(p); err != nil {
		return nil, err
	}
	req, err := s.request(ctx, p, requestID)
	if err != nil {
		return nil, err
	}
	return s.matches(ctx, req)
}

// matches is the ranking pass for one request, in the org scope.
func (s *TalentService) matches(ctx context.Context, req TalentRequest) ([]TalentMatch, error) {
	terms := strings.Join(append([]string{}, req.Skills...), " or ")
	var out []TalentMatch
	err := s.st.WithTx(ctx, orgScoped(orgOf(req)), func(ctx context.Context, tx *store.Tx) error {
		profiles, err := tx.Q.ListTalentProfilesForMatching(ctx, db.ListTalentProfilesForMatchingParams{
			OrgID: orgOf(req), Terms: terms, RowLimit: TalentMatchLimit,
		})
		if err != nil {
			return err
		}
		pool, err := tx.Q.ListTalentPoolForMatching(ctx, db.ListTalentPoolForMatchingParams{
			OrgID: orgOf(req), Terms: terms, RowLimit: TalentMatchLimit,
		})
		if err != nil {
			return err
		}
		known, err := tx.Q.ListCandidateIDsAtCompany(ctx, req.ClientCompanyID)
		if err != nil {
			return err
		}
		intros, err := tx.Q.ListTalentIntrosByRequest(ctx, req.ID)
		if err != nil {
			return err
		}
		exclude := make(map[uuid.UUID]bool, len(known))
		for _, id := range known {
			exclude[id] = true
		}
		introOf := make(map[uuid.UUID]string, len(intros))
		for _, i := range intros {
			introOf[i.CandidateID] = i.Status
			// A person the company already asked to meet stays on the list
			// with that status, even if they have since entered its pipeline.
			delete(exclude, i.CandidateID)
		}
		entries := make([]domain.TalentEntry, 0, len(profiles)+len(pool))
		byID := make(map[uuid.UUID]TalentMatch, len(profiles)+len(pool))
		for _, r := range profiles {
			e := domain.TalentEntry{
				ID: r.ID, CandidateID: r.CandidateID, Source: domain.TalentSourceNetwork, Skills: r.Skills,
				Seniority: deref(r.Seniority), Location: deref(r.Location), RemotePolicy: deref(r.RemotePolicy),
				TextRank: float64(r.TextRank),
			}
			entries = append(entries, e)
			byID[r.ID] = TalentMatch{
				ID: r.ID, Source: e.Source, Skills: r.Skills, Seniority: e.Seniority, Location: e.Location,
				RemotePolicy: e.RemotePolicy, Headline: r.Headline, AvailableFrom: datePtr(r.AvailableFrom),
				HasResume: r.HasResume, candidateID: r.CandidateID,
			}
		}
		for _, r := range pool {
			remote := ""
			if r.RemoteOk {
				remote = RemoteRemote
			}
			e := domain.TalentEntry{
				ID: r.ID, CandidateID: r.CandidateID, Source: domain.TalentSourcePool, Skills: r.Skills,
				Seniority: deref(r.Seniority), Location: deref(r.Location), RemotePolicy: remote,
				TextRank: float64(r.TextRank),
			}
			entries = append(entries, e)
			byID[r.ID] = TalentMatch{
				ID: r.ID, Source: e.Source, Skills: r.Skills, Seniority: e.Seniority, Location: e.Location,
				RemotePolicy: remote, HasResume: r.HasResume, candidateID: r.CandidateID,
			}
		}
		ranked := domain.RankTalent(domain.TalentRequest{
			Skills: req.Skills, Seniority: req.Seniority, Location: req.Location, RemotePolicy: req.RemotePolicy,
		}, entries, exclude)
		out = make([]TalentMatch, 0, len(ranked))
		for _, m := range ranked {
			match := byID[m.Entry.ID]
			match.Score, match.Breakdown = m.Score, m.Breakdown
			match.SharedSkills = domain.SharedSkills(req.Skills, match.Skills)
			match.IntroStatus = introOf[match.candidateID]
			out = append(out, match)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("talent matches: %w", err)
	}
	return out, nil
}

// Introduce records that the company wants to meet one match. The match id
// is re-resolved against a fresh ranking, so a company can only ask about
// someone it was actually shown.
func (s *TalentService) Introduce(ctx context.Context, p Principal, requestID, matchID uuid.UUID) (ClientIntro, error) {
	if err := requireClient(p); err != nil {
		return ClientIntro{}, err
	}
	req, err := s.request(ctx, p, requestID)
	if err != nil {
		return ClientIntro{}, err
	}
	if !req.Open() {
		return ClientIntro{}, ErrTalentRequestClosed
	}
	matches, err := s.matches(ctx, req)
	if err != nil {
		return ClientIntro{}, err
	}
	var match TalentMatch
	found := false
	for _, m := range matches {
		if m.ID == matchID {
			match, found = m, true
			break
		}
	}
	if !found {
		return ClientIntro{}, ErrNotFound
	}
	if match.IntroStatus != "" {
		return ClientIntro{}, ErrIntroRequested
	}
	var out ClientIntro
	err = s.st.WithTx(ctx, orgScoped(p.OrgID), func(ctx context.Context, tx *store.Tx) error {
		row, err := tx.Q.CreateTalentIntro(ctx, db.CreateTalentIntroParams{
			OrgID: p.OrgID, RequestID: req.ID, CandidateID: match.candidateID, Source: match.Source, Score: float32(match.Score),
		})
		if err != nil {
			return err
		}
		out = clientIntro(db.ListTalentIntrosByRequestRow{
			ID: row.ID, OrgID: row.OrgID, RequestID: row.RequestID, CandidateID: row.CandidateID, Source: row.Source,
			Score: row.Score, Status: row.Status, RequestedAt: row.RequestedAt,
		})
		return nil
	})
	if err != nil {
		if isUniqueViolation(err) {
			return ClientIntro{}, ErrIntroRequested
		}
		return ClientIntro{}, fmt.Errorf("request introduction: %w", err)
	}
	return out, nil
}

// AllRequests is every company's requests as the recruiter sees them.
func (s *TalentService) AllRequests(ctx context.Context, p Principal) ([]TalentRequest, error) {
	if err := requireRecruiter(p); err != nil {
		return nil, err
	}
	var out []TalentRequest
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		rows, err := tx.Q.ListTalentRequests(ctx, p.OrgID)
		if err != nil {
			return err
		}
		out = make([]TalentRequest, 0, len(rows))
		for _, r := range rows {
			out = append(out, talentRequest(r))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list all talent requests: %w", err)
	}
	return out, nil
}

// RecruiterRequest is one request as the recruiter works it: the
// introductions with the people named, and the company's open jobs an
// opportunity may be sent for.
type RecruiterRequest struct {
	Request TalentRequest
	Intros  []TalentIntro
	Jobs    []Job
}

// RequestForRecruiter is the recruiter's view of one request.
func (s *TalentService) RequestForRecruiter(ctx context.Context, p Principal, id uuid.UUID) (RecruiterRequest, error) {
	if err := requireRecruiter(p); err != nil {
		return RecruiterRequest{}, err
	}
	var out RecruiterRequest
	req, err := s.request(ctx, p, id)
	if err != nil {
		return RecruiterRequest{}, err
	}
	out.Request = req
	err = s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		rows, err := tx.Q.ListTalentIntrosByRequest(ctx, id)
		if err != nil {
			return err
		}
		out.Intros = make([]TalentIntro, 0, len(rows))
		for _, r := range rows {
			out.Intros = append(out.Intros, talentIntro(r, req))
		}
		jobs, err := tx.Q.ListOpenJobsByCompany(ctx, req.ClientCompanyID)
		if err != nil {
			return err
		}
		out.Jobs = make([]Job, 0, len(jobs))
		for _, j := range jobs {
			out.Jobs = append(out.Jobs, Job{ID: j.ID, ClientCompanyID: j.ClientCompanyID, Title: j.Title, Slug: j.Slug, Status: j.Status})
		}
		return nil
	})
	if err != nil {
		return RecruiterRequest{}, fmt.Errorf("talent request for recruiter: %w", err)
	}
	return out, nil
}

// SendOpportunity mails the candidate about the role, with a link to answer,
// and marks the introduction sent. The job must be one of the requesting
// company's open jobs; the request's own job is the default when none is
// named.
func (s *TalentService) SendOpportunity(ctx context.Context, p Principal, introID, jobID uuid.UUID) (TalentIntro, error) {
	if err := requireRecruiter(p); err != nil {
		return TalentIntro{}, err
	}
	var intro db.GetTalentIntroRow
	var job db.Job
	var orgName string
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		var err error
		intro, err = tx.Q.GetTalentIntro(ctx, introID)
		if err != nil {
			return err
		}
		if intro.Status != IntroRequested {
			return ErrIntroNotWaiting
		}
		if jobID == uuid.Nil {
			if !intro.RequestJobID.Valid {
				return ErrTalentJobRequired
			}
			jobID = intro.RequestJobID.UUID
		}
		job, err = tx.Q.GetJob(ctx, jobID)
		if err != nil {
			return errIfNoRows(err, ErrIntroJob)
		}
		if job.ClientCompanyID != intro.ClientCompanyID || job.Status != JobOpen {
			return ErrIntroJob
		}
		org, err := tx.Q.GetOrg(ctx, p.OrgID)
		if err != nil {
			return err
		}
		orgName = org.Name
		return nil
	})
	if err != nil {
		return TalentIntro{}, wrapTalent("send opportunity", err)
	}
	token := ""
	if s.links != nil {
		token, _, err = s.links.Issue(ctx, p, LinkOpportunity, introID, OpportunityLinkTTL)
		if err != nil {
			return TalentIntro{}, fmt.Errorf("opportunity link: %w", err)
		}
	}
	err = s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		n, err := tx.Q.MarkTalentIntroSent(ctx, db.MarkTalentIntroSentParams{
			ID: introID, JobID: uuid.NullUUID{UUID: job.ID, Valid: true}, SentBy: uuid.NullUUID{UUID: p.UserID, Valid: true},
		})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrIntroNotWaiting
		}
		if s.q == nil {
			return nil
		}
		return enqueued(s.q.Enqueue(ctx, tx, queueEmailKind, emailPayload(mail.TemplateOpportunity, intro.CandidateEmail, p.OrgID, map[string]any{
			"CandidateName":     intro.CandidateName,
			"OrgName":           orgName,
			"ClientCompanyName": intro.ClientCompanyName,
			"JobTitle":          job.Title,
			"OpportunityURL":    s.baseURL + opportunityPath + token,
		})))
	})
	if err != nil {
		return TalentIntro{}, wrapTalent("send opportunity", err)
	}
	return s.intro(ctx, p, introID)
}

// DismissIntro closes an introduction the recruiter will not make.
func (s *TalentService) DismissIntro(ctx context.Context, p Principal, introID uuid.UUID) error {
	if err := requireRecruiter(p); err != nil {
		return err
	}
	var n int64
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		var err error
		n, err = tx.Q.DismissTalentIntro(ctx, introID)
		return err
	})
	if err != nil {
		return fmt.Errorf("dismiss introduction: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *TalentService) intro(ctx context.Context, p Principal, id uuid.UUID) (TalentIntro, error) {
	var out TalentIntro
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		row, err := tx.Q.GetTalentIntro(ctx, id)
		if err != nil {
			return err
		}
		out = talentIntro(db.ListTalentIntrosByRequestRow{
			ID: row.ID, OrgID: row.OrgID, RequestID: row.RequestID, CandidateID: row.CandidateID, Source: row.Source,
			Score: row.Score, Status: row.Status, JobID: row.JobID, ApplicationID: row.ApplicationID, SentBy: row.SentBy,
			RequestedAt: row.RequestedAt, SentAt: row.SentAt, AnsweredAt: row.AnsweredAt,
			CandidateName: row.CandidateName, CandidateEmail: row.CandidateEmail,
		}, TalentRequest{Title: row.RequestTitle, ClientCompanyName: row.ClientCompanyName})
		return nil
	})
	if err != nil {
		return TalentIntro{}, wrapTalent("talent introduction", err)
	}
	return out, nil
}

// Opportunity is the candidate's page, reached by the link the email carries.
func (s *TalentService) Opportunity(ctx context.Context, p Principal) (Opportunity, error) {
	if err := requireOpportunityLink(p); err != nil {
		return Opportunity{}, err
	}
	var out Opportunity
	err := s.st.WithTx(ctx, orgScoped(p.OrgID), func(ctx context.Context, tx *store.Tx) error {
		var err error
		out, err = s.opportunity(ctx, tx, p)
		return err
	})
	if err != nil {
		return Opportunity{}, wrapTalent("opportunity", err)
	}
	return out, nil
}

func (s *TalentService) opportunity(ctx context.Context, tx *store.Tx, p Principal) (Opportunity, error) {
	row, err := tx.Q.GetTalentIntro(ctx, p.SubjectID)
	if err != nil {
		return Opportunity{}, err
	}
	org, err := tx.Q.GetOrg(ctx, p.OrgID)
	if err != nil {
		return Opportunity{}, err
	}
	out := Opportunity{
		IntroID: row.ID, CandidateName: row.CandidateName, OrgName: org.Name,
		ClientCompanyName: row.ClientCompanyName, RequestTitle: row.RequestTitle, Status: row.Status,
	}
	if row.ApplicationID.Valid {
		out.ApplicationID = row.ApplicationID.UUID
	}
	if row.JobID.Valid {
		job, err := tx.Q.GetJob(ctx, row.JobID.UUID)
		if err != nil {
			return Opportunity{}, err
		}
		out.JobTitle, out.JobDescription = job.Title, job.Description
	}
	return out, nil
}

// Answer records the candidate's yes or no. A yes opens an application on
// the job the opportunity was sent for, released to the company at once:
// the company asked for this person and the person agreed, which is the
// vetting a release usually waits for. A no closes the introduction with
// nothing shared.
func (s *TalentService) Answer(ctx context.Context, p Principal, accept bool) (Opportunity, error) {
	if err := requireOpportunityLink(p); err != nil {
		return Opportunity{}, err
	}
	scope := orgScoped(p.OrgID)
	var out Opportunity
	err := s.st.WithTx(ctx, scope, func(ctx context.Context, tx *store.Tx) error {
		intro, err := tx.Q.GetTalentIntro(ctx, p.SubjectID)
		if err != nil {
			return err
		}
		if intro.Status != IntroSent {
			return ErrIntroNotSent
		}
		status := IntroDeclined
		var applicationID uuid.NullUUID
		if accept {
			status = IntroAccepted
			id, err := s.openApplication(ctx, tx, p.OrgID, intro)
			if err != nil {
				return err
			}
			applicationID = uuid.NullUUID{UUID: id, Valid: true}
		}
		n, err := tx.Q.AnswerTalentIntro(ctx, db.AnswerTalentIntroParams{ID: intro.ID, Status: status, ApplicationID: applicationID})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrIntroNotSent
		}
		out, err = s.opportunity(ctx, tx, p)
		return err
	})
	if err != nil {
		return Opportunity{}, wrapTalent("answer opportunity", err)
	}
	return out, nil
}

// openApplication puts the candidate on the job's first stage, released to
// the company, and tells the company's users as a recruiter's release would.
// A candidate already on the job (they applied meanwhile) is released rather
// than filed twice.
func (s *TalentService) openApplication(ctx context.Context, tx *store.Tx, orgID uuid.UUID, intro db.GetTalentIntroRow) (uuid.UUID, error) {
	if !intro.JobID.Valid {
		return uuid.Nil, ErrTalentJobRequired
	}
	job, err := tx.Q.GetJob(ctx, intro.JobID.UUID)
	if err != nil {
		return uuid.Nil, err
	}
	if job.Status != JobOpen {
		return uuid.Nil, ErrJobNotOpen
	}
	stage, err := tx.Q.FirstStage(ctx, job.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNoStages
	}
	if err != nil {
		return uuid.Nil, err
	}
	system := orgScoped(orgID)
	var app db.Application
	existing, err := tx.Q.ListCandidateApplications(ctx, intro.CandidateID)
	if err != nil {
		return uuid.Nil, err
	}
	for _, a := range existing {
		if a.JobID == job.ID {
			app = db.Application{ID: a.ID, ReleasedAt: a.ReleasedAt}
			break
		}
	}
	if app.ID == uuid.Nil {
		app, err = tx.Q.CreateApplication(ctx, db.CreateApplicationParams{
			OrgID: orgID, JobID: job.ID, CandidateID: intro.CandidateID,
			ClientCompanyID: job.ClientCompanyID, StageID: stage.ID, ScreeningAnswers: []byte("{}"),
		})
		if err != nil {
			return uuid.Nil, err
		}
		if _, err := tx.Q.CreateApplicationEvent(ctx, db.CreateApplicationEventParams{
			OrgID: orgID, ApplicationID: app.ID, ActorKind: "candidate",
			Kind: eventApplied, ToStageID: uuid.NullUUID{UUID: stage.ID, Valid: true},
			Payload: []byte(`{"from_talent_intro":"` + intro.ID.String() + `"}`),
		}); err != nil {
			return uuid.Nil, err
		}
	}
	if app.ReleasedAt.Valid {
		return app.ID, nil
	}
	if _, err := tx.Q.SetApplicationReleased(ctx, db.SetApplicationReleasedParams{ID: app.ID, ReleasedAt: ts(s.Now())}); err != nil {
		return uuid.Nil, err
	}
	if _, err := tx.Q.CreateApplicationEvent(ctx, db.CreateApplicationEventParams{
		OrgID: orgID, ApplicationID: app.ID, ActorKind: actorKind(system), Kind: EventReleased,
		Reason: nullable("accepted an introduction"), Payload: []byte("{}"),
	}); err != nil {
		return uuid.Nil, err
	}
	if s.q == nil {
		return app.ID, nil
	}
	users, err := tx.Q.ListClientUsersByCompany(ctx, job.ClientCompanyID)
	if err != nil {
		return uuid.Nil, err
	}
	for _, u := range users {
		if err := enqueued(s.q.Enqueue(ctx, tx, queueEmailKind, emailPayload(mail.TemplateClientReleaseNotice, u.Email, orgID, map[string]any{
			"ContactName":    u.Name,
			"JobTitle":       job.Title,
			"CandidateCount": 1,
			"PortalURL":      s.baseURL + clientJobPath + job.ID.String(),
		}))); err != nil {
			return uuid.Nil, err
		}
	}
	return app.ID, nil
}

// WaitingIntros is the work-queue rule: introductions asked for and not yet
// sent, oldest first.
func talentIntroItems(ctx context.Context, tx *store.Tx) ([]QueueItem, error) {
	rows, err := tx.Q.ListTalentIntrosWaiting(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]QueueItem, 0, len(rows))
	for _, row := range rows {
		due := row.RequestedAt.Time.Add(TalentIntroSLA)
		out = append(out, QueueItem{
			Kind: QueueTalentIntro, Who: row.CandidateName,
			Detail:   row.ClientName + " asked to meet them for " + row.RequestTitle,
			JobTitle: row.RequestTitle, ClientName: row.ClientName, Due: &due,
			ActionLabel: "Send opportunity", ActionURL: talentAppPath + row.RequestID.String(),
			SubjectID: row.ID,
		})
	}
	return out, nil
}

// TalentIntroSLA is how long an introduction may wait before the queue
// counts it late.
const TalentIntroSLA = 48 * time.Hour

func requireProfileLink(p Principal) error {
	if p.Kind != PrincipalMagicLink || p.MagicPurpose != LinkProfile {
		return ErrForbidden
	}
	return nil
}

func requireOpportunityLink(p Principal) error {
	if p.Kind != PrincipalMagicLink || p.MagicPurpose != LinkOpportunity {
		return ErrForbidden
	}
	return nil
}

// talentFields is a profile's own fields, cleaned for the row.
type talentFields struct {
	headline  string
	skills    []string
	seniority *string
	roles     []string
	location  *string
	remote    *string
	salary    *int32
	available pgtype.Date
}

func cleanTalentFields(in TalentProfileInput) (talentFields, error) {
	out := talentFields{
		headline: strings.TrimSpace(in.Headline), skills: cleanTags(in.Skills), roles: cleanTags(in.Roles),
		location: nullable(strings.TrimSpace(in.Location)),
	}
	if len(out.skills) == 0 {
		return talentFields{}, ErrTalentSkills
	}
	if err := checkRunes(out.headline, MaxReasonRunes); err != nil {
		return talentFields{}, err
	}
	if err := checkVocabulary(in.Seniority, Seniorities); err != nil {
		return talentFields{}, err
	}
	if err := checkVocabulary(in.RemotePolicy, RemotePolicies); err != nil {
		return talentFields{}, err
	}
	out.seniority = nullable(strings.ToLower(strings.TrimSpace(in.Seniority)))
	out.remote = nullable(strings.ToLower(strings.TrimSpace(in.RemotePolicy)))
	if in.SalaryMin < 0 {
		return talentFields{}, fmt.Errorf("%w: a salary floor cannot be negative", ErrInvalidJob)
	}
	out.salary = nullableInt(in.SalaryMin)
	if in.AvailableFrom != nil {
		out.available = pgtype.Date{Time: in.AvailableFrom.UTC().Truncate(24 * time.Hour), Valid: true}
	}
	return out, nil
}

// checkVocabulary refuses a value outside the job vocabulary; empty is fine.
func checkVocabulary(value string, allowed []string) error {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return nil
	}
	for _, a := range allowed {
		if a == value {
			return nil
		}
	}
	return fmt.Errorf("%w: %q is not one of %s", ErrInvalidJob, value, strings.Join(allowed, ", "))
}

func checkRunes(s string, limit int) error {
	if len([]rune(s)) > limit {
		return ErrTooLong
	}
	return nil
}

func orgOf(req TalentRequest) uuid.UUID { return req.orgID }

func talentProfile(r db.GetTalentProfileRow) TalentProfile {
	return TalentProfile{
		ID: r.ID, CandidateID: r.CandidateID, Name: r.CandidateName, Email: r.CandidateEmail, Phone: deref(r.CandidatePhone),
		Links: decodeLinks(r.CandidateLinks), Headline: r.Headline, Skills: r.Skills, Seniority: deref(r.Seniority),
		Roles: r.Roles, Location: deref(r.Location), RemotePolicy: deref(r.RemotePolicy), SalaryMin: derefInt32(r.SalaryMin),
		AvailableFrom: datePtr(r.AvailableFrom), ConsentAt: r.ConsentAt.Time, Withdrawn: r.WithdrawnAt.Valid,
		HasResume: r.HasResume, UpdatedAt: r.UpdatedAt.Time,
	}
}

func talentProfileFromList(r db.ListTalentProfilesRow) TalentProfile {
	return TalentProfile{
		ID: r.ID, CandidateID: r.CandidateID, Name: r.CandidateName, Email: r.CandidateEmail,
		Headline: r.Headline, Skills: r.Skills, Seniority: deref(r.Seniority), Roles: r.Roles,
		Location: deref(r.Location), RemotePolicy: deref(r.RemotePolicy), SalaryMin: derefInt32(r.SalaryMin),
		AvailableFrom: datePtr(r.AvailableFrom), ConsentAt: r.ConsentAt.Time, Withdrawn: r.WithdrawnAt.Valid,
		UpdatedAt: r.UpdatedAt.Time,
	}
}

func talentRequest(r db.ListTalentRequestsRow) TalentRequest {
	out := TalentRequest{
		ID: r.ID, orgID: r.OrgID, ClientCompanyID: r.ClientCompanyID, ClientCompanyName: r.ClientCompanyName,
		Title: r.Title, Skills: r.Skills, Seniority: deref(r.Seniority), Location: deref(r.Location),
		RemotePolicy: deref(r.RemotePolicy), Note: r.Note, Status: r.Status, CreatedAt: r.CreatedAt.Time,
		IntroCount: int(r.IntroCount), WaitingCount: int(r.WaitingCount),
	}
	if r.JobID.Valid {
		out.JobID, out.JobTitle = r.JobID.UUID, deref(r.JobTitle)
	}
	return out
}

func talentIntro(r db.ListTalentIntrosByRequestRow, req TalentRequest) TalentIntro {
	out := TalentIntro{
		ID: r.ID, RequestID: r.RequestID, RequestTitle: req.Title, ClientCompanyName: req.ClientCompanyName,
		CandidateID: r.CandidateID, CandidateName: r.CandidateName, CandidateEmail: r.CandidateEmail,
		Source: r.Source, Score: float64(r.Score), Status: r.Status, RequestedAt: r.RequestedAt.Time,
		SentAt: timePtr(r.SentAt), AnsweredAt: timePtr(r.AnsweredAt),
	}
	if r.JobID.Valid {
		out.JobID = r.JobID.UUID
	}
	if r.ApplicationID.Valid {
		out.ApplicationID = r.ApplicationID.UUID
	}
	return out
}

// clientIntro is the company's view: the label names the introduction, not
// the person, until they accept and their application is theirs to read.
func clientIntro(r db.ListTalentIntrosByRequestRow) ClientIntro {
	out := ClientIntro{
		ID: r.ID, Source: r.Source, Score: float64(r.Score), Status: r.Status,
		Label: "Introduction " + r.ID.String()[:8], RequestedAt: r.RequestedAt.Time,
		SentAt: timePtr(r.SentAt), AnsweredAt: timePtr(r.AnsweredAt),
	}
	if r.Status == IntroAccepted {
		out.Label = r.CandidateName
		if r.ApplicationID.Valid {
			out.ApplicationID = r.ApplicationID.UUID
		}
	}
	return out
}

func datePtr(d pgtype.Date) *time.Time {
	if !d.Valid {
		return nil
	}
	t := d.Time
	return &t
}

func errIfNoRows(err, sentinel error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return sentinel
	}
	return err
}

// wrapTalent keeps the errors a screen shows inline unwrapped.
func wrapTalent(what string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pgx.ErrNoRows):
		return ErrNotFound
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrForbidden), errors.Is(err, ErrTalentConsent),
		errors.Is(err, ErrTalentSkills), errors.Is(err, ErrTalentTitle), errors.Is(err, ErrTalentRequestClosed),
		errors.Is(err, ErrIntroRequested), errors.Is(err, ErrIntroNotWaiting), errors.Is(err, ErrIntroNotSent),
		errors.Is(err, ErrIntroJob), errors.Is(err, ErrTalentJobRequired), errors.Is(err, ErrInvalidJob),
		errors.Is(err, ErrTooLong), errors.Is(err, ErrNoStages), errors.Is(err, ErrJobNotOpen),
		errors.Is(err, ErrNoBlobStore), errors.Is(err, domain.ErrResumeEmpty), errors.Is(err, domain.ErrResumeTooLarge),
		errors.Is(err, domain.ErrResumeType):
		return err
	}
	return fmt.Errorf("%s: %w", what, err)
}
