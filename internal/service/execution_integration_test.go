//go:build integration

package service_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"recruiting/internal/domain"
	"recruiting/internal/queue"
	runnerclient "recruiting/internal/runner/client"
	"recruiting/internal/runner/server"
	"recruiting/internal/service"
)

// executionFixture is an invited attempt over the problems it is built with,
// plus the handlers that run and score its submissions.
type executionFixture struct {
	*pipelineFixture
	q          *queue.Client
	attempts   *service.AttemptService
	assessment service.Assessment
	problems   []service.Problem
	now        time.Time
}

func newExecutionFixture(t *testing.T, imports ...domain.ImportProblem) *executionFixture {
	t.Helper()
	pf := newPipelineFixture(t)
	ctx := context.Background()
	rec := pf.principal(service.RoleRecruiter)

	problems := service.NewProblemService(pf.st, passExecutor{})
	f := &executionFixture{pipelineFixture: pf, now: time.Now().UTC().Truncate(time.Second)}
	ids := make([]uuid.UUID, 0, len(imports))
	for _, imp := range imports {
		p, err := problems.Create(ctx, rec, imp)
		if err != nil {
			t.Fatalf("create problem %s: %v", imp.Title, err)
		}
		f.problems = append(f.problems, p)
		ids = append(ids, p.ID)
	}
	assessments := service.NewAssessmentService(pf.st)
	a, err := assessments.Create(ctx, rec, service.AssessmentInput{
		Name: "Screen " + pf.orgID.String(), DurationMinutes: 60, InviteWindowDays: 5, ProblemIDs: ids,
	})
	if err != nil {
		t.Fatalf("create assessment: %v", err)
	}
	if err := assessments.AttachToStage(ctx, rec, pf.jobID, pf.stages[domain.StageAssessment], a.ID); err != nil {
		t.Fatalf("attach: %v", err)
	}
	q, err := queue.New(pf.st.Pool(), queue.Config{})
	if err != nil {
		t.Fatal(err)
	}
	f.q, f.assessment = q, a
	f.attempts = service.NewAttemptService(pf.st, q, "https://example.test/")
	f.attempts.Now = func() time.Time { return f.now }
	return f
}

// start invites the candidate and starts the timer, returning the attempt.
func (f *executionFixture) start(t *testing.T) service.Attempt {
	t.Helper()
	ctx := context.Background()
	h := service.AssessmentInviteHandler(f.st, f.q, "https://example.test/")
	payload, _ := json.Marshal(service.AssessmentInvitePayload{ApplicationID: f.appID, StageID: f.stages[domain.StageAssessment], OrgID: f.orgID})
	if err := h(ctx, queue.Job{Kind: queue.KindAssessmentInvite, Payload: payload}); err != nil {
		t.Fatalf("invite: %v", err)
	}
	var id uuid.UUID
	if err := f.sys.QueryRow(ctx, `select id from attempt where application_id = $1 and stage_id = $2`, f.appID, f.stages[domain.StageAssessment]).Scan(&id); err != nil {
		t.Fatalf("attempt row: %v", err)
	}
	s, err := f.attempts.Start(ctx, f.candidateOf(id))
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	return s.Attempt
}

func (f *executionFixture) candidateOf(attemptID uuid.UUID) service.Principal {
	return service.Principal{Kind: service.PrincipalMagicLink, OrgID: f.orgID, MagicPurpose: service.LinkAssessment, SubjectID: attemptID}
}

// execute works one runner.execute job for the submission, as delivery
// number attempt.
func (f *executionFixture) execute(t *testing.T, exec service.Executor, submissionID uuid.UUID, attempt int) error {
	t.Helper()
	payload, _ := json.Marshal(service.RunnerExecutePayload{SubmissionID: submissionID, OrgID: f.orgID})
	h := service.RunnerExecuteHandler(f.st, exec, nil)
	return h(context.Background(), queue.Job{Kind: queue.KindRunnerExecute, Attempt: attempt, Payload: payload})
}

// submissionRow reads what the handler stored.
func (f *executionFixture) submissionRow(t *testing.T, id uuid.UUID) (status string, score *float64, result []byte) {
	t.Helper()
	if err := f.sys.QueryRow(context.Background(),
		`select status, score, result from submission where id = $1`, id).Scan(&status, &score, &result); err != nil {
		t.Fatalf("submission row: %v", err)
	}
	return status, score, result
}

// deadExecutor stands in for a runner that cannot be reached.
type deadExecutor struct{ calls int }

func (e *deadExecutor) Execute(context.Context, server.Request) (server.Response, error) {
	e.calls++
	return server.Response{}, errors.New("dial tcp: connection refused")
}

// recordingExecutor answers everything as passing and keeps the requests.
type recordingExecutor struct{ requests []server.Request }

func (e *recordingExecutor) Execute(ctx context.Context, req server.Request) (server.Response, error) {
	e.requests = append(e.requests, req)
	return passExecutor{}.Execute(ctx, req)
}

func adderImport(t *testing.T, title string) domain.ImportProblem {
	t.Helper()
	parsed, err := domain.ParseProblemImport([]byte("[" + codeProblemJSON(title, "print(3)") + "]"))
	if err != nil {
		t.Fatal(err)
	}
	return parsed[0]
}

func TestRunnerExecuteStoresPerTestResultsAndScoresTheSubmission(t *testing.T) {
	f := newExecutionFixture(t, adderImport(t, "Execute Adder"))
	ctx := context.Background()
	att := f.start(t)
	cand := f.candidateOf(att.ID)
	problem := f.problems[0]

	run, err := f.attempts.Run(ctx, cand, att.ID, problem.ID, "python", "print(3)")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	exec := &recordingExecutor{}
	if err := f.execute(t, exec, run.ID, 1); err != nil {
		t.Fatalf("execute run: %v", err)
	}
	if len(exec.requests) != 1 {
		t.Fatalf("%d runner calls, want 1", len(exec.requests))
	}
	req := exec.requests[0]
	if req.ID != run.ID.String() {
		t.Errorf("request id = %q, want the submission id", req.ID)
	}
	if len(req.Tests) != 1 {
		t.Errorf("a run sent %d cases, want only the public one", len(req.Tests))
	}
	status, score, result := f.submissionRow(t, run.ID)
	if status != service.SubmissionDone || score == nil || *score != 1 {
		t.Fatalf("run submission = %s score %v, want done at 1", status, score)
	}
	var stored server.Response
	if err := json.Unmarshal(result, &stored); err != nil || len(stored.Results) != 1 {
		t.Fatalf("stored result = %s, %v; want the per-case results", result, err)
	}

	sub, err := f.attempts.Submit(ctx, cand, att.ID, problem.ID, "python", "print(3)")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if err := f.execute(t, exec, sub.ID, 1); err != nil {
		t.Fatalf("execute submit: %v", err)
	}
	if got := len(exec.requests[1].Tests); got != 2 {
		t.Errorf("a submit sent %d cases, want every case", got)
	}
	if _, score, _ := f.submissionRow(t, sub.ID); score == nil || *score != 1 {
		t.Errorf("submit score = %v, want 1", score)
	}
}

// A redelivered job must not run the candidate's code a second time.
func TestRunnerExecuteLeavesAFinishedSubmissionAlone(t *testing.T) {
	f := newExecutionFixture(t, adderImport(t, "Idempotent Adder"))
	att := f.start(t)
	sub, err := f.attempts.Submit(context.Background(), f.candidateOf(att.ID), att.ID, f.problems[0].ID, "python", "print(3)")
	if err != nil {
		t.Fatal(err)
	}
	exec := &recordingExecutor{}
	for i := 0; i < 2; i++ {
		if err := f.execute(t, exec, sub.ID, i+1); err != nil {
			t.Fatalf("execute %d: %v", i, err)
		}
	}
	if len(exec.requests) != 1 {
		t.Errorf("%d runner calls over two deliveries, want 1", len(exec.requests))
	}
}

// An unreachable runner is retried through the queue; once the deliveries run
// out the submission is left in error, which the candidate may resubmit past
// and which finalization counts for the vetter.
func TestRunnerDownLeavesTheSubmissionResubmittable(t *testing.T) {
	f := newExecutionFixture(t, adderImport(t, "Unreachable Adder"))
	ctx := context.Background()
	att := f.start(t)
	cand := f.candidateOf(att.ID)
	sub, err := f.attempts.Submit(ctx, cand, att.ID, f.problems[0].ID, "python", "print(3)")
	if err != nil {
		t.Fatal(err)
	}
	dead := &deadExecutor{}
	if err := f.execute(t, dead, sub.ID, 1); err == nil {
		t.Fatal("an early delivery against a dead runner must fail so the queue retries")
	}
	if status, _, _ := f.submissionRow(t, sub.ID); status == service.SubmissionError {
		t.Fatal("the submission must not be given up on while deliveries remain")
	}
	if err := f.execute(t, dead, sub.ID, queue.MaxAttempts); err != nil {
		t.Fatalf("the last delivery must not fail the job: %v", err)
	}
	status, score, _ := f.submissionRow(t, sub.ID)
	if status != service.SubmissionError || score != nil {
		t.Fatalf("submission = %s score %v, want error with no score", status, score)
	}

	// The attempt is untouched, so the candidate can submit again.
	again, err := f.attempts.Submit(ctx, cand, att.ID, f.problems[0].ID, "python", "print(4)")
	if err != nil {
		t.Fatalf("resubmit after a runner failure: %v", err)
	}
	if err := f.execute(t, &recordingExecutor{}, again.ID, 1); err != nil {
		t.Fatalf("execute the resubmission: %v", err)
	}
	if status, _, _ := f.submissionRow(t, again.ID); status != service.SubmissionDone {
		t.Errorf("resubmission = %s, want done", status)
	}
}

// The runner's verdict on the code — it would not compile, it crashed, it ran
// out of time — is an answer, not a failure: the submission is done and
// simply scores nothing.
func TestRunnerVerdictsAreScoredNotErrored(t *testing.T) {
	f := newExecutionFixture(t, adderImport(t, "Compile Error Adder"))
	att := f.start(t)
	sub, err := f.attempts.Submit(context.Background(), f.candidateOf(att.ID), att.ID, f.problems[0].ID, "python", "print(")
	if err != nil {
		t.Fatal(err)
	}
	compileErr := executorFn(func(_ context.Context, req server.Request) (server.Response, error) {
		return server.Response{ID: req.ID, Status: server.StatusCompileError, CompileOutput: "SyntaxError"}, nil
	})
	if err := f.execute(t, compileErr, sub.ID, 1); err != nil {
		t.Fatalf("execute: %v", err)
	}
	status, score, _ := f.submissionRow(t, sub.ID)
	if status != service.SubmissionDone || score == nil || *score != 0 {
		t.Fatalf("compile error = %s score %v, want done at 0", status, score)
	}
}

type executorFn func(context.Context, server.Request) (server.Response, error)

func (f executorFn) Execute(ctx context.Context, req server.Request) (server.Response, error) {
	return f(ctx, req)
}

// doubleImport is a Python problem whose harness reads one number from stdin.
func doubleImport(t *testing.T) domain.ImportProblem {
	t.Helper()
	return parseOne(t, `{"kind":"code","title":"Double It","statement":"Double the number.","difficulty":"easy",
		"tags":["math"],"allowed_languages":["python"],"time_limit_ms":5000,
		"reference_solutions":[{"language":"python","source":"import sys\nprint(2 * int(sys.stdin.read().strip()))\n"}],
		"test_cases":[{"input":"2\n","expected":"4","visibility":"public","weight":1},
			{"input":"5\n","expected":"10","visibility":"hidden","weight":3}]}`)
}

// sumImport is a SQL problem seeded with three rows.
func sumImport(t *testing.T) domain.ImportProblem {
	t.Helper()
	return parseOne(t, `{"kind":"sql","title":"Sum The Rows","statement":"Total the column.","difficulty":"easy",
		"tags":["sql"],"allowed_languages":["sql"],"time_limit_ms":5000,
		"sql_schema":"create table t (n int not null);","sql_seed":"insert into t values (1), (2), (3);",
		"reference_solutions":[{"language":"sql","source":"select sum(n) from t"}],
		"test_cases":[{"input":"","expected":"6","visibility":"public","weight":1},
			{"input":"insert into t values (4);","expected":"10","visibility":"hidden","weight":1}]}`)
}

func parseOne(t *testing.T, body string) domain.ImportProblem {
	t.Helper()
	parsed, err := domain.ParseProblemImport([]byte("[" + body + "]"))
	if err != nil {
		t.Fatal(err)
	}
	return parsed[0]
}

// TestAttemptOverPythonAndSQLScoresOnTheRealRunner is the acceptance path:
// a candidate submits both problems, the queue handlers run them in the
// sandbox, and finalization scores the attempt out of a hundred.
func TestAttemptOverPythonAndSQLScoresOnTheRealRunner(t *testing.T) {
	f := newExecutionFixture(t, doubleImport(t), sumImport(t))
	exec := runnerclient.New(startRunner(t), runnerTestSecret)
	ctx := context.Background()
	att := f.start(t)
	cand := f.candidateOf(att.ID)

	for _, tc := range []struct{ problem, language, source string }{
		{"Double It", "python", "import sys\nprint(2 * int(sys.stdin.read().strip()))\n"},
		{"Sum The Rows", "sql", "select sum(n) from t"},
	} {
		var problem service.Problem
		for _, p := range f.problems {
			if p.Title == tc.problem {
				problem = p
			}
		}
		sub, err := f.attempts.Submit(ctx, cand, att.ID, problem.ID, tc.language, tc.source)
		if err != nil {
			t.Fatalf("submit %s: %v", tc.problem, err)
		}
		if err := f.execute(t, exec, sub.ID, 1); err != nil {
			t.Fatalf("execute %s: %v", tc.problem, err)
		}
		status, score, result := f.submissionRow(t, sub.ID)
		if status != service.SubmissionDone || score == nil || *score != 1 {
			t.Fatalf("%s = %s score %v; runner said %s", tc.problem, status, score, result)
		}
	}

	if _, err := f.attempts.Finish(ctx, cand, att.ID); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if err := f.finalize(t, newFakeBlob(), att.ID, 0); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	status, score, _, errCount, _, _ := f.scored(t, att.ID)
	if status != service.AttemptScored || score == nil || *score != 100 {
		t.Fatalf("attempt = %s score %v, want scored at 100", status, score)
	}
	if errCount != 0 {
		t.Errorf("error count = %d, want 0", errCount)
	}
}

// A runner that answers but has no verdict — it lost the container, it
// panicked — has failed just as surely as one that never answered, so it is
// retried and only then given up on.
func TestRunnerInBandErrorIsRetriedThenErrored(t *testing.T) {
	f := newExecutionFixture(t, adderImport(t, "In-band Error Adder"))
	att := f.start(t)
	sub, err := f.attempts.Submit(context.Background(), f.candidateOf(att.ID), att.ID, f.problems[0].ID, "python", "print(3)")
	if err != nil {
		t.Fatal(err)
	}
	broken := executorFn(func(_ context.Context, req server.Request) (server.Response, error) {
		return server.Response{ID: req.ID, Status: server.StatusError, CompileOutput: "client disconnected"}, nil
	})
	if err := f.execute(t, broken, sub.ID, 1); err == nil {
		t.Fatal("an early delivery must fail so the queue retries")
	}
	if status, _, _ := f.submissionRow(t, sub.ID); status == service.SubmissionError {
		t.Fatal("the submission must not be given up on while deliveries remain")
	}
	if err := f.execute(t, broken, sub.ID, queue.MaxAttempts); err != nil {
		t.Fatalf("the last delivery must not fail the job: %v", err)
	}
	status, score, result := f.submissionRow(t, sub.ID)
	if status != service.SubmissionError || score != nil {
		t.Fatalf("submission = %s score %v, want error with no score", status, score)
	}
	if !bytes.Contains(result, []byte("client disconnected")) {
		t.Errorf("stored result = %s, want the runner's reason kept", result)
	}
}
