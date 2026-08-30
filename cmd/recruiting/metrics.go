package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"recruiting/internal/observe"
)

// defaultMetricsAddr is where the worker and runner modes serve /metrics
// when METRICS_ADDR is unset. serve mode instead answers /metrics on its own
// listener, since it already has one.
const defaultMetricsAddr = ":9090"

// metricsReadHeaderTimeout caps how long a scraper may take to send its
// headers.
const metricsReadHeaderTimeout = 10 * time.Second

// metricsShutdownGrace bounds how long an in-flight scrape has once the
// process is asked to stop.
const metricsShutdownGrace = 5 * time.Second

func metricsAddr() string {
	if v := strings.TrimSpace(os.Getenv("METRICS_ADDR")); v != "" {
		return v
	}
	return defaultMetricsAddr
}

// serveMetrics runs a standalone /metrics listener until ctx is cancelled.
// worker and runner have no HTTP server of their own to hang the endpoint
// off, so each gets this one on its own port; serve mounts observe.Handler
// directly on its existing mux instead.
func serveMetrics(ctx context.Context, logger *slog.Logger, addr string) error {
	mux := http.NewServeMux()
	mux.Handle("/metrics", observe.Handler())
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: metricsReadHeaderTimeout}

	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), metricsShutdownGrace)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return nil
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		logger.Error("metrics listener failed", "addr", addr, "error", err)
		return err
	}
}

// statusRecorder captures the status code a handler wrote, for middleware
// that needs to react to it after the fact.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// bookingConflictMetrics counts POSTs to the booking surface that the
// service refused because the slot was already taken (book.go maps
// service.ErrSlotTaken to 409). It reads only the method and status code, so
// it never sees the request body or anything it carries.
func bookingConflictMetrics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if r.Method == http.MethodPost && rec.status == http.StatusConflict {
			observe.BookingConflictsTotal.Inc()
		}
	})
}
