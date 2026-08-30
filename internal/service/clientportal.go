package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"recruiting/internal/domain"
	"recruiting/internal/mail"
	"recruiting/internal/queue"
	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

// EventRequestInfo is the application_event a client's question writes.
const EventRequestInfo = "client_request_info"

// appPath is where the request-info email sends the recruiter.
const appPath = "/app/applications/"

// Bounds on what a client types; anything longer is refused, not truncated.
const (
	MaxReasonRunes  = 500
	MaxMessageRunes = 4000
)

var (
	ErrTooLong         = errors.New("service: that text is too long")
	ErrBlind           = errors.New("service: the candidate is hidden until the application reaches an unblinded stage")
	ErrMessageRequired = errors.New("service: a message is required")
	ErrNoResume        = errors.New("service: the candidate has no resume on file")
)

// The client portal's own view types. Templates and the API receive these and
// nothing else, so a field added to a domain entity cannot leak by default.
// Every field here is one a client may see.

// ClientJob is one of the company's jobs as the portal lists it.
type ClientJob struct {
	ID            uuid.UUID
	Title         string
	Location      string
	RemotePolicy  string
	Seniority     string
	Status        string
	BlindMode     bool
	ReleasedCount int
}

// ClientCandidate is what the client sees of the person. While Blind is set
// the identifying fields are empty and Label stands in for the name.
type ClientCandidate struct {
	Blind bool
	Label string // the name, or an anonymous label while blind
	Email string
	Phone string
	Links []string
}

// ClientApplication is one released application as the portal shows it.
type ClientApplication struct {
	ID               uuid.UUID
	JobID            uuid.UUID
	JobTitle         string
	StageID          uuid.UUID
	StageName        string
	Status           domain.ApplicationStatus
	Candidate        ClientCandidate
	RecruiterSummary string
	ReleasedAt       time.Time
}

// Active reports whether the client may still act on the application.
func (a ClientApplication) Active() bool { return a.Status == domain.StatusActive }

// ClientScore is one criterion's score; the interviewer's note on it is not carried.
type ClientScore struct {
	Name  string
	Score int
}

// ClientScorecard is an interviewer's verdict without their notes.
type ClientScorecard struct {
	StageName  string
	VetterName string
	Overall    string
	Scores     []ClientScore
	FiledAt    time.Time
}

// ClientAssessment is the assessment outcome: a score and a verdict, both
// empty until an attempt is scored and reviewed.
type ClientAssessment struct {
	Score   string
	Verdict string
}

// ClientStageOption is a stage the client may advance the application to.
type ClientStageOption struct {
	ID   uuid.UUID
	Name string
}

// ClientApplicationDetail is the portal's application page.
type ClientApplicationDetail struct {
	Application ClientApplication
	HasResume   bool
	Scorecards  []ClientScorecard
	Assessment  ClientAssessment
	AdvanceTo   []ClientStageOption
	CanReject   bool
}

// ClientPortalService is the client company's window on their released
// applications. Every read runs as the client, so RLS shows only released
// applications of their own company; every move goes through
// ApplicationService with the client actor role, so the domain rules decide.
type ClientPortalService struct {
	st      *store.Store
	apps    *ApplicationService
	resumes *ResumeService
	q       *queue.Client
	baseURL string
}

// NewClientPortalService wires the store, the mover, the resume signer, and
// the queue the request-info email goes to. A nil queue drops the email.
func NewClientPortalService(st *store.Store, apps *ApplicationService, resumes *ResumeService, q *queue.Client, baseURL string) *ClientPortalService {
	return &ClientPortalService{st: st, apps: apps, resumes: resumes, q: q, baseURL: strings.TrimRight(baseURL, "/")}
}

func requireClient(p Principal) error {
	if p.Kind != PrincipalClientUser {
		return ErrForbidden
	}
	return nil
}

// Me is the signed-in client user's display name.
func (s *ClientPortalService) Me(ctx context.Context, p Principal) (ClientUser, error) {
	if err := requireClient(p); err != nil {
		return ClientUser{}, err
	}
	var out ClientUser
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		row, err := tx.Q.GetClientUser(ctx, p.UserID)
		if err != nil {
			return err
		}
		out = ClientUser{ID: row.ID, ClientCompanyID: row.ClientCompanyID, Email: row.Email, Name: row.Name}
		return nil
	})
	if err != nil {
		return ClientUser{}, wrapClient("client user", err)
	}
	return out, nil
}

// Jobs lists the company's jobs, newest first.
func (s *ClientPortalService) Jobs(ctx context.Context, p Principal) ([]ClientJob, error) {
	if err := requireClient(p); err != nil {
		return nil, err
	}
	var out []ClientJob
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		rows, err := tx.Q.ListClientJobs(ctx, p.ClientCompanyID)
		if err != nil {
			return err
		}
		out = make([]ClientJob, 0, len(rows))
		for _, r := range rows {
			out = append(out, clientJob(db.Job{
				ID: r.ID, Title: r.Title, Location: r.Location, RemotePolicy: r.RemotePolicy,
				Seniority: r.Seniority, Status: r.Status, BlindMode: r.BlindMode,
			}, int(r.ReleasedCount)))
		}
		return nil
	})
	if err != nil {
		return nil, wrapClient("client jobs", err)
	}
	return out, nil
}

// Job is one of the company's jobs with its released applications.
func (s *ClientPortalService) Job(ctx context.Context, p Principal, jobID uuid.UUID) (ClientJob, []ClientApplication, error) {
	if err := requireClient(p); err != nil {
		return ClientJob{}, nil, err
	}
	var job ClientJob
	var apps []ClientApplication
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		row, err := tx.Q.GetJob(ctx, jobID)
		if err != nil {
			return err
		}
		if row.Status == JobDraft {
			// A draft is not announced to the client yet.
			return ErrNotFound
		}
		rows, err := tx.Q.ListReleasedApplications(ctx, jobID)
		if err != nil {
			return err
		}
		job = clientJob(row, len(rows))
		apps = make([]ClientApplication, 0, len(rows))
		for _, r := range rows {
			apps = append(apps, clientApplication(r))
		}
		return nil
	})
	if err != nil {
		return ClientJob{}, nil, wrapClient("client job", err)
	}
	return job, apps, nil
}

// Application is the portal's application page.
func (s *ClientPortalService) Application(ctx context.Context, p Principal, id uuid.UUID) (ClientApplicationDetail, error) {
	if err := requireClient(p); err != nil {
		return ClientApplicationDetail{}, err
	}
	var out ClientApplicationDetail
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		row, err := tx.Q.GetReleasedApplication(ctx, id)
		if err != nil {
			return err
		}
		out.Application = clientApplication(db.ListReleasedApplicationsRow(row))
		resumes, err := tx.Q.ListResumesByCandidate(ctx, row.CandidateID)
		if err != nil {
			return err
		}
		out.HasResume = len(resumes) > 0
		cards, err := tx.Q.ListClientScorecards(ctx, id)
		if err != nil {
			return err
		}
		out.Scorecards = make([]ClientScorecard, 0, len(cards))
		for _, c := range cards {
			out.Scorecards = append(out.Scorecards, clientScorecard(c))
		}
		outcome, err := tx.Q.GetLatestAssessmentOutcome(ctx, id)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return err
		default:
			out.Assessment = clientAssessment(outcome)
		}
		stages, err := listStages(ctx, tx, row.JobID)
		if err != nil {
			return err
		}
		out.AdvanceTo, out.CanReject = clientMoves(stages, row.StageID, domain.ApplicationStatus(row.Status))
		return nil
	})
	if err != nil {
		return ClientApplicationDetail{}, wrapClient("client application", err)
	}
	return out, nil
}

// ResumeURL signs a download link for the candidate's newest resume. A blind
// application has no resume for the client: the file itself would name them.
func (s *ClientPortalService) ResumeURL(ctx context.Context, p Principal, id uuid.UUID) (string, error) {
	if err := requireClient(p); err != nil {
		return "", err
	}
	if s.resumes == nil || s.resumes.blob == nil {
		return "", ErrNoBlobStore
	}
	var resume db.Resume
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		row, err := tx.Q.GetReleasedApplication(ctx, id)
		if err != nil {
			return err
		}
		if blind(row.JobBlindMode, row.StageUnblind) {
			return ErrBlind
		}
		resumes, err := tx.Q.ListResumesByCandidate(ctx, row.CandidateID)
		if err != nil {
			return err
		}
		if len(resumes) == 0 {
			return ErrNoResume
		}
		resume = resumes[0]
		return nil
	})
	if err != nil {
		return "", wrapClient("client resume", err)
	}
	url, err := s.resumes.blob.SignedGetURL(ctx, resume.BlobKey, resume.Filename, ResumeURLTTL)
	if err != nil {
		return "", fmt.Errorf("client resume: %w", err)
	}
	return url, nil
}

// Advance moves the application forward to a later client review stage. The
// domain would let a client move to any client review stage; the portal
// only ever advances, so a target at or before the current stage is refused.
func (s *ClientPortalService) Advance(ctx context.Context, p Principal, id, toStageID uuid.UUID) error {
	if err := requireClient(p); err != nil {
		return err
	}
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		row, err := tx.Q.GetReleasedApplication(ctx, id)
		if err != nil {
			return err
		}
		stages, err := listStages(ctx, tx, row.JobID)
		if err != nil {
			return err
		}
		to, ok := findStage(stages, toStageID)
		if !ok {
			return ErrNotFound
		}
		if to.Position <= int(row.StagePosition) {
			return domain.ErrForbiddenMove
		}
		return nil
	})
	if err != nil {
		return wrapClient("client advance", err)
	}
	_, err = s.apps.Move(ctx, p, MoveRequest{ApplicationID: id, ToStageID: toStageID})
	return err
}

// Reject closes the application; the domain requires a reason.
func (s *ClientPortalService) Reject(ctx context.Context, p Principal, id uuid.UUID, reason string) error {
	if err := requireClient(p); err != nil {
		return err
	}
	if utf8.RuneCountInString(reason) > MaxReasonRunes {
		return ErrTooLong
	}
	var rejectID uuid.UUID
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		row, err := tx.Q.GetReleasedApplication(ctx, id)
		if err != nil {
			return err
		}
		stages, err := listStages(ctx, tx, row.JobID)
		if err != nil {
			return err
		}
		for _, st := range stages {
			if st.Kind == domain.StageTerminal && st.Terminal == domain.StatusRejected {
				rejectID = st.ID
			}
		}
		if rejectID == uuid.Nil {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return wrapClient("client reject", err)
	}
	_, err = s.apps.Move(ctx, p, MoveRequest{ApplicationID: id, ToStageID: rejectID, Reason: reason})
	return err
}

// RequestInfo records the client's question on the timeline and emails it
// to the org's recruiters. The application is checked as the client, so an
// unreleased one is not found; the recruiters are looked up as the org, whose
// roles a client cannot read.
func (s *ClientPortalService) RequestInfo(ctx context.Context, p Principal, id uuid.UUID, message string) error {
	if err := requireClient(p); err != nil {
		return err
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return ErrMessageRequired
	}
	if utf8.RuneCountInString(message) > MaxMessageRunes {
		return ErrTooLong
	}
	var app db.GetReleasedApplicationRow
	var contact db.ClientUser
	var company db.ClientCompany
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		var err error
		if app, err = tx.Q.GetReleasedApplication(ctx, id); err != nil {
			return err
		}
		if app.Status != string(domain.StatusActive) {
			return ErrNotActive
		}
		if contact, err = tx.Q.GetClientUser(ctx, p.UserID); err != nil {
			return err
		}
		company, err = tx.Q.GetClientCompany(ctx, db.GetClientCompanyParams{ID: p.ClientCompanyID, OrgID: p.OrgID})
		return err
	})
	if err != nil {
		return wrapClient("client request info", err)
	}
	err = s.st.WithTx(ctx, orgScoped(p.OrgID), func(ctx context.Context, tx *store.Tx) error {
		if err := s.apps.event(ctx, tx, p, id, EventRequestInfo, message); err != nil {
			return err
		}
		if s.q == nil {
			return nil
		}
		recruiters, err := tx.Q.ListOrgUsersWithRole(ctx, db.ListOrgUsersWithRoleParams{OrgID: p.OrgID, Role: RoleRecruiter})
		if err != nil {
			return err
		}
		for _, r := range recruiters {
			err := s.q.Enqueue(ctx, tx, queue.KindEmailSend, queue.EmailPayload{
				Template: mail.TemplateClientRequestInfo, To: r.Email, OrgID: p.OrgID,
				Data: map[string]any{
					"RecruiterName":     r.Name,
					"ClientContactName": contact.Name,
					"ClientCompanyName": company.Name,
					"CandidateName":     app.CandidateName,
					"JobTitle":          app.JobTitle,
					"Message":           message,
					"ApplicationURL":    s.baseURL + appPath + id.String(),
				},
			})
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("client request info: %w", err)
	}
	return nil
}

// blind reports whether the candidate is still hidden: the job is blind and
// the application's stage has not lifted it.
func blind(jobBlind, stageUnblind bool) bool { return jobBlind && !stageUnblind }

func clientJob(row db.Job, released int) ClientJob {
	return ClientJob{
		ID: row.ID, Title: row.Title, Location: deref(row.Location), RemotePolicy: deref(row.RemotePolicy),
		Seniority: deref(row.Seniority), Status: row.Status, BlindMode: row.BlindMode, ReleasedCount: released,
	}
}

// clientApplication builds the view; redaction happens here and nowhere
// else, so a blind row never reaches a template with its name attached.
func clientApplication(r db.ListReleasedApplicationsRow) ClientApplication {
	out := ClientApplication{
		ID: r.ID, JobID: r.JobID, JobTitle: r.JobTitle, StageID: r.StageID, StageName: r.StageName,
		Status: domain.ApplicationStatus(r.Status), RecruiterSummary: deref(r.RecruiterSummary),
		ReleasedAt: r.ReleasedAt.Time,
	}
	if blind(r.JobBlindMode, r.StageUnblind) {
		out.Candidate = ClientCandidate{Blind: true, Label: "Candidate " + r.ID.String()[:8], Links: []string{}}
		return out
	}
	links := []string{}
	_ = json.Unmarshal(r.CandidateLinks, &links)
	out.Candidate = ClientCandidate{Label: r.CandidateName, Email: r.CandidateEmail, Phone: deref(r.CandidatePhone), Links: links}
	return out
}

// clientScorecard keeps the scores and drops every note, per criterion and overall.
func clientScorecard(c db.ListClientScorecardsRow) ClientScorecard {
	var scores []struct {
		Name  string `json:"name"`
		Score int    `json:"score"`
	}
	_ = json.Unmarshal(c.Scores, &scores)
	out := ClientScorecard{StageName: c.StageName, VetterName: c.VetterName, Overall: c.Overall, FiledAt: c.CreatedAt.Time, Scores: make([]ClientScore, 0, len(scores))}
	for _, s := range scores {
		out.Scores = append(out.Scores, ClientScore{Name: s.Name, Score: s.Score})
	}
	return out
}

func clientAssessment(row db.GetLatestAssessmentOutcomeRow) ClientAssessment {
	out := ClientAssessment{Verdict: deref(row.Verdict)}
	if f, err := row.Score.Float64Value(); err == nil && f.Valid {
		out.Score = strconv.FormatFloat(f.Float64, 'f', -1, 64)
	}
	return out
}

// clientMoves is what the domain lets a client do from here: advance to a
// later client review stage, or reject. The domain re-checks on the move.
func clientMoves(stages []domain.Stage, stageID uuid.UUID, status domain.ApplicationStatus) ([]ClientStageOption, bool) {
	from, ok := findStage(stages, stageID)
	if !ok || status != domain.StatusActive || from.Kind != domain.StageClientReview {
		return []ClientStageOption{}, false
	}
	out := []ClientStageOption{}
	for _, st := range stages {
		if st.Kind == domain.StageClientReview && st.Position > from.Position {
			out = append(out, ClientStageOption{ID: st.ID, Name: st.Name})
		}
	}
	return out, true
}

// wrapClient keeps the errors a screen shows inline unwrapped.
func wrapClient(what string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pgx.ErrNoRows):
		return ErrNotFound
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrForbidden), errors.Is(err, ErrBlind),
		errors.Is(err, ErrNoResume), errors.Is(err, ErrNoBlobStore), errors.Is(err, ErrMessageRequired),
		errors.Is(err, ErrTooLong), errors.Is(err, ErrNotActive), errors.Is(err, domain.ErrForbiddenMove):
		return err
	}
	return fmt.Errorf("%s: %w", what, err)
}
