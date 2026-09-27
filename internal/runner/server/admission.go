package server

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Admission defaults and bounds. A waiter is bounded by DefaultQueueWait so
// a worker's HTTP call returns well inside its own timeout; MaxQueueWait is
// the ceiling any configuration is clamped to, and the client package asserts
// its timeout stays above it.
const (
	DefaultMaxConcurrent = 2
	// DefaultQueueFactor is how many waiters the queue holds per slot.
	DefaultQueueFactor = 4
	DefaultQueueWait   = 20 * time.Second
	MaxQueueWait       = 4 * time.Minute

	// minRetryAfter and maxRetryAfter bound the Retry-After a 503 carries.
	minRetryAfter = 2 * time.Second
	maxRetryAfter = 60 * time.Second
	// defaultAvgDuration stands in for the average execution time before the
	// process has measured one.
	defaultAvgDuration = 5 * time.Second
)

// SaturatedError is why a request was not admitted: the queue was full or the
// wait for a slot ran out. RetryAfter is when the caller should try again.
type SaturatedError struct {
	RetryAfter time.Duration
	Reason     string
}

func (e *SaturatedError) Error() string {
	return fmt.Sprintf("runner saturated: %s; retry after %s", e.Reason, e.RetryAfter)
}

// admission is the bounded wait queue in front of the sandboxes: capacity
// executions run at once, queueCap more wait up to wait each for a slot, and
// everything beyond that is turned away with a Retry-After the caller can
// act on. It also keeps the process's activity clock for idle shutdown.
type admission struct {
	capacity int
	queueCap int
	wait     time.Duration
	now      func() time.Time
	slots    chan struct{}

	mu       sync.Mutex
	queued   int
	inFlight int
	// avg is an exponentially weighted average of execution durations; it
	// is what a Retry-After is computed from.
	avg      time.Duration
	lastDone time.Time
	started  time.Time
}

func newAdmission(capacity, queueCap int, wait time.Duration) *admission {
	if capacity <= 0 {
		capacity = DefaultMaxConcurrent
	}
	if queueCap < 0 {
		queueCap = 0
	}
	if wait <= 0 {
		wait = DefaultQueueWait
	}
	if wait > MaxQueueWait {
		wait = MaxQueueWait
	}
	a := &admission{capacity: capacity, queueCap: queueCap, wait: wait, now: time.Now, slots: make(chan struct{}, capacity), avg: defaultAvgDuration}
	a.started = a.now()
	return a
}

// acquire waits for a slot. On success the returned release must be called
// exactly once, with the execution's outcome, when the slot is free again.
// It fails with a *SaturatedError when the queue is full or the wait ran
// out, and with ctx.Err() when the caller went away first.
func (a *admission) acquire(ctx context.Context) (release func(status string), err error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	// A free slot is taken without queueing at all.
	select {
	case a.slots <- struct{}{}:
		return a.admitted(), nil
	default:
	}
	a.mu.Lock()
	if a.queued >= a.queueCap {
		retry := a.retryAfterLocked()
		a.mu.Unlock()
		executionsTotal.WithLabelValues(outcomeRejected).Inc()
		return nil, &SaturatedError{RetryAfter: retry, Reason: "queue full"}
	}
	a.queued++
	queueDepth.Set(float64(a.queued))
	a.mu.Unlock()

	enqueued := a.now()
	timer := time.NewTimer(a.wait)
	defer timer.Stop()
	defer func() {
		a.mu.Lock()
		a.queued--
		queueDepth.Set(float64(a.queued))
		a.mu.Unlock()
		queueWaitSeconds.Observe(a.now().Sub(enqueued).Seconds())
	}()
	select {
	case a.slots <- struct{}{}:
		return a.admitted(), nil
	case <-timer.C:
		a.mu.Lock()
		retry := a.retryAfterLocked()
		a.mu.Unlock()
		executionsTotal.WithLabelValues(outcomeRejected).Inc()
		return nil, &SaturatedError{RetryAfter: retry, Reason: "wait for a slot expired"}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// admitted records a taken slot and hands back its release.
func (a *admission) admitted() func(status string) {
	start := a.now()
	a.mu.Lock()
	a.inFlight++
	executionsInFlight.Set(float64(a.inFlight))
	a.mu.Unlock()
	var once sync.Once
	return func(status string) {
		once.Do(func() {
			done := a.now()
			d := done.Sub(start)
			a.mu.Lock()
			a.inFlight--
			executionsInFlight.Set(float64(a.inFlight))
			// Weighted a quarter towards the newest sample: a few slow runs
			// move the estimate, one outlier does not own it.
			a.avg = (a.avg*3 + d) / 4
			a.lastDone = done
			a.mu.Unlock()
			<-a.slots
			executionsTotal.WithLabelValues(status).Inc()
			executionSeconds.Observe(d.Seconds())
			lastExecutionTimestamp.Set(float64(done.Unix()))
		})
	}
}

// retryAfterLocked estimates when a slot will be free for a new arrival:
// the queue ahead of it drains at capacity executions per average
// duration. It is clamped so a cold estimate is neither a spin nor an hour.
func (a *admission) retryAfterLocked() time.Duration {
	depth := a.queued
	if depth < 1 {
		depth = 1
	}
	d := time.Duration(depth) * a.avg / time.Duration(a.capacity)
	if d < minRetryAfter {
		return minRetryAfter
	}
	if d > maxRetryAfter {
		return maxRetryAfter
	}
	return d
}

// snapshot is what /status reports.
type snapshot struct {
	InFlight      int     `json:"in_flight"`
	Queued        int     `json:"queued"`
	Capacity      int     `json:"capacity"`
	QueueCapacity int     `json:"queue_capacity"`
	IdleSeconds   float64 `json:"idle_seconds"`
}

func (a *admission) status() snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	return snapshot{
		InFlight: a.inFlight, Queued: a.queued, Capacity: a.capacity, QueueCapacity: a.queueCap,
		IdleSeconds: a.idleLocked().Seconds(),
	}
}

// idle is how long the runner has had nothing running and nothing waiting:
// zero while busy, otherwise the time since the last execution finished or,
// before any, since the process started.
func (a *admission) idle() time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.idleLocked()
}

func (a *admission) idleLocked() time.Duration {
	if a.inFlight > 0 || a.queued > 0 {
		return 0
	}
	since := a.started
	if a.lastDone.After(since) {
		since = a.lastDone
	}
	return a.now().Sub(since)
}

// isSaturated reports whether err is the queue turning a request away.
func isSaturated(err error) (*SaturatedError, bool) {
	var se *SaturatedError
	if errors.As(err, &se) {
		return se, true
	}
	return nil, false
}
