package queue_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"recruiting/internal/queue"
)

// The set of kinds is a shared interface: handlers, payload shapes, and the
// enqueuing code all key off these strings.
func TestKindsAreTheAgreedSet(t *testing.T) {
	want := []string{
		"assessment.invite",
		"assessment.remind",
		"attempt.finalize",
		"attempt.purge_preview",
		"email.send",
		"interview.remind",
		"jobpost.publish",
		"runner.execute",
		"signals.compute",
		"snapshot.purge",
	}
	got := slices.Clone(queue.Kinds())
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("Kinds() = %v, want %v", got, want)
	}
}

// A worker that cannot run every kind would silently leave jobs stuck, so the
// gap has to surface at startup.
func TestNewWorkerRequiresAHandlerForEveryKind(t *testing.T) {
	handlers := map[string]queue.Handler{}
	for _, k := range queue.Kinds() {
		if k == queue.KindSignalsCompute {
			continue
		}
		handlers[k] = func(context.Context, queue.Job) error { return nil }
	}
	_, err := queue.NewWorker(nil, queue.Config{Handlers: handlers})
	if err == nil {
		t.Fatal("want an error when a kind has no handler")
	}
	if !strings.Contains(err.Error(), queue.KindSignalsCompute) {
		t.Errorf("error %q should name the unhandled kind", err)
	}
}

func TestNewWorkerRejectsUnknownKind(t *testing.T) {
	handlers := map[string]queue.Handler{"nope.invented": func(context.Context, queue.Job) error { return nil }}
	for _, k := range queue.Kinds() {
		handlers[k] = func(context.Context, queue.Job) error { return nil }
	}
	if _, err := queue.NewWorker(nil, queue.Config{Handlers: handlers}); err == nil {
		t.Fatal("want an error for a handler registered under an unknown kind")
	}
}

// Backoff has to grow so a failing dependency is not hammered, and stay
// bounded so a retry is not scheduled past the process's lifetime.
func TestBackoffGrowsAndIsCapped(t *testing.T) {
	base := 2 * time.Second
	prev := time.Duration(0)
	for attempt := 1; attempt <= 5; attempt++ {
		d := queue.Backoff(base, attempt)
		if d <= prev {
			t.Errorf("Backoff(attempt=%d) = %v, want more than the previous %v", attempt, d, prev)
		}
		prev = d
	}
	if d := queue.Backoff(base, 60); d > queue.MaxBackoff {
		t.Errorf("Backoff(attempt=60) = %v, want at most %v", d, queue.MaxBackoff)
	}
}

// The payload travels as opaque JSON, so what a handler receives must be
// exactly what the caller enqueued.
func TestEncodePayloadRoundTrip(t *testing.T) {
	type reminder struct {
		SlotID string `json:"slot_id"`
		Offset string `json:"offset"`
	}
	raw, err := queue.EncodePayload(reminder{SlotID: "abc", Offset: "24h"})
	if err != nil {
		t.Fatal(err)
	}
	var got reminder
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.SlotID != "abc" || got.Offset != "24h" {
		t.Errorf("round trip = %+v", got)
	}
}

// A worked job keeps its payload, and payloads carry reset links and personal
// details. Retention bounds how long the queue holds them.
func TestJobRetentionIsShort(t *testing.T) {
	if queue.CompletedRetention <= 0 || queue.CompletedRetention > 15*time.Minute {
		t.Errorf("CompletedRetention = %v, want a positive period no longer than 15m", queue.CompletedRetention)
	}
	if queue.DiscardedRetention <= 0 || queue.DiscardedRetention > time.Hour {
		t.Errorf("DiscardedRetention = %v, want a positive period no longer than 1h", queue.DiscardedRetention)
	}
	if queue.DiscardedRetention < queue.CompletedRetention {
		t.Error("a discarded job should be inspectable for at least as long as a completed one")
	}
}

// runner.execute has a queue of its own, so a worker pool sized for email
// and reminders cannot pile onto the runner; everything else stays on the
// default queue.
func TestRunnerExecuteRunsOnItsOwnQueue(t *testing.T) {
	if got := queue.QueueOf(queue.KindRunnerExecute); got != queue.QueueRunner {
		t.Errorf("QueueOf(runner.execute) = %q, want %q", got, queue.QueueRunner)
	}
	for _, k := range queue.Kinds() {
		if k == queue.KindRunnerExecute {
			continue
		}
		if got := queue.QueueOf(k); got != "default" {
			t.Errorf("QueueOf(%s) = %q, want the default queue", k, got)
		}
	}
	if queue.QueueOf("nope.invented") != "default" {
		t.Error("an unknown kind should read as the default queue rather than panic")
	}
	if queue.DefaultRunnerWorkers <= 0 {
		t.Error("the runner queue needs at least one worker by default")
	}
}

// A snoozed job is neither done nor failed: the error type carries the
// delay, and any wrapping of it is still recognised.
func TestSnoozeIsRecognisedThroughWrapping(t *testing.T) {
	err := queue.Snooze(30 * time.Second)
	if d, ok := queue.IsSnooze(err); !ok || d != 30*time.Second {
		t.Fatalf("IsSnooze(Snooze(30s)) = %v %v", d, ok)
	}
	wrapped := fmt.Errorf("runner.execute: %w", err)
	if d, ok := queue.IsSnooze(wrapped); !ok || d != 30*time.Second {
		t.Fatalf("a wrapped snooze was not recognised: %v", wrapped)
	}
	var se *queue.SnoozeError
	if !errors.As(wrapped, &se) || se.Delay != 30*time.Second {
		t.Fatalf("errors.As did not reach the SnoozeError in %v", wrapped)
	}
	if d, ok := queue.IsSnooze(queue.Snooze(-time.Second)); !ok || d != 0 {
		t.Errorf("a negative snooze should clamp to zero, got %v %v", d, ok)
	}
	if _, ok := queue.IsSnooze(errors.New("boom")); ok {
		t.Error("an ordinary error is not a snooze")
	}
	if _, ok := queue.IsSnooze(nil); ok {
		t.Error("nil is not a snooze")
	}
}
