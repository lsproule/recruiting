package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"recruiting/internal/queue"
	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

// Attempt statuses, in lifecycle order. scored and reviewed are set by the
// finalize and review steps, not here.
const (
	AttemptInvited   = "invited"
	AttemptStarted   = "started"
	AttemptSubmitted = "submitted"
	AttemptExpired   = "expired"
	AttemptScored    = "scored"
	AttemptReviewed  = "reviewed"
)

// Submission kinds and statuses. A run sees public cases only; a submit is
// scored against all of them.
const (
	SubmissionRun    = "run"
	SubmissionSubmit = "submit"

	SubmissionQueued  = "queued"
	SubmissionRunning = "running"
	SubmissionDone    = "done"
	SubmissionError   = "error"
)

// Event kinds the recording accepts.
var attemptEventKinds = map[string]bool{
	"edit": true, "paste": true, "focus": true, "blur": true, "run": true, "submit": true, "lang_change": true,
	"keymap": true, "fullscreen_enter": true, "fullscreen_exit": true, "snapshot": true, "consent": true,
}

// AttemptKeymaps are the editor keymaps a candidate may choose between; the
// island's control offers exactly these.
var AttemptKeymaps = []string{"default", "vim", "emacs"}

// MaxEventBatch bounds one ingest call.
const MaxEventBatch = 500

// MaxEventDataBytes bounds one event's data.
const MaxEventDataBytes = 64 << 10

// MaxSourceBytes bounds an editor's text.
const MaxSourceBytes = 256 << 10

// EventClockSkew is how far an event's client time may sit from the server
// clock before it is refused as nonsense.
const EventClockSkew = 24 * time.Hour

// EventGrace is how long after an attempt closes the island may still land
// its last batch (the submit event, a beacon in flight).
const EventGrace = 60 * time.Second

// LinkGrace is how long past the attempt's deadline its link stays open, so
// the final requests after expiry are still authenticated.
const LinkGrace = 15 * time.Minute

// assessmentPath is where an assessment link lands; auth swaps the token
// for a sealed cookie there.
const assessmentPath = "/assess/"

var (
	ErrInviteExpired      = errors.New("service: the assessment invite has expired")
	ErrAttemptExpired     = errors.New("service: the assessment time is up")
	ErrAttemptNotStarted  = errors.New("service: the assessment has not been started")
	ErrAttemptClosed      = errors.New("service: the assessment has been submitted")
	ErrEventSeq           = errors.New("service: event seq is not after the last recorded one")
	ErrEventKind          = errors.New("service: unknown event kind")
	ErrLanguageNotAllowed = errors.New("service: that language is not allowed for this problem")
	ErrProblemNotInSet    = errors.New("service: that problem is not part of this assessment")
	ErrSubmissionPending  = errors.New("service: a run of this problem is still in progress")
	ErrSourceTooLarge     = errors.New("service: the source is larger than 256 KiB")
	ErrEventInvalid       = errors.New("service: invalid event")
)

// Attempt is one candidate's go at an assessment.
type Attempt struct {
	ID              uuid.UUID
	OrgID           uuid.UUID
	ApplicationID   uuid.UUID
	AssessmentID    uuid.UUID
	StageID         uuid.UUID
	Status          string
	InvitedAt       time.Time
	InviteExpiresAt time.Time
	StartedAt       time.Time
	ExpiresAt       time.Time
	FinishedAt      time.Time
	LastEventSeq    int64
	// Preview marks a recruiter's own sitting: it belongs to no application
	// and nothing downstream reads it.
	Preview bool
}

// Closed reports whether the candidate can no longer work on the attempt.
func (a Attempt) Closed() bool { return a.Status != AttemptInvited && a.Status != AttemptStarted }

// SessionProblem is one problem as the candidate sees it: hidden cases and
// reference solutions stripped, languages narrowed by the override.
type SessionProblem struct {
	ID          uuid.UUID
	Kind        string
	Title       string
	Statement   string
	Difficulty  string
	Languages   []string
	PublicTests []ProblemTestCase
	SQLSchema   string
	// Language and Source are the last synced editor state, if any.
	Language string
	Source   string
}

// AttemptSession is everything the candidate page renders.
type AttemptSession struct {
	Attempt       Attempt
	Assessment    Assessment
	Problems      []SessionProblem
	CandidateName string
	JobTitle      string
	// Remaining is the server's view of the time left; zero when not started.
	Remaining time.Duration
}

// Submission is one run or submit of a problem.
type Submission struct {
	ID        uuid.UUID
	AttemptID uuid.UUID
	ProblemID uuid.UUID
	Kind      string
	Language  string
	Source    string
	Status    string
	Result    json.RawMessage // raw runner output; never sent to the candidate
	Score     *float64
	CreatedAt time.Time
	UpdatedAt time.Time
	// CandidateResult is the projection of Result a candidate may see; nil
	// until the runner has answered.
	CandidateResult *CandidateResult
}

// AttemptEvent is one recorded editor event, as the island posts it.
type AttemptEvent struct {
	Seq       int64           `json:"seq"`
	T         int64           `json:"t"` // client unix millis
	ProblemID uuid.UUID       `json:"problem_id"`
	Type      string          `json:"type"`
	Data      json.RawMessage `json:"data"`
}

// RunnerExecutePayload is the runner.execute payload.
type RunnerExecutePayload struct {
	SubmissionID uuid.UUID `json:"submission_id"`
	OrgID        uuid.UUID `json:"org_id"`
}

// AttemptFinalizePayload is the attempt.finalize payload.
type AttemptFinalizePayload struct {
	AttemptID uuid.UUID `json:"attempt_id"`
	OrgID     uuid.UUID `json:"org_id"`
}

// AttemptService runs the candidate side of an assessment: invitation,
// the timed session, source sync, runs and submits, and event ingest.
type AttemptService struct {
	st      *store.Store
	q       *queue.Client
	baseURL string
	// Now is the clock the timer is enforced on; tests replace it.
	Now func() time.Time
	// Blobs is object storage the preview purge clears; nil leaves stored
	// objects alone.
	Blobs BlobStore
	// Logger notes what a sweep could not clear. Nil disables it.
	Logger *slog.Logger
}

// NewAttemptService wires the store, the queue runs and finalization go to,
// and the base URL invite links are built on. A nil queue queues nothing.
func NewAttemptService(st *store.Store, q *queue.Client, baseURL string) *AttemptService {
	return &AttemptService{st: st, q: q, baseURL: strings.TrimRight(baseURL, "/"), Now: time.Now}
}

// requireCandidate checks that p is the assessment link for attemptID.
func requireCandidate(p Principal, attemptID uuid.UUID) error {
	if p.Kind != PrincipalMagicLink || p.MagicPurpose != LinkAssessment || p.SubjectID == uuid.Nil {
		return ErrForbidden
	}
	if p.SubjectID != attemptID {
		// Another attempt's id is not this candidate's to see.
		return ErrNotFound
	}
	return nil
}

// Session loads the candidate's attempt. A started attempt past its
// deadline is expired here, on the way in, so every read enforces the timer.
func (s *AttemptService) Session(ctx context.Context, p Principal) (AttemptSession, error) {
	id := p.SubjectID
	if err := requireCandidate(p, id); err != nil {
		return AttemptSession{}, err
	}
	if err := s.expireIfDue(ctx, p, id); err != nil {
		return AttemptSession{}, wrapAttempt("expire attempt", err)
	}
	var out AttemptSession
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		att, err := s.lock(ctx, tx, id)
		if err != nil {
			return err
		}
		out, err = s.session(ctx, tx, att)
		return err
	})
	if err != nil {
		return AttemptSession{}, wrapAttempt("load session", err)
	}
	return out, nil
}

// Start begins the timer. It is not idempotent: a second start is refused so
// a reload cannot extend the deadline.
func (s *AttemptService) Start(ctx context.Context, p Principal) (AttemptSession, error) {
	id := p.SubjectID
	if err := requireCandidate(p, id); err != nil {
		return AttemptSession{}, err
	}
	if err := s.expireIfDue(ctx, p, id); err != nil {
		return AttemptSession{}, wrapAttempt("expire attempt", err)
	}
	var out AttemptSession
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		att, err := s.lock(ctx, tx, id)
		if err != nil {
			return err
		}
		if att.Status != AttemptInvited {
			return closedError(att)
		}
		a, err := tx.Q.GetAssessment(ctx, att.AssessmentID)
		if err != nil {
			return err
		}
		integrity := toAssessment(a).Integrity
		if ConsentRequired(integrity) && !att.ConsentAt.Valid {
			return ErrConsentRequired
		}
		now := s.Now()
		deadline := now.Add(time.Duration(a.DurationMinutes) * time.Minute)
		row, err := tx.Q.StartAttempt(ctx, db.StartAttemptParams{ID: id, StartedAt: ts(now), ExpiresAt: ts(deadline)})
		if err != nil {
			return err
		}
		// An invite window shorter than the session must not log the
		// candidate out mid-attempt.
		if err := tx.Q.ExtendMagicLinkExpiry(ctx, db.ExtendMagicLinkExpiryParams{
			Purpose: LinkAssessment, SubjectID: id, ExpiresAt: ts(deadline.Add(LinkGrace)),
		}); err != nil {
			return err
		}
		if ConsentRequired(integrity) {
			// The recording opens with what was agreed to, so a reviewer
			// reads the terms before the session they produced.
			if err := s.appendConsentEvent(ctx, tx, row, integrity); err != nil {
				return err
			}
			row, err = tx.Q.GetAttempt(ctx, id)
			if err != nil {
				return err
			}
		}
		out, err = s.session(ctx, tx, row)
		return err
	})
	if err != nil {
		return AttemptSession{}, wrapAttempt("start attempt", err)
	}
	return out, nil
}

// SaveSource records the editor's current text for a problem. It is what an
// expired attempt submits.
func (s *AttemptService) SaveSource(ctx context.Context, p Principal, attemptID, problemID uuid.UUID, language, source string) error {
	if err := requireCandidate(p, attemptID); err != nil {
		return err
	}
	if len(source) > MaxSourceBytes {
		return ErrSourceTooLarge
	}
	if err := s.expireIfDue(ctx, p, attemptID); err != nil {
		return wrapAttempt("expire attempt", err)
	}
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		att, err := s.lockStarted(ctx, tx, attemptID)
		if err != nil {
			return err
		}
		if _, err := s.checkLanguage(ctx, tx, att, problemID, language); err != nil {
			return err
		}
		return tx.Q.UpsertAttemptSource(ctx, db.UpsertAttemptSourceParams{
			AttemptID: attemptID, OrgID: att.OrgID, ProblemID: problemID, Language: language, Source: source, UpdatedAt: ts(s.Now()),
		})
	})
	return wrapAttempt("save source", err)
}

// Run queues the source against the problem's public cases.
func (s *AttemptService) Run(ctx context.Context, p Principal, attemptID, problemID uuid.UUID, language, source string) (Submission, error) {
	return s.execute(ctx, p, attemptID, problemID, SubmissionRun, language, source)
}

// Submit queues the source against every case of the problem.
func (s *AttemptService) Submit(ctx context.Context, p Principal, attemptID, problemID uuid.UUID, language, source string) (Submission, error) {
	return s.execute(ctx, p, attemptID, problemID, SubmissionSubmit, language, source)
}

func (s *AttemptService) execute(ctx context.Context, p Principal, attemptID, problemID uuid.UUID, kind, language, source string) (Submission, error) {
	if err := requireCandidate(p, attemptID); err != nil {
		return Submission{}, err
	}
	if len(source) > MaxSourceBytes {
		return Submission{}, ErrSourceTooLarge
	}
	if err := s.expireIfDue(ctx, p, attemptID); err != nil {
		return Submission{}, wrapAttempt("expire attempt", err)
	}
	var out Submission
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		att, err := s.lockStarted(ctx, tx, attemptID)
		if err != nil {
			return err
		}
		if _, err := s.checkLanguage(ctx, tx, att, problemID, language); err != nil {
			return err
		}
		pending, err := tx.Q.CountPendingSubmissions(ctx, db.CountPendingSubmissionsParams{AttemptID: attemptID, ProblemID: problemID})
		if err != nil {
			return err
		}
		if pending > 0 {
			return ErrSubmissionPending
		}
		// A submit is also the newest synced state, so an expiry right after
		// it does not resubmit older text.
		if err := tx.Q.UpsertAttemptSource(ctx, db.UpsertAttemptSourceParams{
			AttemptID: attemptID, OrgID: att.OrgID, ProblemID: problemID, Language: language, Source: source, UpdatedAt: ts(s.Now()),
		}); err != nil {
			return err
		}
		out, err = s.submit(ctx, tx, att, problemID, kind, language, source)
		return err
	})
	if err != nil {
		return Submission{}, wrapAttempt(kind, err)
	}
	return out, nil
}

// submit creates the submission row and queues its execution.
func (s *AttemptService) submit(ctx context.Context, tx *store.Tx, att db.Attempt, problemID uuid.UUID, kind, language, source string) (Submission, error) {
	row, err := tx.Q.CreateSubmission(ctx, db.CreateSubmissionParams{
		OrgID: att.OrgID, AttemptID: att.ID, ProblemID: problemID, Kind: kind, Language: language, Source: source,
	})
	if err != nil {
		return Submission{}, err
	}
	if s.q != nil {
		if _, err := s.q.Enqueue(ctx, tx, queue.KindRunnerExecute, RunnerExecutePayload{SubmissionID: row.ID, OrgID: att.OrgID}); err != nil {
			return Submission{}, err
		}
	}
	return toSubmission(row), nil
}

// Submission reads one submission so the page can poll its status. The
// result is projected to what a candidate may see; the raw runner output
// stays in the row.
func (s *AttemptService) Submission(ctx context.Context, p Principal, attemptID, id uuid.UUID) (Submission, error) {
	if err := requireCandidate(p, attemptID); err != nil {
		return Submission{}, err
	}
	var out Submission
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		row, err := tx.Q.GetSubmission(ctx, db.GetSubmissionParams{ID: id, AttemptID: attemptID})
		if err != nil {
			return err
		}
		out = toSubmission(row)
		if len(row.Result) > 0 {
			problem, err := loadProblem(ctx, tx, row.ProblemID)
			if err != nil {
				return err
			}
			r := CandidateResultOf(row.Result, problem.TestCases)
			out.CandidateResult = &r
		}
		return nil
	})
	if err != nil {
		return Submission{}, wrapAttempt("get submission", err)
	}
	return out, nil
}

// Finish closes a started attempt as submitted. Problems the candidate
// synced but never submitted are submitted from the synced text.
func (s *AttemptService) Finish(ctx context.Context, p Principal, attemptID uuid.UUID) (Attempt, error) {
	if err := requireCandidate(p, attemptID); err != nil {
		return Attempt{}, err
	}
	if err := s.expireIfDue(ctx, p, attemptID); err != nil {
		return Attempt{}, wrapAttempt("expire attempt", err)
	}
	var out Attempt
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		att, err := tx.Q.GetAttemptForUpdate(ctx, attemptID)
		if err != nil {
			return err
		}
		switch att.Status {
		case AttemptInvited:
			return ErrAttemptNotStarted
		case AttemptStarted:
			row, err := s.close(ctx, tx, att, AttemptSubmitted)
			if err != nil {
				return err
			}
			out = toAttempt(row)
		default:
			// Already closed (a retry, or expiry beat the click): report it.
			out = toAttempt(att)
		}
		return nil
	})
	if err != nil {
		return Attempt{}, wrapAttempt("finish attempt", err)
	}
	return out, nil
}

// RecordEvents appends a batch to the recording. Every seq must exceed the
// last one stored, in order, or the whole batch is refused so the client
// resends from where the server is. It returns the new last seq.
func (s *AttemptService) RecordEvents(ctx context.Context, p Principal, attemptID uuid.UUID, events []AttemptEvent) (int64, error) {
	if err := requireCandidate(p, attemptID); err != nil {
		return 0, err
	}
	if len(events) > MaxEventBatch {
		return 0, fmt.Errorf("%w: batch of %d exceeds %d", ErrEventInvalid, len(events), MaxEventBatch)
	}
	if err := s.expireIfDue(ctx, p, attemptID); err != nil {
		return 0, wrapAttempt("expire attempt", err)
	}
	var last int64
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		att, err := tx.Q.GetAttemptForUpdate(ctx, attemptID)
		if err != nil {
			return err
		}
		now := s.Now()
		// The last batch (the submit event, a beacon in flight) lands after
		// the close; anything later is not part of the session.
		switch att.Status {
		case AttemptStarted:
		case AttemptInvited:
			return ErrAttemptNotStarted
		default:
			if att.RecordingBlobKey != nil {
				// Finalization has compacted the stream into object storage;
				// a batch appended now would be missing from the replay.
				return fmt.Errorf("%w: the recording has been sealed", ErrAttemptClosed)
			}
			if att.FinishedAt.Valid && now.Before(att.FinishedAt.Time.Add(EventGrace)) {
				break
			}
			return closedError(att)
		}
		a, err := loadAssessment(ctx, tx, att.AssessmentID)
		if err != nil {
			return err
		}
		last = att.LastEventSeq
		for _, ev := range events {
			if ev.Seq <= last {
				return fmt.Errorf("%w: seq %d after %d", ErrEventSeq, ev.Seq, last)
			}
			if err := validateEvent(ev, a, now); err != nil {
				return err
			}
			if err := tx.Q.AppendAttemptEvent(ctx, db.AppendAttemptEventParams{
				OrgID: att.OrgID, AttemptID: attemptID, Seq: ev.Seq, Kind: ev.Type, Payload: ev.Data,
				ClientTs:  ts(time.UnixMilli(ev.T)),
				ProblemID: uuid.NullUUID{UUID: ev.ProblemID, Valid: true},
				ServerTs:  ts(now),
			}); err != nil {
				return err
			}
			last = ev.Seq
		}
		return tx.Q.SetAttemptLastEventSeq(ctx, db.SetAttemptLastEventSeqParams{ID: attemptID, LastEventSeq: last})
	})
	if err != nil {
		return 0, wrapAttempt("record events", err)
	}
	return last, nil
}

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// validateEvent checks one event against I5: a known type, a problem of the
// assessment, a sane client time, bounded data, and the per-type shape.
func validateEvent(ev AttemptEvent, a Assessment, now time.Time) error {
	if !attemptEventKinds[ev.Type] {
		return fmt.Errorf("%w: seq %d: unknown type %q", ErrEventInvalid, ev.Seq, ev.Type)
	}
	var problem *Problem
	for i := range a.Problems {
		if a.Problems[i].ID == ev.ProblemID {
			problem = &a.Problems[i]
		}
	}
	if problem == nil {
		return fmt.Errorf("%w: seq %d: problem %s is not in the assessment", ErrEventInvalid, ev.Seq, ev.ProblemID)
	}
	if skew := time.UnixMilli(ev.T).Sub(now); skew > EventClockSkew || skew < -EventClockSkew {
		return fmt.Errorf("%w: seq %d: client time is more than %v from now", ErrEventInvalid, ev.Seq, EventClockSkew)
	}
	if len(ev.Data) > MaxEventDataBytes {
		return fmt.Errorf("%w: seq %d: data exceeds %d bytes", ErrEventInvalid, ev.Seq, MaxEventDataBytes)
	}
	if len(ev.Data) == 0 || !json.Valid(ev.Data) {
		return fmt.Errorf("%w: seq %d: data is not JSON", ErrEventInvalid, ev.Seq)
	}
	var problems string
	switch ev.Type {
	case "paste":
		var d struct {
			Len      *int    `json:"len"`
			SHA256   *string `json:"sha256"`
			Internal *bool   `json:"internal"`
		}
		switch err := json.Unmarshal(ev.Data, &d); {
		case err != nil:
			problems = "paste data is not an object"
		case d.Len == nil || *d.Len < 0:
			problems = "paste len must be a non-negative integer"
		case d.SHA256 == nil || !sha256Hex.MatchString(*d.SHA256):
			problems = "paste sha256 must be 64 hex digits"
		case d.Internal == nil:
			problems = "paste internal must be a boolean"
		}
	case "run", "submit":
		var d struct {
			SubmissionID uuid.UUID `json:"submission_id"`
		}
		if err := json.Unmarshal(ev.Data, &d); err != nil || d.SubmissionID == uuid.Nil {
			problems = ev.Type + " data needs a submission_id"
		}
	case "lang_change":
		var d struct {
			Language string `json:"language"`
		}
		if err := json.Unmarshal(ev.Data, &d); err != nil || !containsString(sessionLanguages(a, *problem), d.Language) {
			problems = "lang_change language is not allowed for the problem"
		}
	case "keymap":
		var d struct {
			Keymap string `json:"keymap"`
		}
		if err := json.Unmarshal(ev.Data, &d); err != nil || !containsString(AttemptKeymaps, d.Keymap) {
			problems = "keymap must be one of " + strings.Join(AttemptKeymaps, ", ")
		}
	case "snapshot":
		var d struct {
			Seq *int  `json:"seq"`
			OK  *bool `json:"ok"`
		}
		switch err := json.Unmarshal(ev.Data, &d); {
		case err != nil:
			problems = "snapshot data is not an object"
		case d.Seq == nil || *d.Seq < 0:
			problems = "snapshot seq must be a non-negative integer"
		case d.OK == nil:
			problems = "snapshot ok must be a boolean"
		}
	case "consent":
		var d struct {
			Webcam  *bool `json:"webcam"`
			PhotoID *bool `json:"photo_id"`
		}
		if err := json.Unmarshal(ev.Data, &d); err != nil || d.Webcam == nil || d.PhotoID == nil {
			problems = "consent needs webcam and photo_id booleans"
		}
	}
	if problems != "" {
		return fmt.Errorf("%w: seq %d: %s", ErrEventInvalid, ev.Seq, problems)
	}
	return nil
}

// ExpireDue closes every started attempt whose deadline has passed, across
// all orgs, submitting synced sources and queueing finalization. The worker
// runs it periodically so an attempt the candidate abandoned still closes.
// It returns how many attempts it closed.
func (s *AttemptService) ExpireDue(ctx context.Context) (int, error) {
	now := s.Now()
	var orgs []uuid.UUID
	err := s.st.WithTx(ctx, orgScoped(PlatformOrgID), func(ctx context.Context, tx *store.Tx) error {
		var err error
		orgs, err = tx.Q.ListDueAttemptOrgs(ctx, ts(now))
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("expire due: %w", err)
	}
	n := 0
	for _, orgID := range orgs {
		err := s.st.WithTx(ctx, orgScoped(orgID), func(ctx context.Context, tx *store.Tx) error {
			due, err := tx.Q.ListDueAttempts(ctx, ts(now))
			if err != nil {
				return err
			}
			for _, att := range due {
				if _, err := s.close(ctx, tx, att, AttemptExpired); err != nil {
					return err
				}
				n++
			}
			return nil
		})
		if err != nil {
			return n, fmt.Errorf("expire due: org %s: %w", orgID, err)
		}
	}
	return n, nil
}

// expireIfDue commits an expiry for a started attempt past its deadline:
// the synced sources are submitted and finalization queued. It runs in its
// own transaction, before the caller's, so the expiry outlives the error the
// caller then returns.
func (s *AttemptService) expireIfDue(ctx context.Context, p Principal, id uuid.UUID) error {
	return s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		att, err := tx.Q.GetAttemptForUpdate(ctx, id)
		if err != nil {
			return err
		}
		if att.Status != AttemptStarted || s.Now().Before(att.ExpiresAt.Time) {
			return nil
		}
		_, err = s.close(ctx, tx, att, AttemptExpired)
		return err
	})
}

// lock loads the attempt under a row lock and applies the clock: a started
// attempt past its deadline is ErrAttemptExpired (expireIfDue has already
// closed it) and an invite past its window is ErrInviteExpired.
func (s *AttemptService) lock(ctx context.Context, tx *store.Tx, id uuid.UUID) (db.Attempt, error) {
	att, err := tx.Q.GetAttemptForUpdate(ctx, id)
	if err != nil {
		return db.Attempt{}, err
	}
	now := s.Now()
	switch att.Status {
	case AttemptExpired:
		return db.Attempt{}, ErrAttemptExpired
	case AttemptStarted:
		if !now.Before(att.ExpiresAt.Time) {
			return db.Attempt{}, ErrAttemptExpired
		}
	case AttemptInvited:
		if att.InviteExpiresAt.Valid && !now.Before(att.InviteExpiresAt.Time) {
			return db.Attempt{}, ErrInviteExpired
		}
	}
	return att, nil
}

func (s *AttemptService) lockStarted(ctx context.Context, tx *store.Tx, id uuid.UUID) (db.Attempt, error) {
	att, err := s.lock(ctx, tx, id)
	if err != nil {
		return db.Attempt{}, err
	}
	if att.Status != AttemptStarted {
		return db.Attempt{}, closedError(att)
	}
	return att, nil
}

func closedError(att db.Attempt) error {
	switch att.Status {
	case AttemptInvited:
		return ErrAttemptNotStarted
	case AttemptExpired:
		return ErrAttemptExpired
	}
	return ErrAttemptClosed
}

// close ends a started attempt with status, submitting each synced source
// that was never submitted, and queues finalization.
func (s *AttemptService) close(ctx context.Context, tx *store.Tx, att db.Attempt, status string) (db.Attempt, error) {
	sources, err := tx.Q.ListAttemptSources(ctx, att.ID)
	if err != nil {
		return db.Attempt{}, err
	}
	subs, err := tx.Q.ListSubmissions(ctx, att.ID)
	if err != nil {
		return db.Attempt{}, err
	}
	submitted := make(map[uuid.UUID]string)
	for _, sub := range subs {
		if sub.Kind == SubmissionSubmit {
			submitted[sub.ProblemID] = sub.Source
		}
	}
	for _, src := range sources {
		if last, ok := submitted[src.ProblemID]; ok && last == src.Source {
			continue
		}
		if _, err := s.submit(ctx, tx, att, src.ProblemID, SubmissionSubmit, src.Language, src.Source); err != nil {
			return db.Attempt{}, err
		}
	}
	finishedAt := s.Now()
	row, err := tx.Q.CloseAttempt(ctx, db.CloseAttemptParams{ID: att.ID, Status: status, FinishedAt: ts(finishedAt)})
	if err != nil {
		return db.Attempt{}, err
	}
	if s.q != nil {
		// The island may still land its last batch until EventGrace after the
		// close; finalizing before then would seal a recording without it.
		if _, err := s.q.EnqueueAt(ctx, tx, queue.KindAttemptFinalize,
			AttemptFinalizePayload{AttemptID: att.ID, OrgID: att.OrgID}, finishedAt.Add(EventGrace)); err != nil {
			return db.Attempt{}, err
		}
	}
	return row, nil
}

// checkLanguage confirms the problem belongs to the attempt's assessment and
// allows the language, returning the problem.
func (s *AttemptService) checkLanguage(ctx context.Context, tx *store.Tx, att db.Attempt, problemID uuid.UUID, language string) (Problem, error) {
	a, err := loadAssessment(ctx, tx, att.AssessmentID)
	if err != nil {
		return Problem{}, err
	}
	for _, p := range a.Problems {
		if p.ID != problemID {
			continue
		}
		if !containsString(sessionLanguages(a, p), language) {
			return Problem{}, ErrLanguageNotAllowed
		}
		return p, nil
	}
	return Problem{}, ErrProblemNotInSet
}

// sessionLanguages narrows a problem's languages to the assessment's allowed
// set and then to its override. An override outside that intersection is
// ignored rather than leaving the candidate with nothing.
func sessionLanguages(a Assessment, p Problem) []string {
	langs := intersectLanguages(a.AllowedLanguages, p.AllowedLanguages)
	if a.LanguageOverride != "" && containsString(langs, a.LanguageOverride) {
		return []string{a.LanguageOverride}
	}
	return langs
}

func (s *AttemptService) session(ctx context.Context, tx *store.Tx, att db.Attempt) (AttemptSession, error) {
	a, err := loadAssessment(ctx, tx, att.AssessmentID)
	if err != nil {
		return AttemptSession{}, err
	}
	sources, err := tx.Q.ListAttemptSources(ctx, att.ID)
	if err != nil {
		return AttemptSession{}, err
	}
	synced := make(map[uuid.UUID]db.AttemptSource, len(sources))
	for _, src := range sources {
		synced[src.ProblemID] = src
	}
	out := AttemptSession{Attempt: toAttempt(att), Assessment: a}
	if att.ApplicationID.Valid {
		card, err := tx.Q.GetApplicationCard(ctx, att.ApplicationID.UUID)
		if err != nil {
			return AttemptSession{}, err
		}
		out.CandidateName, out.JobTitle = card.CandidateName, card.JobTitle
	} else {
		out.CandidateName, out.JobTitle = PreviewCandidateName, a.Name
	}
	out.Assessment.Problems = nil
	for _, p := range a.Problems {
		sp := SessionProblem{
			ID: p.ID, Kind: p.Kind, Title: p.Title, Statement: p.Statement, Difficulty: p.Difficulty,
			Languages: sessionLanguages(a, p), SQLSchema: p.SQLSchema,
		}
		for _, tc := range p.TestCases {
			if tc.Visibility == "public" {
				sp.PublicTests = append(sp.PublicTests, tc)
			}
		}
		if src, ok := synced[p.ID]; ok {
			sp.Language, sp.Source = src.Language, src.Source
		}
		out.Problems = append(out.Problems, sp)
	}
	if att.Status == AttemptStarted {
		out.Remaining = att.ExpiresAt.Time.Sub(s.Now())
	}
	return out, nil
}

func toAttempt(r db.Attempt) Attempt {
	return Attempt{
		ID: r.ID, OrgID: r.OrgID, ApplicationID: r.ApplicationID.UUID, AssessmentID: r.AssessmentID, StageID: r.StageID.UUID,
		Preview: r.Preview,
		Status:  r.Status, InvitedAt: r.InvitedAt.Time.UTC(), InviteExpiresAt: r.InviteExpiresAt.Time.UTC(),
		StartedAt: r.StartedAt.Time.UTC(), ExpiresAt: r.ExpiresAt.Time.UTC(), FinishedAt: r.FinishedAt.Time.UTC(),
		LastEventSeq: r.LastEventSeq,
	}
}

func toSubmission(r db.Submission) Submission {
	out := Submission{
		ID: r.ID, AttemptID: r.AttemptID, ProblemID: r.ProblemID, Kind: r.Kind, Language: r.Language, Source: r.Source,
		Status: r.Status, Result: r.Result, CreatedAt: r.CreatedAt.Time.UTC(), UpdatedAt: r.UpdatedAt.Time.UTC(),
	}
	if f, err := r.Score.Float64Value(); err == nil && f.Valid {
		out.Score = &f.Float64
	}
	return out
}

func wrapAttempt(what string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pgx.ErrNoRows):
		return ErrNotFound
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrForbidden), errors.Is(err, ErrInviteExpired),
		errors.Is(err, ErrAttemptExpired), errors.Is(err, ErrAttemptNotStarted), errors.Is(err, ErrAttemptClosed),
		errors.Is(err, ErrEventSeq), errors.Is(err, ErrEventKind), errors.Is(err, ErrLanguageNotAllowed),
		errors.Is(err, ErrProblemNotInSet), errors.Is(err, ErrSubmissionPending), errors.Is(err, ErrSourceTooLarge),
		errors.Is(err, ErrEventInvalid):
		return err
	}
	return fmt.Errorf("%s: %w", what, err)
}
