package main

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"recruiting/internal/api"
	"recruiting/internal/service"
	"recruiting/internal/web/layout"
	"recruiting/internal/web/middleware"
)

// apiTokensPath is where the admin issues and revokes API tokens. It lives
// in cmd/recruiting rather than internal/web/admin because api.TokensFragment
// (T19) is the only piece of UI this screen needs, and the fragment already
// carries its own form markup.
const apiTokensPath = "/app/admin/api-tokens"

type apiTokensDeps struct {
	Tokens *service.APITokenService
	Org    *service.OrgService
	Logger *slog.Logger
}

// mountAPITokens registers the API token screen on r. It expects the shared
// auth middleware (CSRF and Authenticate) to be installed already.
func mountAPITokens(r chi.Router, d apiTokensDeps) {
	h := &apiTokensHandlers{d: d}
	r.Route(apiTokensPath, func(r chi.Router) {
		r.Use(middleware.RequireAuth("/app/login"), requireAdminPrincipal)
		r.Get("/", h.show)
		r.Post("/", h.issue)
		r.Post("/{id}/revoke", h.revoke)
	})
}

// requireAdminPrincipal refuses anyone but an org admin. Authentication has
// already happened by the time this runs, so a non-admin gets a 403 rather
// than a redirect to login.
func requireAdminPrincipal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := middleware.PrincipalFrom(r.Context())
		if !ok || p.Kind != service.PrincipalOrgUser || !p.HasRole(service.RoleAdmin) {
			http.Error(w, "admin role required", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type apiTokensHandlers struct{ d apiTokensDeps }

func (h *apiTokensHandlers) show(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, http.StatusOK, "")
}

// render loads the live tokens and the org's users and draws the screen.
// secret, when set, is a just-issued token's raw value, shown once.
func (h *apiTokensHandlers) render(w http.ResponseWriter, r *http.Request, status int, secret string) {
	p, _ := middleware.PrincipalFrom(r.Context())
	tokens, err := h.d.Tokens.List(r.Context(), p)
	if err != nil {
		http.Error(w, userMessage(err), statusFor(err))
		return
	}
	users, err := h.d.Org.ListUsers(r.Context(), p)
	if err != nil {
		http.Error(w, userMessage(err), statusFor(err))
		return
	}
	view := api.TokensView{
		Tokens:     tokens,
		Users:      users,
		IssuePath:  apiTokensPath,
		RevokePath: apiTokensPath,
		Secret:     secret,
		CSRF:       middleware.CSRFToken(r),
	}
	page := layout.Page{
		Title: "API tokens", Surface: layout.SurfaceApp,
		Nav: layout.AppNav(p, apiTokensPath), CSRF: middleware.CSRFToken(r),
	}
	writeHTML(w, r, status, page, api.TokensFragment(view))
}

func (h *apiTokensHandlers) issue(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	userID, err := uuid.Parse(r.PostFormValue("user_id"))
	if err != nil {
		h.render(w, r, http.StatusUnprocessableEntity, "")
		return
	}
	in := service.NewAPIToken{Name: r.PostFormValue("name"), UserID: userID}
	if raw := strings.TrimSpace(r.PostFormValue("expires_on")); raw != "" {
		exp, err := time.Parse("2006-01-02", raw)
		if err != nil {
			h.render(w, r, http.StatusUnprocessableEntity, "")
			return
		}
		exp = exp.Add(24*time.Hour - time.Nanosecond) // through the end of the chosen day
		in.ExpiresAt = &exp
	}
	_, secret, err := h.d.Tokens.Issue(r.Context(), p, in)
	if err != nil {
		h.render(w, r, statusFor(err), "")
		return
	}
	h.render(w, r, http.StatusOK, secret)
}

func (h *apiTokensHandlers) revoke(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "bad token id", http.StatusBadRequest)
		return
	}
	if err := h.d.Tokens.Revoke(r.Context(), p, id); err != nil {
		h.render(w, r, statusFor(err), "")
		return
	}
	h.render(w, r, http.StatusOK, "")
}

// statusFor maps a service error onto the response status: rejected input is
// 422, a missing row 404, forbidden 403, anything else a 500.
func statusFor(err error) int {
	switch {
	case err == nil:
		return http.StatusOK
	case isNotFound(err):
		return http.StatusNotFound
	case isForbidden(err):
		return http.StatusForbidden
	case isInvalid(err):
		return http.StatusUnprocessableEntity
	}
	return http.StatusInternalServerError
}

func isNotFound(err error) bool  { return errors.Is(err, service.ErrNotFound) }
func isForbidden(err error) bool { return errors.Is(err, service.ErrForbidden) }
func isInvalid(err error) bool {
	return errors.Is(err, service.ErrTokenName) || errors.Is(err, service.ErrTokenExpired)
}

// userMessage strips the service's package prefix from an error meant for a
// person; unexpected errors are not shown at all.
func userMessage(err error) string {
	if statusFor(err) == http.StatusInternalServerError {
		return "Something went wrong. Try again."
	}
	return strings.TrimPrefix(err.Error(), "service: ")
}

// writeHTML renders body inside the app chrome. It exists here rather than
// reusing a web package's own render helper because this screen composes
// api.TokensFragment directly instead of a page built with .templ.
func writeHTML(w http.ResponseWriter, r *http.Request, status int, page layout.Page, body templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = layout.Base(page).Render(templ.WithChildren(r.Context(), body), w)
}
