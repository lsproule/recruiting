package signals_test

import (
	"testing"
	"time"

	"recruiting/internal/domain/signals"
)

// integrityInput is the fixture stream on its own: the session ran with the
// webcam on a one-minute beat, and nothing was typed, so only the session's
// integrity signals have anything to say.
func integrityInput(t *testing.T) signals.Input {
	t.Helper()
	events := load(t, "integrity.jsonl")
	return signals.Input{
		StartedAt:   events[0].At(),
		Events:      events,
		Problems:    []signals.Problem{{ID: problemID, Difficulty: "easy", Language: "python"}},
		WebcamEvery: time.Minute,
	}
}

// The fixture leaves fullscreen twice, for thirty seconds each time, and
// three of its ten webcam beats never produced a frame.
func TestFullscreenExitsAndSnapshotGaps(t *testing.T) {
	got := byName(signals.Compute(integrityInput(t)))

	s := got["fullscreen_exits"]
	if !near(s.Value, 2.0/5) {
		t.Errorf("fullscreen_exits = %v, want two of the five exits that saturate it", s.Value)
	}
	if len(s.Evidence) != 2 {
		t.Errorf("fullscreen_exits evidence = %+v, want one entry per exit", s.Evidence)
	}
	if s.Confidence != signals.ConfidenceNormal {
		t.Errorf("fullscreen_exits confidence = %q", s.Confidence)
	}

	if s := got["snapshot_gaps"]; !near(s.Value, 0.3) || len(s.Evidence) == 0 {
		t.Errorf("snapshot_gaps = %+v, want 3 of 10 intervals missed with evidence", s)
	}
}

// A session that never asked for the webcam has no beats to miss, so the
// gap signal says nothing rather than reporting every interval as missed.
func TestSnapshotGapsAreZeroWithTheWebcamOff(t *testing.T) {
	in := integrityInput(t)
	in.WebcamEvery = 0
	if s := byName(signals.Compute(in))["snapshot_gaps"]; s.Value != 0 {
		t.Errorf("snapshot_gaps with the webcam off = %v, want 0", s.Value)
	}
}

// A candidate who leaves fullscreen and never comes back is outside for the
// rest of the sitting, not for nothing.
func TestFullscreenExitWithoutAReturnRunsToTheEnd(t *testing.T) {
	in := integrityInput(t)
	var kept []signals.Event
	for _, ev := range in.Events {
		// Drop the returns so the first exit stays open to the last event.
		if ev.Kind == "fullscreen_enter" {
			continue
		}
		kept = append(kept, ev)
	}
	in.Events = kept
	s := byName(signals.Compute(in))["fullscreen_exits"]
	// One exit of five is 0.2 on the count alone; the eight minutes it stays
	// open are past the time the signal saturates at.
	if s.Value != 1 {
		t.Errorf("fullscreen_exits = %v, want the time outside to saturate it", s.Value)
	}
}
