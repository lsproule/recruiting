package observe

import (
	"log/slog"
	"net/http"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"

	"recruiting/internal/web/middleware"
)

// statusWriter records the status code a handler wrote so the request log
// line can carry it; http.ResponseWriter has no getter of its own.
type statusWriter struct {
	http.ResponseWriter
	status int
}

// Unwrap exposes the writer underneath, so http.ResponseController can
// reach its Flusher: a streamed response (an interview room's event stream)
// has to be flushed event by event through this wrapper.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// RequestLogging returns middleware that logs one line per request carrying
// the chi request id and, once the caller is known, the org id — never the
// request body, a resume, source code, or a token, since none of those are
// read here. Mount it after auth.Mount installs its session middleware (see
// cmd/recruiting/serve.go) so the org id middleware.PrincipalFrom reads is
// already on the request context by the time this wrapper reads it back.
func RequestLogging(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)

			fields := LogFields{RequestID: chimw.GetReqID(r.Context())}
			if p, ok := middleware.PrincipalFrom(r.Context()); ok {
				fields.OrgID = p.OrgID.String()
			}
			WithFields(logger, fields).Info("http_request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", sw.status,
				"duration_ms", time.Since(start).Milliseconds(),
			)
		})
	}
}
