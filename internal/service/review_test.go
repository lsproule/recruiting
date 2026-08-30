package service

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestReplayMarkersDescribeWhatTheReviewerJumpsTo(t *testing.T) {
	problem := uuid.New()
	at := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	ev := func(seq int64, kind, payload string) RecordedEvent {
		return RecordedEvent{Seq: seq, Kind: kind, ProblemID: &problem, ServerTs: at, Payload: json.RawMessage(payload)}
	}
	events := []RecordedEvent{
		ev(1, "focus", `{}`),
		ev(2, "edit", `{"changes":[[0,"x"]]}`),
		ev(3, "paste", `{"len":42,"sha256":"a","internal":false}`),
		ev(4, "paste", `{"len":7,"sha256":"b","internal":true}`),
		ev(5, "blur", `{}`),
		ev(6, "run", `{"submission_id":"`+uuid.New().String()+`"}`),
		ev(7, "submit", `{"submission_id":"`+uuid.New().String()+`"}`),
	}
	markers := replayMarkers(events)

	var kinds []string
	for _, m := range markers {
		kinds = append(kinds, m.Kind)
		if m.ProblemID != problem {
			t.Errorf("marker %d names problem %s, want %s", m.Seq, m.ProblemID, problem)
		}
		if m.Note == "" {
			t.Errorf("marker %d at seq %d has no note", len(kinds), m.Seq)
		}
	}
	want := []string{"paste", "paste", "blur", "run", "submit"}
	if len(kinds) != len(want) {
		t.Fatalf("markers = %v, want %v", kinds, want)
	}
	for i, k := range kinds {
		if k != want[i] {
			t.Fatalf("markers = %v, want %v", kinds, want)
		}
	}
	if markers[1].Note == markers[0].Note {
		t.Error("an internal paste reads the same as one from outside the page")
	}
}

func TestValidVerdictAcceptsOnlyTheThree(t *testing.T) {
	for _, v := range Verdicts {
		if !validVerdict(v) {
			t.Errorf("%q is not accepted", v)
		}
	}
	for _, v := range []string{"", "yes", "PASS"} {
		if validVerdict(v) {
			t.Errorf("%q is accepted", v)
		}
	}
}
