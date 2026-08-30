// Package observe carries the structured-logging and Prometheus metrics that
// every process mode registers at its composition root. Metrics are
// registered once, in this package's init, so serve, worker, and runner can
// all import Handler without risking the duplicate-registration panic that
// package-level global registration invites when more than one mode links
// the same binary.
package observe

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// namespace prefixes every metric name so they are unambiguous next to
// whatever else scrapes the same Prometheus.
const namespace = "recruiting"

var (
	// registry is private so every metric in this file is the only way to
	// touch it; nothing outside this package can register a name that
	// collides with another mode's.
	registry = prometheus.NewRegistry()

	// QueueDepth is how many jobs of each kind are waiting to run: queued,
	// scheduled, or being retried. A poller in cmd/recruiting keeps it
	// current by querying river_job directly, since river exposes no
	// counter of its own.
	QueueDepth = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: "queue",
		Name:      "depth",
		Help:      "Number of river_job rows waiting or retrying, by kind.",
	}, []string{"kind"})

	// RunnerDuration observes how long runner.execute jobs take, split by
	// outcome, so latency and failure rate are both visible from one metric.
	RunnerDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Subsystem: "runner",
		Name:      "execute_seconds",
		Help:      "runner.execute job duration in seconds, by outcome.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"outcome"})

	// RunnerFailuresTotal counts runner.execute jobs whose handler returned
	// an error, so the failure rate survives even if nobody computes it from
	// RunnerDuration's outcome label.
	RunnerFailuresTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "runner",
		Name:      "execute_failures_total",
		Help:      "runner.execute jobs whose handler returned an error.",
	})

	// BookingConflictsTotal counts interview-slot bookings refused because
	// the slot was taken by another booking or exception first.
	BookingConflictsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "booking",
		Name:      "conflicts_total",
		Help:      "Interview slot bookings refused because the slot was already taken.",
	})

	// EmailFailuresTotal counts email.send jobs whose handler returned an
	// error (bounced SMTP call, unrenderable template, and so on).
	EmailFailuresTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "email",
		Name:      "send_failures_total",
		Help:      "email.send jobs whose handler returned an error.",
	})
)

func init() {
	registry.MustRegister(
		QueueDepth,
		RunnerDuration,
		RunnerFailuresTotal,
		BookingConflictsTotal,
		EmailFailuresTotal,
	)
}

// Handler serves /metrics in the Prometheus text exposition format. Every
// mode (serve, worker, runner) mounts it; all three read from the one
// registry above, so a metric means the same thing regardless of which
// process reported it.
func Handler() http.Handler {
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
}

// ObserveRunnerExecute records one runner.execute call's duration and
// outcome. Called from worker.go, which has the context and error a queue
// handler produces.
func ObserveRunnerExecute(d time.Duration, err error) {
	outcome := "ok"
	if err != nil {
		outcome = "error"
		RunnerFailuresTotal.Inc()
	}
	RunnerDuration.WithLabelValues(outcome).Observe(d.Seconds())
}

// LogFields never carry resume text, source code, or bearer tokens: they
// exist only to correlate a log line with the request and tenant it
// belongs to.
type LogFields struct {
	RequestID string
	OrgID     string
}

// Attrs turns f into slog attributes ready for a log line.
func (f LogFields) Attrs() []any {
	return []any{"request_id", f.RequestID, "org_id", f.OrgID}
}

// WithFields returns a logger carrying f, so every line it writes for the
// rest of a request's handling names both ids without repeating them at each
// call site.
func WithFields(logger *slog.Logger, f LogFields) *slog.Logger {
	return logger.With(f.Attrs()...)
}
