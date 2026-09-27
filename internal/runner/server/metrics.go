package server

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// The runner's own metrics. They live in a registry of this package's own
// rather than internal/observe's: the runner is deployed apart from the app,
// on an isolated host, and what an orchestrator scales it on is exactly this
// handful of series. An autoscaler adds capacity on runner_queue_depth and
// takes it away on runner_executions_in_flight staying at zero; see
// docs/runner-scaling.md.
var (
	metricsRegistry = prometheus.NewRegistry()

	executionsInFlight = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "runner_executions_in_flight",
		Help: "Executions currently running in a sandbox.",
	})
	queueDepth = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "runner_queue_depth",
		Help: "Requests admitted to the wait queue and waiting for a sandbox slot.",
	})
	executionsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "runner_executions_total",
		Help: "Executions by outcome: the response status, or rejected when the queue was full or the wait expired.",
	}, []string{"status"})
	executionSeconds = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "runner_execution_seconds",
		Help:    "Wall-clock duration of one execution, from admission to result.",
		Buckets: prometheus.ExponentialBuckets(0.25, 2, 12), // 0.25s .. 512s
	})
	queueWaitSeconds = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "runner_queue_wait_seconds",
		Help:    "Time a request spent waiting for a slot, including rejected waits.",
		Buckets: prometheus.ExponentialBuckets(0.05, 2, 10), // 50ms .. 25.6s
	})
	lastExecutionTimestamp = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "runner_last_execution_timestamp_seconds",
		Help: "Unix time at which the last execution finished; zero when none has since the process started.",
	})
)

func init() {
	metricsRegistry.MustRegister(
		executionsInFlight, queueDepth, executionsTotal, executionSeconds, queueWaitSeconds, lastExecutionTimestamp,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	// Every status label exists from the first scrape, so a rate() over a
	// label that has not happened yet reads zero rather than nothing.
	for _, s := range []string{StatusOK, StatusCompileError, StatusRuntimeError, StatusTimeout, StatusError, outcomeRejected} {
		executionsTotal.WithLabelValues(s)
	}
}

// outcomeRejected is the executions_total status of a request the queue
// turned away with 503.
const outcomeRejected = "rejected"

// MetricsHandler serves the runner's metrics in the Prometheus exposition
// format. The runner mode mounts it on its METRICS_ADDR listener.
func MetricsHandler() http.Handler {
	return promhttp.HandlerFor(metricsRegistry, promhttp.HandlerOpts{})
}
