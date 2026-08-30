package service

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"recruiting/internal/domain/signals"
	"recruiting/internal/queue"
	"recruiting/internal/runner/server"
	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

// BlobReader is the slice of object storage the signal computation needs:
// reading the compacted recording back. blob.Client satisfies it.
type BlobReader interface {
	Get(ctx context.Context, key string) (io.ReadCloser, error)
}

// SignalsService computes an attempt's integrity signals and risk score.
// It reads the recording, the submissions, the problems, and the org's
// weights, and writes integrity_signal rows and attempt.risk_score. It never
// touches the application or the pipeline: a signal is for a reviewer to
// read, not a move.
type SignalsService struct {
	st   *store.Store
	blob BlobReader
	// Logger notes a recording read from Postgres instead of object storage.
	// Nil disables it.
	Logger *slog.Logger
}

// NewSignalsService wires the store and object storage. A nil reader, or a
// recording that is not in storage, is read from the attempt_event rows.
func NewSignalsService(st *store.Store, b BlobReader) *SignalsService {
	return &SignalsService{st: st, blob: b}
}

// SignalsComputeHandler works signals.compute. Wire it into the worker's
// handler table under queue.KindSignalsCompute.
func SignalsComputeHandler(st *store.Store, b BlobReader, logger *slog.Logger) queue.Handler {
	s := NewSignalsService(st, b)
	s.Logger = logger
	return func(ctx context.Context, job queue.Job) error {
		var p SignalsComputePayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			return fmt.Errorf("signals.compute payload: %w", err)
		}
		if p.AttemptID == uuid.Nil || p.OrgID == uuid.Nil {
			return errors.New("signals.compute payload names no attempt or org")
		}
		return s.Compute(ctx, p)
	}
}

// Compute computes and stores the attempt's signals. It is idempotent: a
// redelivery replaces the rows and the score with the same values.
func (s *SignalsService) Compute(ctx context.Context, p SignalsComputePayload) error {
	in, weights, err := s.load(ctx, p)
	if err != nil {
		return fmt.Errorf("signals.compute: %w", err)
	}
	sigs := signals.Compute(in)
	risk := signals.Risk(sigs, weights)
	err = s.st.WithTx(ctx, orgScoped(p.OrgID), func(ctx context.Context, tx *store.Tx) error {
		if err := tx.Q.DeleteIntegritySignals(ctx, p.AttemptID); err != nil {
			return err
		}
		for _, sig := range sigs {
			evidence, err := json.Marshal(sig.Evidence)
			if err != nil {
				return err
			}
			if err := tx.Q.CreateIntegritySignal(ctx, db.CreateIntegritySignalParams{
				OrgID: p.OrgID, AttemptID: p.AttemptID, Name: sig.Name, Value: numeric(sig.Value),
				Weight: numeric(weights[sig.Name]), Confidence: sig.Confidence, Evidence: evidence,
			}); err != nil {
				return err
			}
		}
		return tx.Q.SetAttemptRiskScore(ctx, db.SetAttemptRiskScoreParams{ID: p.AttemptID, RiskScore: numeric(risk)})
	})
	if err != nil {
		return fmt.Errorf("signals.compute: %w", err)
	}
	return nil
}

// load gathers the signal input and the org's weights.
func (s *SignalsService) load(ctx context.Context, p SignalsComputePayload) (signals.Input, map[string]float64, error) {
	var in signals.Input
	var att db.Attempt
	var weights map[string]float64
	err := s.st.WithTx(ctx, orgScoped(p.OrgID), func(ctx context.Context, tx *store.Tx) error {
		var err error
		if att, err = tx.Q.GetAttempt(ctx, p.AttemptID); err != nil {
			return err
		}
		if att.StartedAt.Valid {
			in.StartedAt = att.StartedAt.Time.UTC()
		}
		in.Incomplete = att.RecordingStatus == RecordingIncomplete
		if in.Events, err = s.events(ctx, tx, att); err != nil {
			return err
		}
		subs, err := tx.Q.ListSubmissions(ctx, p.AttemptID)
		if err != nil {
			return err
		}
		in.Submissions = signalSubmissions(subs)
		sources, err := tx.Q.ListAttemptSources(ctx, p.AttemptID)
		if err != nil {
			return err
		}
		in.InitialSources = initialSources(in.Events, sources)
		a, err := loadAssessment(ctx, tx, att.AssessmentID)
		if err != nil {
			return err
		}
		for _, problem := range a.Problems {
			language, final := finalSource(problem.ID, subs, sources)
			sp := signals.Problem{ID: problem.ID, Difficulty: problem.Difficulty, Language: language, FinalSource: final}
			for _, ref := range problem.References {
				sp.References = append(sp.References, signals.Source{Language: ref.Language, Source: ref.Source})
			}
			others, err := tx.Q.ListOtherProblemSubmits(ctx, db.ListOtherProblemSubmitsParams{ProblemID: problem.ID, AttemptID: p.AttemptID, Language: language})
			if err != nil {
				return err
			}
			for _, o := range others {
				sp.Others = append(sp.Others, signals.Source{Language: language, Source: o.Source})
			}
			in.Problems = append(in.Problems, sp)
		}
		weights, err = integrityWeights(ctx, tx, p.OrgID)
		return err
	})
	return in, weights, err
}

// events reads the recording: the compacted stream when finalization put
// it in object storage, the attempt_event rows otherwise. Rows outlive the
// compaction, so a missing object is not fatal either.
func (s *SignalsService) events(ctx context.Context, tx *store.Tx, att db.Attempt) ([]signals.Event, error) {
	if s.blob != nil && att.RecordingBlobKey != nil {
		events, err := s.readCompacted(ctx, *att.RecordingBlobKey)
		if err == nil {
			return events, nil
		}
		if s.Logger != nil {
			s.Logger.Warn("reading the recording from the database instead of object storage", "attempt_id", att.ID, "error", err)
		}
	}
	rows, err := tx.Q.ListAttemptEvents(ctx, att.ID)
	if err != nil {
		return nil, err
	}
	out := make([]signals.Event, 0, len(rows))
	for _, ev := range recordedEvents(rows) {
		out = append(out, signals.Event(ev))
	}
	return out, nil
}

func (s *SignalsService) readCompacted(ctx context.Context, key string) ([]signals.Event, error) {
	rc, err := s.blob.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	gz, err := gzip.NewReader(rc)
	if err != nil {
		return nil, fmt.Errorf("gunzip %s: %w", key, err)
	}
	defer gz.Close()
	var out []signals.Event
	sc := bufio.NewScanner(gz)
	sc.Buffer(make([]byte, 0, 64<<10), MaxEventDataBytes*2)
	for sc.Scan() {
		var ev signals.Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			return nil, fmt.Errorf("decode %s: %w", key, err)
		}
		out = append(out, ev)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", key, err)
	}
	return out, nil
}

// signalSubmissions projects the rows to what the signals read: when each
// was made and whether the runner passed every case.
func signalSubmissions(subs []db.Submission) []signals.Submission {
	out := make([]signals.Submission, 0, len(subs))
	for _, sub := range subs {
		ss := signals.Submission{ID: sub.ID, ProblemID: sub.ProblemID, Kind: sub.Kind, At: sub.CreatedAt.Time.UTC(), Source: sub.Source}
		if sub.Status == SubmissionDone && len(sub.Result) > 0 {
			var res server.Response
			if err := json.Unmarshal(sub.Result, &res); err == nil && res.Status == server.StatusOK && len(res.Results) > 0 {
				ss.Passed = true
				for _, r := range res.Results {
					if r.Status != server.TestPass {
						ss.Passed = false
						break
					}
				}
			}
		}
		out = append(out, ss)
	}
	return out
}

// finalSource is the candidate's last text for the problem and its
// language: the last submit, else the last synced editor state.
func finalSource(problemID uuid.UUID, subs []db.Submission, sources []db.AttemptSource) (language, source string) {
	for _, sub := range subs {
		if sub.ProblemID == problemID && sub.Kind == SubmissionSubmit {
			language, source = sub.Language, sub.Source // ordered by created_at, so the last wins
		}
	}
	if source != "" {
		return language, source
	}
	for _, src := range sources {
		if src.ProblemID == problemID {
			return src.Language, src.Source
		}
	}
	return "", ""
}

// initialSources is the editor text each problem started from. Nothing
// records the text before the first event, so the synced source stands in
// only for a problem whose stream holds no edit at all: the text was there
// before recording began and nothing has changed it since.
func initialSources(events []signals.Event, sources []db.AttemptSource) map[uuid.UUID]string {
	edited := map[uuid.UUID]bool{}
	for _, ev := range events {
		if ev.Kind == "edit" && ev.ProblemID != nil {
			edited[*ev.ProblemID] = true
		}
	}
	out := map[uuid.UUID]string{}
	for _, src := range sources {
		if !edited[src.ProblemID] {
			out[src.ProblemID] = src.Source
		}
	}
	return out
}

// integrityWeights reads the org's weights, defaults filling in for signals
// it never reweighted, the same way the settings form does.
func integrityWeights(ctx context.Context, tx *store.Tx, orgID uuid.UUID) (map[string]float64, error) {
	weights := DefaultSettings().IntegrityWeights
	row, err := tx.Q.GetOrgSetting(ctx, db.GetOrgSettingParams{OrgID: orgID, Key: SettingIntegrityWeights})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return weights, nil
		}
		return nil, err
	}
	var stored map[string]float64
	if err := json.Unmarshal(row, &stored); err != nil {
		return nil, fmt.Errorf("%w: %s is not valid JSON", ErrInvalidSettings, SettingIntegrityWeights)
	}
	for _, name := range IntegritySignalNames {
		if v, ok := stored[name]; ok {
			weights[name] = v
		}
	}
	return weights, nil
}
