package auth

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"recruiting/internal/service"
	"recruiting/internal/web/middleware"
)

// AssessmentCookie carries a sealed assessment link token so the candidate's
// URL no longer exposes it while they work.
const (
	AssessmentCookie = "assessment"
	assessmentTTL    = 4 * time.Hour
	assessmentPath   = "/assess/"
)

// MagicLink resolves the {token} path parameter as a link of the given
// purpose and attaches its principal. GET only resolves (mail scanners
// prefetch links); apply links are consumed on POST, when the form submits.
func MagicLink(links *service.MagicLinkService, purpose string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := chi.URLParam(r, "token")
			var p service.Principal
			var err error
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				p, err = links.Resolve(r.Context(), token, purpose)
			} else {
				p, err = links.Consume(r.Context(), token, purpose)
			}
			if err != nil {
				renderLinkError(w, r, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(middleware.WithPrincipal(r.Context(), p)))
		})
	}
}

// assessmentEntry swaps the link token for a short-lived sealed cookie and
// redirects to the assessment page.
func (h *handlers) assessmentEntry(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	if _, err := h.d.Links.Resolve(r.Context(), token, service.LinkAssessment); err != nil {
		renderLinkError(w, r, err)
		return
	}
	sealed, err := h.seal.seal(token, time.Now())
	if err != nil {
		http.Error(w, "could not start assessment", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: AssessmentCookie, Value: sealed, Path: assessmentPath, HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: h.secure, MaxAge: int(assessmentTTL.Seconds()),
	})
	http.Redirect(w, r, assessmentPath, http.StatusSeeOther)
}

// Assessment resolves the sealed assessment cookie on /assess/ pages. Must be
// built from the same Deps as Mount so the seal key matches.
func (h *handlers) assessment(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(AssessmentCookie)
		if err != nil {
			renderLinkError(w, r, service.ErrLinkInvalid)
			return
		}
		token, err := h.seal.open(c.Value, time.Now())
		if err != nil {
			renderLinkError(w, r, service.ErrLinkInvalid)
			return
		}
		p, err := h.d.Links.Resolve(r.Context(), token, service.LinkAssessment)
		if err != nil {
			renderLinkError(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(middleware.WithPrincipal(r.Context(), p)))
	})
}

func renderLinkError(w http.ResponseWriter, r *http.Request, err error) {
	var title, msg string
	switch {
	case errors.Is(err, service.ErrLinkExpired):
		title, msg = "Link expired", "This link has expired. Please ask for a new one."
	case errors.Is(err, service.ErrLinkRevoked):
		title, msg = "Link revoked", "This link has been revoked and can no longer be used."
	case errors.Is(err, service.ErrLinkUsed):
		title, msg = "Link already used", "This link has already been used and cannot be opened again."
	case errors.Is(err, service.ErrLinkPurpose), errors.Is(err, service.ErrLinkInvalid):
		title, msg = "Invalid link", "This link is not valid for this page. Check the address you were sent."
	default:
		http.Error(w, "could not open link", http.StatusInternalServerError)
		return
	}
	render(w, r, http.StatusGone, errorPage(title, msg))
}
