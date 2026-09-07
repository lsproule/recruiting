package queue_test

import (
	"context"
	"encoding/json"
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
