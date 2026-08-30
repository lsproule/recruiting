package service

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"

	"recruiting/internal/runner/server"
)

func testCases(weights ...float64) []ProblemTestCase {
	cases := make([]ProblemTestCase, len(weights))
	for i, w := range weights {
		vis := "hidden"
		if i == 0 {
			vis = "public"
		}
		cases[i] = ProblemTestCase{ID: uuid.New(), Position: i + 1, Weight: w, Visibility: vis}
	}
	return cases
}

func response(cases []ProblemTestCase, statuses ...string) server.Response {
	res := server.Response{Status: server.StatusOK}
	for i, s := range statuses {
		res.Results = append(res.Results, server.TestResult{TestID: cases[i].ID.String(), Status: s})
	}
	return res
}

func TestWeightedScoreCountsPassedWeightAgainstTheWholeSet(t *testing.T) {
	cases := testCases(1, 3, 6)
	for _, tc := range []struct {
		name     string
		statuses []string
		want     float64
	}{
		{"all passed", []string{server.TestPass, server.TestPass, server.TestPass}, 1},
		{"the heavy case failed", []string{server.TestPass, server.TestPass, server.TestFail}, 0.4},
		{"only the light case passed", []string{server.TestPass, server.TestFail, server.TestTimeout}, 0.1},
		{"nothing ran", nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			passed, total := weightedScore(cases, response(cases, tc.statuses...))
			if total != 10 {
				t.Fatalf("total weight = %v, want 10", total)
			}
			if got := passed / total; math.Abs(got-tc.want) > 1e-9 {
				t.Errorf("score = %v, want %v", got, tc.want)
			}
		})
	}
}

// A result naming a case the problem no longer has must not earn weight.
func TestWeightedScoreIgnoresUnknownTestIDs(t *testing.T) {
	cases := testCases(1, 1)
	res := response(cases, server.TestPass)
	res.Results = append(res.Results, server.TestResult{TestID: uuid.New().String(), Status: server.TestPass})
	passed, total := weightedScore(cases, res)
	if passed != 1 || total != 2 {
		t.Errorf("passed/total = %v/%v, want 1/2", passed, total)
	}
}

func TestAttemptScoreIsTheMeanOfProblemScoresOutOfAHundred(t *testing.T) {
	if got := attemptScore([]ProblemScore{{Score: 1}, {Score: 0.5}, {Score: 0}}); math.Abs(got-50) > 1e-9 {
		t.Errorf("attempt score = %v, want 50", got)
	}
	if got := attemptScore(nil); got != 0 {
		t.Errorf("attempt score with no problems = %v, want 0", got)
	}
}

func TestCompactEventsRoundTripsTheStream(t *testing.T) {
	problem := uuid.New()
	at := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	events := []RecordedEvent{
		{Seq: 1, Kind: "edit", ProblemID: &problem, ServerTs: at, Payload: json.RawMessage(`{"n":1}`)},
		{Seq: 2, Kind: "paste", ServerTs: at.Add(time.Second), Payload: json.RawMessage(`{"len":4}`)},
	}
	var buf bytes.Buffer
	if err := compactEvents(&buf, events); err != nil {
		t.Fatalf("compact: %v", err)
	}
	gz, err := gzip.NewReader(&buf)
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	var got []RecordedEvent
	sc := bufio.NewScanner(gz)
	for sc.Scan() {
		var ev RecordedEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("line %q: %v", sc.Text(), err)
		}
		got = append(got, ev)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Seq != 1 || got[1].Kind != "paste" || got[0].ProblemID == nil || *got[0].ProblemID != problem {
		t.Fatalf("round trip = %+v, want the two events back", got)
	}
	if string(got[0].Payload) != `{"n":1}` {
		t.Errorf("payload = %s, want the original JSON", got[0].Payload)
	}
}

func TestEventsBlobKeyNamesTheAttempt(t *testing.T) {
	id := uuid.New()
	if got, want := eventsBlobKey(id), "attempts/"+id.String()+"/events.jsonl.gz"; got != want {
		t.Errorf("key = %q, want %q", got, want)
	}
}

// A gap between the stored count and the highest accepted seq means events
// never reached the server, so the recording cannot be replayed in full.
func TestRecordingStatusFlagsSequenceGaps(t *testing.T) {
	if got := recordingStatus(10, 10); got != RecordingComplete {
		t.Errorf("no gap = %q, want complete", got)
	}
	if got := recordingStatus(9, 10); got != RecordingIncomplete {
		t.Errorf("one missing event = %q, want incomplete", got)
	}
	if got := recordingStatus(0, 0); got != RecordingComplete {
		t.Errorf("empty recording = %q, want complete", got)
	}
}

// The stored score is what a screen shows, so it is rounded there rather than
// carrying a repeating fraction into every reader.
func TestAttemptScoreIsRoundedToTwoDecimals(t *testing.T) {
	if got := attemptScore([]ProblemScore{{Score: 1.0 / 3}, {Score: 0}}); got != 16.67 {
		t.Errorf("attempt score = %v, want 16.67", got)
	}
}
