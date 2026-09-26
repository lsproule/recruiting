package observe_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"recruiting/internal/observe"
)

// A streamed response (an interview room's event stream) flushes event by
// event through http.ResponseController, which reaches the real writer only
// if every wrapper on the way exposes it. The logging wrapper must.
func TestRequestLoggingWrapperCanBeFlushed(t *testing.T) {
	var flushErr error
	handler := observe.RequestLogging(slog.New(slog.NewTextHandler(io.Discard, nil)))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "event: hello\ndata: {}\n\n")
		flushErr = http.NewResponseController(w).Flush()
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/app/rooms/slot/x/events", nil))
	if flushErr != nil {
		t.Fatalf("flush through the logging wrapper: %v", flushErr)
	}
	if !rec.Flushed {
		t.Fatal("the flush never reached the underlying writer")
	}
}
