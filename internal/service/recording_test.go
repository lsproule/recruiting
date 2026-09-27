package service

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// memBlob serves one object for the readers under test.
type memBlob struct{ objects map[string][]byte }

func (b memBlob) Get(_ context.Context, key string) (io.ReadCloser, error) {
	body, ok := b.objects[key]
	if !ok {
		return nil, errors.New("blob: no such key")
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

// streamOf compacts n events, every kind the readers care about among them,
// with payloads big enough that a line is not a trivial decode.
func streamOf(t *testing.T, n int) ([]RecordedEvent, []byte) {
	t.Helper()
	problem := uuid.New()
	at := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	events := make([]RecordedEvent, 0, n)
	var buf bytes.Buffer
	rw := newRecordingWriter(&buf)
	for i := 1; i <= n; i++ {
		client := at.Add(time.Duration(i) * time.Second)
		ev := RecordedEvent{Seq: int64(i), Kind: "edit", ProblemID: &problem, ClientTs: &client, ServerTs: client.Add(time.Millisecond)}
		switch i % 3 {
		case 0:
			ev.Kind, ev.ProblemID = "paste", nil
			ev.Payload = json.RawMessage(`{"len":` + fmt.Sprint(i) + `,"internal":false,"pad":"` + strings.Repeat("p", 2000) + `"}`)
		default:
			ev.Payload = json.RawMessage(`[` + fmt.Sprint(i) + `,[0,"` + strings.Repeat("x", 3000) + `"]]`)
		}
		if err := rw.write(ev); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		events = append(events, ev)
	}
	if err := rw.close(); err != nil {
		t.Fatal(err)
	}
	return events, buf.Bytes()
}

// The streaming decoder and the replay viewer's paging reader read the same
// bytes the writer produced, event for event, so the compacted format is one
// format however it is written or read.
func TestRecordingWriterAndReadersAgree(t *testing.T) {
	const n = 2500
	want, body := streamOf(t, n)
	blob := memBlob{objects: map[string][]byte{"k": body}}
	ctx := context.Background()

	var streamed []RecordedEvent
	if err := eachCompactedEvent(ctx, blob, "k", func(ev RecordedEvent) error {
		streamed = append(streamed, ev)
		return nil
	}); err != nil {
		t.Fatalf("stream: %v", err)
	}
	if len(streamed) != n {
		t.Fatalf("streamed %d events, want %d", len(streamed), n)
	}
	for i, ev := range streamed {
		if ev.Seq != want[i].Seq || ev.Kind != want[i].Kind || string(ev.Payload) != string(want[i].Payload) ||
			(ev.ProblemID == nil) != (want[i].ProblemID == nil) || !ev.ServerTs.Equal(want[i].ServerTs) {
			t.Fatalf("event %d = %+v, want %+v", i, ev, want[i])
		}
	}

	// The replay reader pages through the same object.
	var paged []RecordedEvent
	for after := int64(0); ; {
		page, err := readCompactedEvents(ctx, blob, "k", after, 700)
		if err != nil {
			t.Fatalf("page after %d: %v", after, err)
		}
		paged = append(paged, page...)
		if len(page) < 700 {
			break
		}
		after = page[len(page)-1].Seq
	}
	if len(paged) != n || paged[n-1].Seq != n {
		t.Fatalf("paged reader saw %d events ending at %d, want %d", len(paged), paged[len(paged)-1].Seq, n)
	}

	// And the line format is unchanged: one JSON object per line, in order.
	lines := strings.Split(strings.TrimSuffix(gunzip(t, body), "\n"), "\n")
	if len(lines) != n || !strings.HasPrefix(lines[0], `{"seq":1,"kind":"edit"`) {
		t.Fatalf("%d lines, first %q; want %d JSON lines in seq order", len(lines), lines[0], n)
	}
	var buf bytes.Buffer
	if err := compactEvents(&buf, want); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes(), body) {
		t.Error("the streamed writer and the whole-slice writer produced different bytes")
	}
}

// A read that fails mid-stream reports the failure rather than a shorter
// recording, so the caller can fall back to the rows.
func TestStreamingDecoderReportsACutStream(t *testing.T) {
	_, body := streamOf(t, 30)
	blob := memBlob{objects: map[string][]byte{"cut": body[:len(body)/2]}}
	var seen int
	err := eachCompactedEvent(context.Background(), blob, "cut", func(RecordedEvent) error {
		seen++
		return nil
	})
	if err == nil {
		t.Fatalf("a cut stream read %d events without error", seen)
	}
	stop := errors.New("enough")
	err = eachCompactedEvent(context.Background(), memBlob{objects: map[string][]byte{"k": body}}, "k", func(RecordedEvent) error { return stop })
	if !errors.Is(err, stop) {
		t.Errorf("fn's error = %v, want it returned as is", err)
	}
}

func gunzip(t *testing.T, body []byte) string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	out, err := io.ReadAll(gz)
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	return string(out)
}
