package signals_test

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"recruiting/internal/domain/signals"
)

// A recording folded one event at a time, as the worker decodes it, must
// score exactly as the same recording handed over whole: every fixture,
// every signal, every piece of evidence.
func TestStreamedInputScoresLikeTheWholeRecording(t *testing.T) {
	for _, name := range []string{"clean.jsonl", "paste_heavy.jsonl", "burst.jsonl", "blur_then_solution.jsonl", "integrity.jsonl"} {
		t.Run(name, func(t *testing.T) {
			whole := input(t, name, "")
			whole.WebcamEvery = 30 * time.Second
			whole.StartedAt = whole.Events[0].At().Add(-time.Minute)

			streamed := whole
			streamed.Events = nil
			streamed.Stream = signals.NewStream(whole.InitialSources)
			for _, ev := range whole.Events {
				streamed.Stream.Add(ev)
			}
			if streamed.Stream.Count() != len(whole.Events) {
				t.Fatalf("stream folded %d events, want %d", streamed.Stream.Count(), len(whole.Events))
			}

			want, got := signals.Compute(whole), signals.Compute(streamed)
			if !reflect.DeepEqual(got, want) {
				wantJSON, _ := json.MarshalIndent(want, "", "  ")
				gotJSON, _ := json.MarshalIndent(got, "", "  ")
				t.Errorf("streamed signals differ\n got %s\nwant %s", gotJSON, wantJSON)
			}
		})
	}
}

// The fold keeps what the rules read, never the payloads: a session-wide
// event with no problem still counts toward the sitting, and an exit from
// fullscreen never returned from runs to the last event.
func TestStreamClosesWhatTheRecordingLeftOpen(t *testing.T) {
	st := signals.NewStream(nil)
	exit := event(1, "fullscreen_exit", `{}`)
	exit.ProblemID = nil
	st.Add(exit)
	st.Add(event(2, "edit", `[0,[0,"x"]]`))
	last := event(3, "blur", `{}`)
	st.Add(last)
	sigs := signals.Compute(signals.Input{Stream: st, StartedAt: exit.At()})
	var fullscreen signals.Signal
	for _, s := range sigs {
		if s.Name == signals.FullscreenExits {
			fullscreen = s
		}
	}
	if len(fullscreen.Evidence) != 1 || fullscreen.Evidence[0].Values["seconds"] != 2 {
		t.Fatalf("fullscreen evidence = %+v, want one exit lasting to the last event (2s)", fullscreen.Evidence)
	}
	tl := st.Timelines()[problemID]
	if tl == nil || tl.Source != "x" || len(tl.Blurs) != 1 || !tl.Blurs[0].To.Equal(last.At()) {
		t.Fatalf("timeline = %+v, want the source replayed and the blur closed at the last event", tl)
	}
	// Nothing lands once the stream has been read.
	st.Add(event(4, "edit", `[1,[0,"y"]]`))
	if st.Count() != 3 || st.Timelines()[problemID].Source != "x" {
		t.Errorf("an event added after the read changed the fold: %d events, source %q", st.Count(), st.Timelines()[problemID].Source)
	}
}
