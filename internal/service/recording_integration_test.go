//go:build integration

package service_test

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"recruiting/internal/service"
)

// recordMany records n events on the fixture's first problem in batches of
// MaxEventBatch: an edit that grows the document, a paste every so often.
func (f *executionFixture) recordMany(t *testing.T, att service.Attempt, n int) {
	t.Helper()
	ctx := context.Background()
	cand := f.candidateOf(att.ID)
	p := f.problems[0]
	hash := strings.Repeat("ab", 32)
	var length int // of the replayed document, so every changeset applies
	for seq := 1; seq <= n; seq += service.MaxEventBatch {
		var batch []service.AttemptEvent
		for i := seq; i <= n && i < seq+service.MaxEventBatch; i++ {
			ev := service.AttemptEvent{Seq: int64(i), T: f.now.UnixMilli() + int64(i)*100, ProblemID: p.ID, Type: "edit",
				Data: json.RawMessage(fmt.Sprintf(`[%d,[0,"x"]]`, length))}
			if i%50 == 0 {
				ev.Type = "paste"
				ev.Data = json.RawMessage(`{"len":1,"sha256":"` + hash + `","internal":false,"pad":"` + strings.Repeat("p", 1500) + `"}`)
			} else {
				length++
			}
			batch = append(batch, ev)
		}
		if _, err := f.attempts.RecordEvents(ctx, cand, att.ID, batch); err != nil {
			t.Fatalf("record batch from %d: %v", seq, err)
		}
	}
}

// A recording longer than one page is compacted from pages walked with the
// seq cursor: the object holds every event exactly once and in order, and
// the replay viewer and the signal computation both read it back whole.
func TestFinalizeCompactsAMultiPageRecording(t *testing.T) {
	const n = 2750 // a few pages of recordingPageSize, the last one partial
	f := newExecutionFixture(t, adderImport(t, "Long Adder"))
	ctx := context.Background()
	att := f.start(t)
	cand := f.candidateOf(att.ID)
	f.recordMany(t, att, n)
	if _, err := f.attempts.Finish(ctx, cand, att.ID); err != nil {
		t.Fatal(err)
	}
	blob := newFakeBlob()
	if err := f.finalize(t, blob, att.ID, 0); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	_, _, _, _, recording, key := f.scored(t, att.ID)
	if recording != service.RecordingComplete || key == nil {
		t.Fatalf("recording = %s, key %v; want complete with an object", recording, key)
	}

	// The same reader the replay viewer uses: gzipped JSON lines in seq order.
	gz, err := gzip.NewReader(bytes.NewReader(blobBody(t, blob, *key)))
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	sc := bufio.NewScanner(gz)
	sc.Buffer(make([]byte, 0, 64<<10), service.MaxEventDataBytes*2)
	var seq int64
	var pastes int
	for sc.Scan() {
		var ev service.RecordedEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("line %d: %v", seq+1, err)
		}
		if ev.Seq != seq+1 || ev.ProblemID == nil || *ev.ProblemID != f.problems[0].ID || ev.ClientTs == nil {
			t.Fatalf("event %d = %+v, want seq %d on the problem with a client time", seq+1, ev, seq+1)
		}
		if ev.Kind == "paste" {
			pastes++
		}
		seq = ev.Seq
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if seq != n || pastes != n/50 {
		t.Fatalf("compacted stream holds %d events with %d pastes, want %d and %d", seq, pastes, n, n/50)
	}

	// The signals fold the same object one event at a time.
	if err := f.computeSignals(t, blob, att.ID); err != nil {
		t.Fatalf("signals.compute: %v", err)
	}
	rows, _ := f.signalRows(t, att.ID)
	r := rows["paste_ratio"]
	if len(r.evidence) != n/50 || r.confidence != "normal" {
		t.Errorf("paste_ratio = %+v, want one piece of evidence per paste at normal confidence", r)
	}
	if last := r.evidence[len(r.evidence)-1]; last.Seq != n {
		t.Errorf("last paste evidence at seq %d, want %d: the stream was not read to the end", last.Seq, n)
	}
}

// Ingest charges every accepted batch against the attempt's ceiling and
// refuses the batch that would pass it, whole, marking the recording
// truncated so the reviewer knows the replay is short. The ceiling is
// enforced on the counters the row keeps, so the test sets those rather
// than recording two hundred thousand events.
func TestEventIngestStopsAtTheRecordingCeiling(t *testing.T) {
	f := newAttemptFixture(t)
	ctx := context.Background()
	att := f.invite(t)
	cand := f.candidate(att.ID)
	if _, err := f.attempts.Start(ctx, cand); err != nil {
		t.Fatal(err)
	}
	ev := func(seq int64, data string) service.AttemptEvent {
		return service.AttemptEvent{Seq: seq, T: f.now.UnixMilli(), ProblemID: f.problem.ID, Type: "blur", Data: json.RawMessage(data)}
	}
	usage := func() (events, bytes int64, truncated bool) {
		t.Helper()
		if err := f.sys.QueryRow(ctx, `select recording_events, recording_bytes, recording_truncated from attempt where id = $1`, att.ID).Scan(&events, &bytes, &truncated); err != nil {
			t.Fatal(err)
		}
		return events, bytes, truncated
	}

	if _, err := f.attempts.RecordEvents(ctx, cand, att.ID, []service.AttemptEvent{ev(1, `{}`), ev(2, `{"a":1}`)}); err != nil {
		t.Fatal(err)
	}
	if events, bytes, truncated := usage(); events != 2 || bytes != 9 || truncated {
		t.Fatalf("usage after two events = %d events, %d bytes, truncated %v; want 2, 9, false", events, bytes, truncated)
	}

	// One event short of the count ceiling: a batch of two is refused, a
	// batch of one still fits.
	if _, err := f.sys.Exec(ctx, `update attempt set recording_events = $2 where id = $1`, att.ID, service.MaxEventsPerAttempt-1); err != nil {
		t.Fatal(err)
	}
	if _, err := f.attempts.RecordEvents(ctx, cand, att.ID, []service.AttemptEvent{ev(3, `{}`), ev(4, `{}`)}); !errors.Is(err, service.ErrRecordingFull) {
		t.Fatalf("batch past the event ceiling = %v, want ErrRecordingFull", err)
	}
	if events, _, truncated := usage(); events != service.MaxEventsPerAttempt-1 || !truncated {
		t.Fatalf("after the refusal: %d events, truncated %v; want the count untouched and the flag set", events, truncated)
	}
	// Once truncated, the recording stays closed even to a batch that fits.
	if _, err := f.attempts.RecordEvents(ctx, cand, att.ID, []service.AttemptEvent{ev(3, `{}`)}); !errors.Is(err, service.ErrRecordingFull) {
		t.Fatalf("batch after truncation = %v, want ErrRecordingFull", err)
	}
	var n int
	if err := f.sys.QueryRow(ctx, `select count(*) from attempt_event where attempt_id = $1`, att.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("%d events stored, want the 2 accepted before the ceiling", n)
	}
	session, err := f.attempts.Session(ctx, cand)
	if err != nil {
		t.Fatal(err)
	}
	if !session.Attempt.RecordingTruncated {
		t.Error("the session's attempt does not report the truncation")
	}

	// The byte ceiling stands on its own: with the count and the flag reset,
	// a batch whose data would pass it is refused and marks the recording.
	if _, err := f.sys.Exec(ctx, `update attempt set recording_truncated = false, recording_events = 2, recording_bytes = $2 where id = $1`,
		att.ID, service.MaxRecordingBytes-10); err != nil {
		t.Fatal(err)
	}
	if _, err := f.attempts.RecordEvents(ctx, cand, att.ID, []service.AttemptEvent{ev(3, `{"pad":"xxxxxx"}`)}); !errors.Is(err, service.ErrRecordingFull) {
		t.Fatalf("batch past the byte ceiling = %v, want ErrRecordingFull", err)
	}
	if _, bytes, truncated := usage(); bytes != service.MaxRecordingBytes-10 || !truncated {
		t.Errorf("after the byte refusal: %d bytes, truncated %v; want the bytes untouched and the flag set", bytes, truncated)
	}
}

// A truncated recording is still finalized and scored; its signals come
// back at low confidence, the way a recording with a gap does.
func TestATruncatedRecordingScoresAtLowConfidence(t *testing.T) {
	f := newExecutionFixture(t, adderImport(t, "Cut Adder"))
	ctx := context.Background()
	att := f.start(t)
	cand := f.candidateOf(att.ID)
	if _, err := f.attempts.RecordEvents(ctx, cand, att.ID, []service.AttemptEvent{
		{Seq: 1, T: f.now.UnixMilli(), ProblemID: f.problems[0].ID, Type: "focus", Data: json.RawMessage(`{}`)},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sys.Exec(ctx, `update attempt set recording_truncated = true where id = $1`, att.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.attempts.Finish(ctx, cand, att.ID); err != nil {
		t.Fatal(err)
	}
	blob := newFakeBlob()
	if err := f.finalize(t, blob, att.ID, 0); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if status, _, _, _, _, _ := f.scored(t, att.ID); status != service.AttemptScored {
		t.Fatalf("status = %s, want scored", status)
	}
	if err := f.computeSignals(t, blob, att.ID); err != nil {
		t.Fatalf("signals.compute: %v", err)
	}
	rows, _ := f.signalRows(t, att.ID)
	for name, r := range rows {
		if r.confidence != "low" {
			t.Errorf("%s at %s confidence, want low for a truncated recording", name, r.confidence)
		}
	}
}
