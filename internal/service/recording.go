package service

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"

	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

// recordingPageSize is how many attempt_event rows finalization and the
// signal computation hold at once: a recording is walked a page at a time,
// so a long sitting costs a worker one page's events, not the whole stream.
const recordingPageSize = 1000

// recordedEvent is one attempt_event row in the shape of the compacted
// stream.
func recordedEvent(r db.AttemptEvent) RecordedEvent {
	ev := RecordedEvent{Seq: r.Seq, Kind: r.Kind, ServerTs: r.ServerTs.Time.UTC(), Payload: json.RawMessage(r.Payload)}
	if r.ProblemID.Valid {
		id := r.ProblemID.UUID
		ev.ProblemID = &id
	}
	if r.ClientTs.Valid {
		at := r.ClientTs.Time.UTC()
		ev.ClientTs = &at
	}
	return ev
}

func recordedEvents(rows []db.AttemptEvent) []RecordedEvent {
	out := make([]RecordedEvent, 0, len(rows))
	for _, r := range rows {
		out = append(out, recordedEvent(r))
	}
	return out
}

// eachAttemptEvent walks the attempt's event rows in seq order, one page at
// a time, handing each to fn; fn's error stops the walk. It reports how many
// rows it saw, which is the stored count finalization compares to the last
// accepted seq.
func eachAttemptEvent(ctx context.Context, tx *store.Tx, attemptID uuid.UUID, fn func(RecordedEvent) error) (int64, error) {
	var after, seen int64
	for {
		rows, err := tx.Q.ListAttemptEventsAfter(ctx, db.ListAttemptEventsAfterParams{
			AttemptID: attemptID, Seq: after, RowLimit: recordingPageSize,
		})
		if err != nil {
			return seen, err
		}
		for _, r := range rows {
			if err := fn(recordedEvent(r)); err != nil {
				return seen, err
			}
			seen++
			after = r.Seq
		}
		if len(rows) < recordingPageSize {
			return seen, nil
		}
	}
}

// eachCompactedEvent decodes the compacted recording at key one event at a
// time, handing each to fn; fn's error stops the read. The decoder holds one
// event's line, never the stream.
func eachCompactedEvent(ctx context.Context, b BlobReader, key string, fn func(RecordedEvent) error) error {
	rc, err := b.Get(ctx, key)
	if err != nil {
		return err
	}
	defer rc.Close()
	gz, err := gzip.NewReader(rc)
	if err != nil {
		return fmt.Errorf("gunzip %s: %w", key, err)
	}
	defer gz.Close()
	dec := json.NewDecoder(gz)
	for {
		var ev RecordedEvent
		switch err := dec.Decode(&ev); {
		case errors.Is(err, io.EOF):
			return nil
		case err != nil:
			return fmt.Errorf("decode %s: %w", key, err)
		}
		if err := fn(ev); err != nil {
			return err
		}
	}
}

// recordingWriter writes the compacted format: gzipped JSON lines, one
// event per line in the order written, which is what the replay viewer and
// the signal computation read back. Close flushes the gzip trailer; the
// underlying writer is the caller's to close.
type recordingWriter struct {
	gz  *gzip.Writer
	enc *json.Encoder
}

func newRecordingWriter(w io.Writer) *recordingWriter {
	gz := gzip.NewWriter(w)
	return &recordingWriter{gz: gz, enc: json.NewEncoder(gz)}
}

func (w *recordingWriter) write(ev RecordedEvent) error {
	if err := w.enc.Encode(ev); err != nil {
		return fmt.Errorf("compact events: %w", err)
	}
	return nil
}

func (w *recordingWriter) close() error {
	if err := w.gz.Close(); err != nil {
		return fmt.Errorf("compact events: %w", err)
	}
	return nil
}

// compactEvents writes events already in memory as the compacted stream.
func compactEvents(w io.Writer, events []RecordedEvent) error {
	rw := newRecordingWriter(w)
	for _, ev := range events {
		if err := rw.write(ev); err != nil {
			return err
		}
	}
	return rw.close()
}
