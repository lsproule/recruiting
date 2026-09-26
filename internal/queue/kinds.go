package queue

import (
	"context"
	"encoding/json"

	"github.com/riverqueue/river"
)

// River routes a job to a worker by the kind of its args type, so each kind
// needs its own Go type. They all carry the payload as opaque JSON: the queue
// does not care what a payload means, only the handler does.

type payload struct {
	Payload json.RawMessage `json:"payload"`
}

func (p payload) payloadJSON() json.RawMessage  { return p.Payload }
func (p *payload) setPayload(b json.RawMessage) { p.Payload = b }

type emailSendArgs struct{ payload }
type interviewRemindArgs struct{ payload }
type assessmentInviteArgs struct{ payload }
type assessmentRemindArgs struct{ payload }
type runnerExecuteArgs struct{ payload }
type attemptFinalizeArgs struct{ payload }
type signalsComputeArgs struct{ payload }
type attemptPurgePreviewArgs struct{ payload }
type snapshotPurgeArgs struct{ payload }
type jobPostPublishArgs struct{ payload }

func (emailSendArgs) Kind() string           { return KindEmailSend }
func (interviewRemindArgs) Kind() string     { return KindInterviewRemind }
func (assessmentInviteArgs) Kind() string    { return KindAssessmentInvite }
func (assessmentRemindArgs) Kind() string    { return KindAssessmentRemind }
func (runnerExecuteArgs) Kind() string       { return KindRunnerExecute }
func (attemptFinalizeArgs) Kind() string     { return KindAttemptFinalize }
func (signalsComputeArgs) Kind() string      { return KindSignalsCompute }
func (jobPostPublishArgs) Kind() string      { return KindJobPostPublish }
func (attemptPurgePreviewArgs) Kind() string { return KindAttemptPurgePreview }
func (snapshotPurgeArgs) Kind() string       { return KindSnapshotPurge }

// kindDef is one row of the kind table: how to build args for an insert and
// how to bind a handler to river's typed worker registry.
type kindDef struct {
	kind      string
	args      func(json.RawMessage) river.JobArgs
	addWorker func(*river.Workers, Handler) error
}

type argsWithPayload interface {
	river.JobArgs
	payloadJSON() json.RawMessage
}

type argsPtr[T any] interface {
	*T
	setPayload(json.RawMessage)
}

func define[T argsWithPayload, PT argsPtr[T]]() kindDef {
	var zero T
	return kindDef{
		kind: zero.Kind(),
		args: func(b json.RawMessage) river.JobArgs {
			var a T
			PT(&a).setPayload(b)
			return a
		},
		addWorker: func(ws *river.Workers, h Handler) error {
			return river.AddWorkerSafely[T](ws, &handlerWorker[T]{handler: h})
		},
	}
}

// registry is the single table of job kinds. Adding a kind means adding its
// args type above and one line here; every worker then has to supply a
// handler for it or refuse to start.
var registry = []kindDef{
	define[emailSendArgs, *emailSendArgs](),
	define[interviewRemindArgs, *interviewRemindArgs](),
	define[assessmentInviteArgs, *assessmentInviteArgs](),
	define[assessmentRemindArgs, *assessmentRemindArgs](),
	define[runnerExecuteArgs, *runnerExecuteArgs](),
	define[attemptFinalizeArgs, *attemptFinalizeArgs](),
	define[signalsComputeArgs, *signalsComputeArgs](),
	define[attemptPurgePreviewArgs, *attemptPurgePreviewArgs](),
	define[snapshotPurgeArgs, *snapshotPurgeArgs](),
	define[jobPostPublishArgs, *jobPostPublishArgs](),
}

var defByKind = func() map[string]kindDef {
	m := make(map[string]kindDef, len(registry))
	for _, def := range registry {
		m[def.kind] = def
	}
	return m
}()

// handlerWorker adapts a Handler to river's typed Worker interface.
type handlerWorker[T argsWithPayload] struct {
	river.WorkerDefaults[T]
	handler Handler
}

func (w *handlerWorker[T]) Work(ctx context.Context, job *river.Job[T]) error {
	return w.handler(ctx, Job{
		ID:      job.ID,
		Kind:    job.Kind,
		Attempt: job.Attempt,
		Payload: job.Args.payloadJSON(),
	})
}
