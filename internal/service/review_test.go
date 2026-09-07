package service

import (
	"encoding/json"
	"strings"
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

func TestReplayMarkersCarryTheIntegrityTimeline(t *testing.T) {
	problem := uuid.New()
	at := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	ev := func(seq int64, kind, payload string, scoped bool) RecordedEvent {
		e := RecordedEvent{Seq: seq, Kind: kind, ServerTs: at, Payload: json.RawMessage(payload)}
		if scoped {
			e.ProblemID = &problem
		}
		return e
	}
	events := []RecordedEvent{
		ev(1, "blur", `{}`, true),
		ev(2, "paste", `{"len":0,"blocked":true}`, true),
		// Fullscreen and webcam beats belong to the sitting, not to one
		// problem, so they carry no problem id.
		ev(3, "fullscreen_exit", `{}`, false),
		ev(4, "fullscreen_enter", `{}`, false),
		ev(5, "snapshot", `{"seq":7,"ok":true}`, false),
		ev(6, "snapshot", `{"seq":8,"ok":false}`, false),
		ev(7, "edit", `{"changes":[[0,"x"]]}`, true),
	}

	markers := replayMarkers(events)

	var kinds []string
	for _, m := range markers {
		kinds = append(kinds, m.Kind)
		if m.Note == "" {
			t.Errorf("marker at seq %d has no note", m.Seq)
		}
	}
	want := []string{"blur", "paste", "fullscreen_exit", "fullscreen_enter", "snapshot", "snapshot"}
	if len(kinds) != len(want) {
		t.Fatalf("markers = %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("markers = %v, want %v", kinds, want)
		}
	}
	if markers[4].SnapshotSeq != 7 {
		t.Errorf("snapshot marker names beat %d, want 7", markers[4].SnapshotSeq)
	}
	if markers[5].SnapshotSeq != 0 {
		t.Errorf("a failed beat names %d, want no frame to open", markers[5].SnapshotSeq)
	}
	if !strings.Contains(markers[1].Note, "blocked") {
		t.Errorf("blocked paste note = %q", markers[1].Note)
	}
}

func TestSnapshotLinksExpireWithinFiveMinutes(t *testing.T) {
	if SnapshotURLTTL > 5*time.Minute {
		t.Fatalf("SnapshotURLTTL = %s, want at most 5m", SnapshotURLTTL)
	}
}
