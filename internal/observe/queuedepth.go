package observe

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"recruiting/internal/queue"
)

// queueDepthStates are the river_job states that count as "not yet done":
// available and scheduled jobs are waiting for their turn, retryable jobs
// failed once and are waiting to run again. Running, completed, cancelled,
// and discarded jobs are not depth — they are not waiting on anything.
var queueDepthStates = []string{"available", "scheduled", "retryable"}

// PollQueueDepth keeps observe.QueueDepth current by querying river_job every
// interval until ctx is cancelled. river_job carries no tenant data — it is
// the one table RLS exempts — so this reads it directly rather than through
// internal/store's tenant-scoped transactions.
func PollQueueDepth(ctx context.Context, pool *pgxpool.Pool, interval time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	pollQueueDepthOnce(ctx, pool, logger)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pollQueueDepthOnce(ctx, pool, logger)
		}
	}
}

func pollQueueDepthOnce(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) {
	counts := make(map[string]float64, len(queue.Kinds()))
	for _, kind := range queue.Kinds() {
		counts[kind] = 0
	}
	rows, err := pool.Query(ctx,
		`select kind, count(*) from river_job where state = any($1) group by kind`,
		queueDepthStates)
	if err != nil {
		if logger != nil {
			logger.Warn("queue depth poll failed", "error", err)
		}
		return
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var n int64
		if err := rows.Scan(&kind, &n); err != nil {
			if logger != nil {
				logger.Warn("queue depth poll: scan", "error", err)
			}
			return
		}
		counts[kind] = float64(n)
	}
	if err := rows.Err(); err != nil {
		if logger != nil {
			logger.Warn("queue depth poll: rows", "error", err)
		}
		return
	}
	for kind, n := range counts {
		QueueDepth.WithLabelValues(kind).Set(n)
	}
}
