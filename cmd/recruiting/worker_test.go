package main

import (
	"log/slog"
	"sort"
	"testing"

	"recruiting/internal/queue"
	runnerclient "recruiting/internal/runner/client"
)

// TestHandlersCoverEveryQueueKind proves the worker starts with a handler
// for every kind the queue package knows about — the same check
// queue.NewWorker performs at startup, run here without a database so a
// missing kind fails fast in `go test ./...` rather than only in the
// integration suite.
func TestHandlersCoverEveryQueueKind(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(nil, nil))
	h := handlers(logger, nil, nil, nil, nil, runnerclient.New("http://runner.invalid", "secret"), nil, nil, "http://app.invalid", nil)

	got := make([]string, 0, len(h))
	for kind := range h {
		got = append(got, kind)
	}
	sort.Strings(got)

	want := append([]string(nil), queue.Kinds()...)
	sort.Strings(want)

	if len(got) != len(want) {
		t.Fatalf("handlers() has %d kinds, queue.Kinds() has %d: got %v, want %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("handlers() kinds = %v, want %v", got, want)
		}
	}
}
