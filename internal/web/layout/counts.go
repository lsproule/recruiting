package layout

import (
	"context"
	"log/slog"
	"net/http"
	"sync"

	"recruiting/internal/service"
	"recruiting/internal/web/middleware"
)

// CountSource computes every sidebar badge for one signed-in user.
type CountSource func(ctx context.Context, p service.Principal) (map[string]int, error)

type countsKey struct{}

// liveCounts computes the badges at most once per request, and only if a
// page actually draws the sidebar: a screen that renders no chrome, or a
// download, never pays for the queries.
type liveCounts struct {
	once   sync.Once
	src    CountSource
	p      service.Principal
	logger *slog.Logger
	counts map[string]int
}

func (l *liveCounts) get(ctx context.Context) map[string]int {
	l.once.Do(func() {
		counts, err := l.src(ctx, l.p)
		if err != nil {
			// A badge is decoration: a failed count leaves the sidebar bare
			// rather than failing the screen the visitor asked for.
			if l.logger != nil {
				l.logger.ErrorContext(ctx, "sidebar counts failed", "error", err)
			}
			return
		}
		l.counts = counts
	})
	return l.counts
}

// WithCounts puts the sidebar's live badges on every request of an org user.
// Install it after the authentication middleware, which sets the principal
// this reads.
func WithCounts(src CountSource, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := middleware.PrincipalFrom(r.Context())
			if !ok || src == nil || p.Kind != service.PrincipalOrgUser {
				next.ServeHTTP(w, r)
				return
			}
			live := &liveCounts{src: src, p: p, logger: logger}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), countsKey{}, live)))
		})
	}
}

// SupplyCounts hands the request the badges a screen has already computed,
// so the sidebar draws them and the middleware never computes its own. A
// screen whose read produces the numbers anyway (the queue itself) calls it
// before rendering; later calls on the same request are ignored, as is a
// request the middleware is not on.
func SupplyCounts(ctx context.Context, counts map[string]int) {
	if l, ok := ctx.Value(countsKey{}).(*liveCounts); ok {
		l.once.Do(func() { l.counts = counts })
	}
}

// counts is what the sidebar draws: the request's live badges, with anything
// the page set itself on top, since a screen knows its own numbers best.
func (p Page) counts(ctx context.Context) map[string]int {
	var live map[string]int
	if l, ok := ctx.Value(countsKey{}).(*liveCounts); ok {
		live = l.get(ctx)
	}
	if len(p.NavCounts) == 0 {
		return live
	}
	out := make(map[string]int, len(live)+len(p.NavCounts))
	for key, n := range live {
		out[key] = n
	}
	for key, n := range p.NavCounts {
		out[key] = n
	}
	return out
}
