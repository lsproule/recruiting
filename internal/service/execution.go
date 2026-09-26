package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"recruiting/internal/queue"
	"recruiting/internal/runner/server"
	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

// ExecutionService works one submission: it hands the code and its cases to
// the runner and writes back what came out. It is the only path candidate
// code takes to the sandbox.
type ExecutionService struct {
	st   *store.Store
	exec Executor
	// Logger records a runner that could not be reached. Nil disables it.
	Logger *slog.Logger
}

// NewExecutionService wires the store and the runner. exec must not be nil:
// there is nothing to fall back to.
func NewExecutionService(st *store.Store, exec Executor) *ExecutionService {
	return &ExecutionService{st: st, exec: exec}
}

// RunnerExecuteHandler works runner.execute. Wire it into the worker's
// handler table under queue.KindRunnerExecute.
func RunnerExecuteHandler(st *store.Store, exec Executor, logger *slog.Logger) queue.Handler {
	s := NewExecutionService(st, exec)
	s.Logger = logger
	return func(ctx context.Context, job queue.Job) error {
		var p RunnerExecutePayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			return fmt.Errorf("runner.execute payload: %w", err)
		}
		if p.SubmissionID == uuid.Nil || p.OrgID == uuid.Nil {
			return errors.New("runner.execute payload names no submission or org")
		}
		return s.Execute(ctx, p, job.Attempt)
	}
}

// Execute runs the submission the payload names. attempt is the queue's
// delivery count: while the queue will still retry, a runner that cannot be
// reached or that returns no verdict is reported as an error so the job comes
// back; on the last delivery the submission is marked error instead, which the
// candidate may resubmit past and the vetter sees as the attempt's error
// count. A submission that already reached a terminal status is left alone, so
// a redelivered job — or a reply that arrives after finalization gave up on it
// — runs and changes nothing.
func (s *ExecutionService) Execute(ctx context.Context, p RunnerExecutePayload, attempt int) error {
	req, cases, err := s.claim(ctx, p)
	if err != nil {
		return fmt.Errorf("runner.execute: %w", err)
	}
	if req.ID == "" {
		return nil // already terminal; a redelivered job runs nothing twice
	}
	res, runErr := s.exec.Execute(ctx, req)
	if runErr == nil && submissionStatusOf(res.Status) != SubmissionDone {
		// The runner answered but has no verdict on the code — it lost the
		// container, or panicked. That is its failure, not the candidate's,
		// so it takes the same path as never answering at all.
		runErr = fmt.Errorf("runner reported %q: %s", res.Status, tailLine(res.CompileOutput))
	}
	if runErr != nil {
		if attempt < queue.MaxAttempts {
			return fmt.Errorf("runner.execute: submission %s: %w", p.SubmissionID, runErr)
		}
		if s.Logger != nil {
			s.Logger.Error("the runner never returned a verdict; marking the submission error",
				"submission_id", p.SubmissionID, "err", runErr)
		}
		if err := s.finish(ctx, p, SubmissionError, server.Response{
			ID: req.ID, Status: server.StatusError, CompileOutput: runErr.Error(),
		}, nil); err != nil {
			return fmt.Errorf("runner.execute: %w", err)
		}
		return nil
	}
	if err := s.finish(ctx, p, SubmissionDone, res, cases); err != nil {
		return fmt.Errorf("runner.execute: %w", err)
	}
	return nil
}

// claim locks the submission, marks it running, and builds the request for
// it. A submission that has already reached a terminal status yields a zero
// request, which the caller reads as nothing to do.
func (s *ExecutionService) claim(ctx context.Context, p RunnerExecutePayload) (server.Request, []ProblemTestCase, error) {
	var req server.Request
	var cases []ProblemTestCase
	err := s.st.WithTx(ctx, orgScoped(p.OrgID), func(ctx context.Context, tx *store.Tx) error {
		sub, err := tx.Q.GetSubmissionForUpdate(ctx, p.SubmissionID)
		if err != nil {
			return err
		}
		if sub.Status == SubmissionDone || sub.Status == SubmissionError {
			return nil
		}
		problem, err := loadProblem(ctx, tx, sub.ProblemID)
		if err != nil {
			return err
		}
		req = executionRequest(sub.ID, sub.Kind, sub.Language, sub.Source, problem)
		cases = executionCases(sub.Kind, problem)
		return tx.Q.StartSubmission(ctx, sub.ID)
	})
	return req, cases, err
}

// finish records the runner's answer and the submission's weighted score.
func (s *ExecutionService) finish(ctx context.Context, p RunnerExecutePayload, status string, res server.Response, cases []ProblemTestCase) error {
	raw, err := json.Marshal(res)
	if err != nil {
		return fmt.Errorf("submission %s result: %w", p.SubmissionID, err)
	}
	params := db.FinishSubmissionParams{ID: p.SubmissionID, Status: status, Result: raw}
	if status == SubmissionDone {
		passed, total := weightedScore(cases, res)
		if total > 0 {
			params.Score = numeric(passed / total)
		}
	}
	return s.st.WithTx(ctx, orgScoped(p.OrgID), func(ctx context.Context, tx *store.Tx) error {
		return tx.Q.FinishSubmission(ctx, params)
	})
}

// executionRequest builds the I4 request for one submission. It carries the
// code, the cases it is judged on, and the limits — the runner is a separate
// service and learns nothing about the org or the candidate behind the code.
// The id is the submission's, which the runner is idempotent on.
func executionRequest(submissionID uuid.UUID, kind, language, source string, p Problem) server.Request {
	cases := executionCases(kind, p)
	tests := make([]server.Test, 0, len(cases))
	for _, c := range cases {
		tests = append(tests, server.Test{
			ID: c.ID.String(), Input: c.Input, Expected: c.Expected,
			Weight: weightUnits(c.Weight), Unordered: c.Unordered,
		})
	}
	return server.Request{
		ID:        submissionID.String(),
		Language:  language,
		Source:    source,
		SQLSchema: sqlSeedScript(p.AsImport()),
		Signature: signatureFor(p.AsImport()),
		Tests:     tests,
		Limits:    problemLimits(p.Kind, p.TimeLimitMs, p.MemoryLimitKB),
	}
}

// executionCases is the set of cases a submission of this kind is run
// against: a run sees only what the candidate can already see, a submit is
// judged on every case.
func executionCases(kind string, p Problem) []ProblemTestCase {
	if kind != SubmissionRun {
		return p.TestCases
	}
	out := make([]ProblemTestCase, 0, len(p.TestCases))
	for _, c := range p.TestCases {
		if c.Visibility == "public" {
			out = append(out, c)
		}
	}
	return out
}

// weightUnits renders a case's weight for the wire, which carries whole
// numbers. Scoring uses the stored weight; this only tells the runner which
// cases matter more.
func weightUnits(w float64) int {
	n := int(w + 0.5)
	if n < 1 {
		n = 1
	}
	return n
}

// submissionStatusOf maps a runner verdict onto the submission's status. A
// program that would not compile, crashed, or ran out of time is a normal
// answer: the submission is done and simply passed nothing. Only the
// runner's own failure leaves the submission in error.
func submissionStatusOf(runnerStatus string) string {
	switch runnerStatus {
	case server.StatusOK, server.StatusCompileError, server.StatusRuntimeError, server.StatusTimeout:
		return SubmissionDone
	}
	return SubmissionError
}
