package service

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"

	"recruiting/internal/domain/signals"
	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

// ReplaySnapshotEvery is how many events the viewer folds before keeping a
// document snapshot. Scrubbing then replays at most that many changesets
// from the nearest snapshot instead of the whole stream.
const ReplaySnapshotEvery = 500

// How many events one page of the stream carries. A sitting records tens of
// thousands, so the viewer pages through them rather than asking for a
// recording whose size nobody bounds.
const (
	ReplayPageDefault = 5000
	ReplayPageMax     = 5000
)

// ReplayQuery is one page of the stream: the events after AfterSeq, at most
// Limit of them. A zero AfterSeq is the first page, which is also the only
// one carrying the editors.
type ReplayQuery struct {
	AfterSeq int64
	Limit    int
}

// page bounds the request, so neither a missing nor an outsized limit asks
// for the whole recording.
func (q ReplayQuery) page() int {
	if q.Limit <= 0 || q.Limit > ReplayPageMax {
		return ReplayPageDefault
	}
	return q.Limit
}

// ReplayProblem is one editor of the replay: the text it started from and
// the text it ended on. Applying the stream's changesets to InitialSource in
// seq order reproduces FinalSource.
type ReplayProblem struct {
	ID            uuid.UUID
	Title         string
	Language      string
	InitialSource string
	FinalSource   string
}

// ReplayMarker is a point on the timeline worth jumping to.
type ReplayMarker struct {
	Seq       int64
	Kind      string
	ProblemID uuid.UUID
	At        time.Time
	Note      string
	// SnapshotSeq is the webcam beat a snapshot marker opens; zero when the
	// marker is not a frame the reviewer can look at.
	SnapshotSeq int
}

// Replay is one page of what the viewer reconstructs a sitting from: the
// editors (first page only), the events in seq order, and their markers.
// NextAfterSeq is the AfterSeq of the page still to come, zero at the end of
// the stream.
type Replay struct {
	AttemptID       uuid.UUID
	Status          string
	RecordingStatus string
	StartedAt       time.Time
	FinishedAt      time.Time
	SnapshotEvery   int
	Problems        []ReplayProblem
	Events          []RecordedEvent
	Markers         []ReplayMarker
	NextAfterSeq    int64
}

// Replay loads one page of the attempt's recording for the viewer. Times are
// the client clock clamped to the server's, the same reading the signals
// take, so a marker and the evidence pointing at it agree. The editors ride
// on the first page only; later pages carry events and markers alone.
func (s *ReviewService) Replay(ctx context.Context, p Principal, attemptID uuid.UUID, q ReplayQuery) (Replay, error) {
	var out Replay
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		att, _, err := s.reviewable(ctx, tx, p, attemptID)
		if err != nil {
			return err
		}
		out, err = s.replay(ctx, tx, att, q, nil)
		return err
	})
	if err != nil {
		return Replay{}, wrapReview("attempt replay", err)
	}
	return out, nil
}

// replay builds one page of the recording for a caller the service has
// already admitted. keep, when set, is the only event kinds the page may
// carry: what it drops is dropped before the markers are derived, so a
// surface that must not see an event cannot see its marker either.
func (s *ReviewService) replay(ctx context.Context, tx *store.Tx, att db.Attempt, q ReplayQuery, keep map[string]bool) (Replay, error) {
	out := Replay{
		AttemptID: att.ID, Status: att.Status, RecordingStatus: att.RecordingStatus,
		StartedAt: att.StartedAt.Time.UTC(), FinishedAt: att.FinishedAt.Time.UTC(),
		SnapshotEvery: ReplaySnapshotEvery,
	}
	limit := q.page()
	// One event beyond the page tells the viewer there is more to come
	// without a second count over the stream.
	events, err := s.recording(ctx, tx, att, q.AfterSeq, limit+1)
	if err != nil {
		return Replay{}, err
	}
	if len(events) > limit {
		events = events[:limit]
		out.NextAfterSeq = events[len(events)-1].Seq
	}
	if keep != nil {
		kept := events[:0]
		for _, ev := range events {
			if keep[ev.Kind] {
				kept = append(kept, ev)
			}
		}
		events = kept
	}
	out.Events = events
	out.Markers = replayMarkers(events)
	if q.AfterSeq > 0 {
		return out, nil
	}
	sources, err := tx.Q.ListAttemptSources(ctx, att.ID)
	if err != nil {
		return Replay{}, err
	}
	subs, err := tx.Q.ListSubmissions(ctx, att.ID)
	if err != nil {
		return Replay{}, err
	}
	a, err := loadAssessment(ctx, tx, att.AssessmentID)
	if err != nil {
		return Replay{}, err
	}
	edited, err := editedProblems(ctx, tx, att.ID)
	if err != nil {
		return Replay{}, err
	}
	for _, problem := range a.Problems {
		language, final := finalSource(problem.ID, subs, sources)
		out.Problems = append(out.Problems, ReplayProblem{
			ID: problem.ID, Title: problem.Title, Language: language,
			InitialSource: startingSource(problem.ID, edited, sources), FinalSource: final,
		})
	}
	return out, nil
}

// editedProblems is the set of problems the recording holds an edit for.
// Paging means no single call sees the whole stream, so the rows answer it.
func editedProblems(ctx context.Context, tx *store.Tx, attemptID uuid.UUID) (map[uuid.UUID]bool, error) {
	rows, err := tx.Q.ListEditedProblems(ctx, attemptID)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]bool, len(rows))
	for _, r := range rows {
		if r.Valid {
			out[r.UUID] = true
		}
	}
	return out, nil
}

// startingSource is the text a problem's editor began with. Nothing records
// the text before the first event, so the synced source stands in only for a
// problem the stream holds no edit for: it was there before recording began
// and nothing has changed it since.
func startingSource(problemID uuid.UUID, edited map[uuid.UUID]bool, sources []db.AttemptSource) string {
	if edited[problemID] {
		return ""
	}
	for _, src := range sources {
		if src.ProblemID == problemID {
			return src.Source
		}
	}
	return ""
}

// recording reads at most limit events after afterSeq: from the compacted
// object finalization wrote, or the attempt_event rows otherwise. The rows
// outlive the compaction, so an object that cannot be read is not fatal.
func (s *ReviewService) recording(ctx context.Context, tx *store.Tx, att db.Attempt, afterSeq int64, limit int) ([]RecordedEvent, error) {
	if s.blob != nil && att.RecordingBlobKey != nil {
		events, err := readCompactedEvents(ctx, s.blob, *att.RecordingBlobKey, afterSeq, limit)
		if err == nil {
			return events, nil
		}
		if s.Logger != nil {
			s.Logger.Warn("replaying from the database instead of object storage", "attempt_id", att.ID, "error", err)
		}
	}
	rows, err := tx.Q.ListAttemptEventsAfter(ctx, db.ListAttemptEventsAfterParams{
		AttemptID: att.ID, Seq: afterSeq, RowLimit: int32(limit),
	})
	if err != nil {
		return nil, err
	}
	return recordedEvents(rows), nil
}

// readCompactedEvents reads the gzipped JSON lines back, one event per line
// in seq order, keeping at most limit of those past afterSeq. It stops
// scanning once the page is full, so a long recording is never all in memory.
func readCompactedEvents(ctx context.Context, b BlobReader, key string, afterSeq int64, limit int) ([]RecordedEvent, error) {
	rc, err := b.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	gz, err := gzip.NewReader(rc)
	if err != nil {
		return nil, fmt.Errorf("gunzip %s: %w", key, err)
	}
	defer gz.Close()
	var out []RecordedEvent
	sc := bufio.NewScanner(gz)
	sc.Buffer(make([]byte, 0, 64<<10), MaxEventDataBytes*2)
	for sc.Scan() && len(out) < limit {
		var ev RecordedEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			return nil, fmt.Errorf("decode %s: %w", key, err)
		}
		if ev.Seq <= afterSeq {
			continue
		}
		out = append(out, ev)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", key, err)
	}
	return out, nil
}

// RecordedEventAt is when an event happened for the viewer: the client clock
// clamped to the server's, the same reading the signals take, so a marker and
// the evidence pointing at it agree.
func RecordedEventAt(ev RecordedEvent) time.Time { return signals.Event(ev).At() }

// replayMarkers picks the points a reviewer scrubs to: the integrity
// timeline (pasted text, lost focus, fullscreen, webcam beats) and every run
// and submit. The timeline is derived here rather than in the viewer, so the
// island stays a renderer of what the server already decided.
func replayMarkers(events []RecordedEvent) []ReplayMarker {
	var out []ReplayMarker
	for _, ev := range events {
		marker, ok := replayMarker(ev)
		if !ok {
			continue
		}
		marker.Seq, marker.Kind, marker.At = ev.Seq, ev.Kind, signals.Event(ev).At()
		if ev.ProblemID != nil {
			marker.ProblemID = *ev.ProblemID
		}
		out = append(out, marker)
	}
	return out
}

// replayMarker is the marker one event earns, if any. Fullscreen and webcam
// beats belong to the sitting rather than to one editor, so they carry no
// problem of their own.
func replayMarker(ev RecordedEvent) (ReplayMarker, bool) {
	switch ev.Kind {
	case "paste":
		var d struct {
			Len      int  `json:"len"`
			Internal bool `json:"internal"`
			Blocked  bool `json:"blocked"`
		}
		_ = json.Unmarshal(ev.Payload, &d)
		if d.Blocked {
			return ReplayMarker{Note: "a paste was blocked"}, true
		}
		note := "pasted " + strconv.Itoa(max(d.Len, 0)) + " characters"
		if d.Internal {
			note += " copied from this page"
		}
		return ReplayMarker{Note: note}, true
	case "blur":
		return ReplayMarker{Note: "left the page"}, true
	case "fullscreen_exit":
		return ReplayMarker{Note: "left fullscreen"}, true
	case "fullscreen_enter":
		return ReplayMarker{Note: "returned to fullscreen"}, true
	case "snapshot":
		var d struct {
			Seq int  `json:"seq"`
			OK  bool `json:"ok"`
		}
		_ = json.Unmarshal(ev.Payload, &d)
		if !d.OK {
			return ReplayMarker{Note: "the webcam beat took no frame"}, true
		}
		// The beat, not the event seq, names the stored frame.
		return ReplayMarker{Note: "webcam frame", SnapshotSeq: d.Seq}, true
	case "run":
		return ReplayMarker{Note: "ran the code"}, true
	case "submit":
		return ReplayMarker{Note: "submitted"}, true
	}
	return ReplayMarker{}, false
}
