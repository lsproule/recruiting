// Package middleware resolves the request principal and enforces CSRF for
// every HTML surface. It calls internal/service only.
package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"recruiting/internal/service"
)

// SessionCookie holds the raw session token; the store keeps only its hash.
const SessionCookie = "session"

// SessionResolver is the subset of service.AuthService the middleware needs.
type SessionResolver interface {
	ResolveSession(ctx context.Context, token string) (service.Principal, error)
}

type ctxKey struct{}

func WithPrincipal(ctx context.Context, p service.Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// PrincipalFrom returns the principal the middleware attached, if any.
func PrincipalFrom(ctx context.Context) (service.Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(service.Principal)
	return p, ok
}

// Authenticate resolves the session cookie into a principal. An org-user
// session on /client/* or a client-user session on /app/* is refused with
// 403 rather than ignored: the surfaces must never share a session.
// Requests without a valid cookie pass through anonymous.
func Authenticate(r SessionResolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			c, err := req.Cookie(SessionCookie)
			if err != nil || c.Value == "" {
				next.ServeHTTP(w, req)
				return
			}
			p, err := r.ResolveSession(req.Context(), c.Value)
			if err != nil {
				if !errors.Is(err, service.ErrNoSession) {
					http.Error(w, "session lookup failed", http.StatusInternalServerError)
					return
				}
				next.ServeHTTP(w, req)
				return
			}
			if !SurfaceAllows(req.URL.Path, p) {
				http.Error(w, "this session cannot be used on this surface", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, req.WithContext(WithPrincipal(req.Context(), p)))
		})
	}
}

// SurfaceAllows reports whether p's kind may use the surface owning path.
func SurfaceAllows(path string, p service.Principal) bool {
	switch {
	case strings.HasPrefix(path, "/app/") || path == "/app":
		return p.Kind == service.PrincipalOrgUser
	case strings.HasPrefix(path, "/client/") || path == "/client":
		return p.Kind == service.PrincipalClientUser
	}
	return true
}

// RequireAuth redirects anonymous requests to loginPath.
func RequireAuth(loginPath string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if _, ok := PrincipalFrom(req.Context()); !ok {
				http.Redirect(w, req, loginPath, http.StatusSeeOther)
				return
			}
			next.ServeHTTP(w, req)
		})
	}
}
