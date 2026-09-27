package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"recruiting/internal/queue"
	"recruiting/internal/runner/server"
	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

// Recording statuses; they mirror the attempt.recording_status check. A
// recording is incomplete when events the server accepted a seq for never
// arrived, which is what the replay viewer and the signal confidence read.
const (
	RecordingPending    = "pending"
	RecordingComplete   = "complete"
	RecordingIncomplete = "incomplete"
)

// eventsContentType is what the compacted stream is stored as: gzipped JSON
// lines, one event per line, in seq order.
const eventsContentType = "application/gzip"

// How long finalization waits for submissions still in the runner. One call
// to the runner can take five minutes, and a saturated runner is waited out
// for another minute on top, so the budget has to exceed both or a slow
// execution would be declared abandoned while it is still running.
const (
	FinalizeWaitDelay = 30 * time.Second
	MaxFinalizeWaits  = 14
)

// abandonedResult is what a submission the runner never answered for is left
// with, so the row says why it has no verdict.
const abandonedResult = "abandoned at finalize: the runner never answered"

// ProblemScore is one problem's outcome on an attempt: the weighted share of
// its cases the final submit passed. It is stored on the attempt so the
// portal and the review screen read the breakdown without re-deriving it.
type ProblemScore struct {
	ProblemID    uuid.UUID `json:"problem_id"`
	SubmissionID uuid.UUID `json:"submission_id,omitempty"`
	// Score is the weighted share passed, 0–1.
	Score        float64 `json:"score"`
	PassedWeight float64 `json:"passed_weight"`
	TotalWeight  float64 `json:"total_weight"`
	// Status is the runner's verdict for the scored submit, empty when the
	// problem was never submitted.
	Status string `json:"status,omitempty"`
}

// RecordedEvent is one event as it appears in the compacted stream.
type RecordedEvent struct {
	Seq       int64           `json:"seq"`
	Kind      string          `json:"kind"`
	ProblemID *uuid.UUID      `json:"problem_id,omitempty"`
	ClientTs  *time.Time      `json:"client_ts,omitempty"`
	ServerTs  time.Time       `json:"server_ts"`
	Payload   json.RawMessage `json:"payload"`
}

// AttemptFinalizeJob is the attempt.finalize payload as finalization
// re-enqueues it: the agreed fields plus how many times the job has already
// stood aside for submissions that were still running.
type AttemptFinalizeJob struct {
	AttemptFinalizePayload
	Wait int `json:"wait,omitempty"`
}

// SignalsComputePayload is the signals.compute payload.
type SignalsComputePayload struct {
	AttemptID uuid.UUID `json:"attempt_id"`
	OrgID     uuid.UUID `json:"org_id"`
}

// ScoringService finalizes a closed attempt: it scores it, moves the
// recording out of Postgres, and hands off to the signal computation.
type ScoringService struct {
	st   *store.Store
	q    *queue.Client
	blob BlobStore
	// Logger records a recording that could not be compacted. Nil disables it.
	Logger *slog.Logger
	// Now is the clock the reschedule delay is measured from; tests replace it.
	Now func() time.Time
	// Apps applies an assessment stage's own decision rule once the score is
	// in. Nil leaves every decision to a person.
	Apps *ApplicationService
}

// NewScoringService wires the store, the queue signals.compute goes to, and
// object storage. A nil blob store scores the attempt and leaves the
// recording in Postgres rather than failing the attempt over it.
func NewScoringService(st *store.Store, q *queue.Client, b BlobStore) *ScoringService {
	return &ScoringService{st: st, q: q, blob: b, Now: time.Now}
}

// AttemptFinalizeHandler works attempt.finalize. Wire it into the worker's
// handler table under queue.KindAttemptFinalize.
func AttemptFinalizeHandler(st *store.Store, q *queue.Client, b BlobStore, apps *ApplicationService, logger *slog.Logger) queue.Handler {
	s := NewScoringService(st, q, b)
	s.Logger = logger
	s.Apps = apps
	return func(ctx context.Context, job queue.Job) error {
		var p AttemptFinalizeJob
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			return fmt.Errorf("attempt.finalize payload: %w", err)
		}
		if p.AttemptID == uuid.Nil || p.OrgID == uuid.Nil {
			return errors.New("attempt.finalize payload names no attempt or org")
		}
		return s.Finalize(ctx, p.AttemptFinalizePayload, p.Wait)
	}
}

// Finalize scores the attempt, compacts its recording, and queues the
// signals. Submissions still in the runner hold it up: the job re-enqueues
// itself with a delay, wait counting how often it already has, and once that
// budget is spent those submissions are given up on rather than leaving the
// attempt unscored forever. Only a closed attempt is scored, and the score is
// written once however many deliveries race here.
func (s *ScoringService) Finalize(ctx context.Context, p AttemptFinalizePayload, wait int) error {
	att, scores, waiting, err := s.collect(ctx, p, wait)
	if err != nil {
		return fmt.Errorf("attempt.finalize: %w", err)
	}
	if waiting || att.ID == uuid.Nil {
		// Come back when the runner has answered, or nothing to do: the
		// attempt is still with the candidate, or already scored.
		return nil
	}
	key, status, err := s.compact(ctx, p, att)
	if err != nil {
		return fmt.Errorf("attempt.finalize: %w", err)
	}
	err = s.st.WithTx(ctx, orgScoped(p.OrgID), func(ctx context.Context, tx *store.Tx) error {
		errCount, err := tx.Q.CountAttemptSubmissionErrors(ctx, p.AttemptID)
		if err != nil {
			return err
		}
		breakdown, err := json.Marshal(scores)
		if err != nil {
			return err
		}
		params := db.ScoreAttemptParams{
			ID: p.AttemptID, Score: numeric(attemptScore(scores)), ProblemScores: breakdown,
			ErrorCount: int32(errCount), RecordingStatus: status,
		}
		if key != "" {
			params.RecordingBlobKey = &key
		} else {
			params.RecordingBlobKey = att.RecordingBlobKey
		}
		switch _, err := tx.Q.ScoreAttempt(ctx, params); {
		case errors.Is(err, pgx.ErrNoRows):
			// Another delivery scored it first; the signals are its to queue.
			return nil
		case err != nil:
			return err
		}
		if att.Preview {
			// A preview is scored so the recruiter sees the same results a
			// candidate would; it moves nothing and its integrity signals
			// are nobody's to read.
			return nil
		}
		if s.Apps != nil && att.ApplicationID.Valid {
			if err := s.Apps.AutoDecide(ctx, tx, p.OrgID, att.ApplicationID.UUID, attemptScore(scores)); err != nil {
				return fmt.Errorf("auto decide: %w", err)
			}
		}
		if s.q == nil {
			return nil
		}
		return enqueued(s.q.Enqueue(ctx, tx, queue.KindSignalsCompute, SignalsComputePayload{AttemptID: p.AttemptID, OrgID: p.OrgID}))
	})
	if err != nil {
		return fmt.Errorf("attempt.finalize: %w", err)
	}
	return nil
}

// collect reads the attempt and scores each of its problems. It reports
// waiting when it has rescheduled itself instead, and a zero attempt when
// there is nothing to finalize: the attempt is still open, or already scored.
func (s *ScoringService) collect(ctx context.Context, p AttemptFinalizePayload, wait int) (db.Attempt, []ProblemScore, bool, error) {
	var att db.Attempt
	var scores []ProblemScore
	var waiting bool
	err := s.st.WithTx(ctx, orgScoped(p.OrgID), func(ctx context.Context, tx *store.Tx) error {
		row, err := tx.Q.GetAttemptForUpdate(ctx, p.AttemptID)
		if err != nil {
			return err
		}
		switch row.Status {
		case AttemptSubmitted, AttemptExpired:
		default:
			// Still with the candidate, or already scored: nothing to do.
			return nil
		}
		subs, err := tx.Q.ListSubmissions(ctx, p.AttemptID)
		if err != nil {
			return err
		}
		if pending := pendingSubmissions(subs); len(pending) > 0 {
			if wait < MaxFinalizeWaits && s.q != nil {
				waiting = true
				return enqueued(s.q.EnqueueAt(ctx, tx, queue.KindAttemptFinalize,
					AttemptFinalizeJob{AttemptFinalizePayload: p, Wait: wait + 1}, s.now().Add(FinalizeWaitDelay)))
			}
			// The wait is spent. Give the submissions up here, under the
			// attempt's lock, so the score and the error count describe what
			// actually happened and a late runner reply changes nothing.
			if err := s.abandon(ctx, tx, pending); err != nil {
				return err
			}
			if subs, err = tx.Q.ListSubmissions(ctx, p.AttemptID); err != nil {
				return err
			}
		}
		a, err := loadAssessment(ctx, tx, row.AssessmentID)
		if err != nil {
			return err
		}
		scores = problemScores(a.Problems, subs)
		att = row
		return nil
	})
	return att, scores, waiting, err
}

// abandon marks submissions the runner never answered for as errors, which
// is what the vetter's error count reads.
func (s *ScoringService) abandon(ctx context.Context, tx *store.Tx, pending []db.Submission) error {
	for _, sub := range pending {
		raw, err := json.Marshal(server.Response{ID: sub.ID.String(), Status: server.StatusError, CompileOutput: abandonedResult})
		if err != nil {
			return err
		}
		if err := tx.Q.FinishSubmission(ctx, db.FinishSubmissionParams{ID: sub.ID, Status: SubmissionError, Result: raw}); err != nil {
			return err
		}
		if s.Logger != nil {
			s.Logger.Warn("giving up on a submission the runner never answered", "submission_id", sub.ID)
		}
	}
	return nil
}

func (s *ScoringService) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

// compact writes the recording to object storage and reports the key and the
// recording's status. Without object storage the events stay in Postgres; the
// attempt is still scored and the gap check still stands. The stream is
// walked a page at a time into a gzip spool on disk and uploaded from there
// with its size known, so a long sitting never sits in the worker's memory:
// the S3 client buffers an object it is not told the size of.
func (s *ScoringService) compact(ctx context.Context, p AttemptFinalizePayload, att db.Attempt) (string, string, error) {
	if s.blob == nil {
		var stored int64
		err := s.st.WithTx(ctx, orgScoped(p.OrgID), func(ctx context.Context, tx *store.Tx) error {
			var err error
			stored, err = tx.Q.CountAttemptEvents(ctx, p.AttemptID)
			return err
		})
		if err != nil {
			return "", RecordingPending, err
		}
		if s.Logger != nil {
			s.Logger.Warn("no object storage configured; the recording stays in the database", "attempt_id", p.AttemptID)
		}
		return "", recordingStatus(stored, att.LastEventSeq), nil
	}
	spool, err := os.CreateTemp("", "recording-*.jsonl.gz")
	if err != nil {
		return "", RecordingPending, fmt.Errorf("compact events: %w", err)
	}
	defer os.Remove(spool.Name())
	defer spool.Close()
	stored, err := s.spool(ctx, p, spool)
	if err != nil {
		return "", RecordingPending, err
	}
	status := recordingStatus(stored, att.LastEventSeq)
	size, err := spool.Seek(0, io.SeekEnd)
	if err != nil {
		return "", status, fmt.Errorf("compact events: %w", err)
	}
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return "", status, fmt.Errorf("compact events: %w", err)
	}
	key := eventsBlobKey(p.AttemptID)
	if err := s.blob.Put(ctx, key, spool, size, eventsContentType); err != nil {
		return "", status, err
	}
	return key, status, nil
}

// spool writes the compacted stream to w, a page of rows at a time, and
// reports how many events it holds.
func (s *ScoringService) spool(ctx context.Context, p AttemptFinalizePayload, w io.Writer) (int64, error) {
	rw := newRecordingWriter(w)
	var stored int64
	err := s.st.WithTx(ctx, orgScoped(p.OrgID), func(ctx context.Context, tx *store.Tx) error {
		var err error
		stored, err = eachAttemptEvent(ctx, tx, p.AttemptID, rw.write)
		return err
	})
	if err != nil {
		return 0, err
	}
	if err := rw.close(); err != nil {
		return 0, err
	}
	return stored, nil
}

// eventsBlobKey is where an attempt's compacted recording lives.
func eventsBlobKey(attemptID uuid.UUID) string {
	return "attempts/" + attemptID.String() + "/events.jsonl.gz"
}

// recordingStatus compares the events actually stored with the highest seq
// the server accepted. Fewer rows than seqs means a batch was lost on the
// way, so the recording cannot be replayed in full.
func recordingStatus(stored, lastSeq int64) string {
	if stored < lastSeq {
		return RecordingIncomplete
	}
	return RecordingComplete
}

// pendingSubmissions is the submissions the runner has not answered for.
func pendingSubmissions(subs []db.Submission) []db.Submission {
	var out []db.Submission
	for _, sub := range subs {
		if sub.Status == SubmissionQueued || sub.Status == SubmissionRunning {
			out = append(out, sub)
		}
	}
	return out
}

// problemScores scores every problem of the assessment against its final
// submit — the newest one the runner returned a verdict for, so a submit that
// errored falls back to the last answered attempt at the problem rather than
// discarding the candidate's work. A problem with no answered submit at all
// scores zero.
func problemScores(problems []Problem, subs []db.Submission) []ProblemScore {
	final := make(map[uuid.UUID]db.Submission, len(problems))
	for _, sub := range subs {
		if sub.Kind == SubmissionSubmit && sub.Status == SubmissionDone {
			final[sub.ProblemID] = sub // ordered by created_at, so the last wins
		}
	}
	out := make([]ProblemScore, 0, len(problems))
	for _, p := range problems {
		score := ProblemScore{ProblemID: p.ID}
		for _, c := range p.TestCases {
			score.TotalWeight += c.Weight
		}
		if sub, ok := final[p.ID]; ok {
			var res server.Response
			_ = json.Unmarshal(sub.Result, &res)
			score.SubmissionID, score.Status = sub.ID, res.Status
			score.PassedWeight, score.TotalWeight = weightedScore(p.TestCases, res)
		}
		if score.TotalWeight > 0 {
			score.Score = score.PassedWeight / score.TotalWeight
		}
		out = append(out, score)
	}
	return out
}

// weightedScore is the weight of the cases the runner reported passing,
// against the weight of every case asked about. The spec's "weighted hidden
// tests passed" is read as the whole set: a submit is judged on the public
// cases and the hidden ones together, so the total is every case of the
// problem. Results naming a case the problem no longer has earn nothing.
func weightedScore(cases []ProblemTestCase, res server.Response) (passed, total float64) {
	byID := make(map[string]ProblemTestCase, len(cases))
	for _, c := range cases {
		byID[c.ID.String()] = c
		total += c.Weight
	}
	for _, r := range res.Results {
		c, ok := byID[r.TestID]
		if !ok || r.Status != server.TestPass {
			continue
		}
		passed += c.Weight
	}
	return passed, total
}

// attemptScore is the mean of the problem scores as a percentage, to two
// decimals: the stored number is displayed, not computed on further.
func attemptScore(scores []ProblemScore) float64 {
	if len(scores) == 0 {
		return 0
	}
	var sum float64
	for _, s := range scores {
		sum += s.Score
	}
	return math.Round(100*sum/float64(len(scores))*100) / 100
}
