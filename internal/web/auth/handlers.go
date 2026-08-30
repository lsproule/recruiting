// Package auth serves login, logout, password reset, and magic-link entry
// pages for both HTML surfaces. Handlers call internal/service only.
package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"

	"recruiting/internal/service"
	"recruiting/internal/web/middleware"
)

// Deps is what Mount needs. SendPasswordReset delivers the reset link to the
// address; nil disables the reset flow's email (the link is dropped).
type Deps struct {
	Auth  *service.AuthService
	Links *service.MagicLinkService
	// BaseURL is the public origin used in reset links; defaults to the
	// request's origin when empty.
	BaseURL           string
	SendPasswordReset func(ctx context.Context, email, link string) error
	// CookieSecret seals the assessment cookie; required.
	CookieSecret []byte
	// SecureCookies sets the Secure flag and __Host- CSRF cookie. Nil means
	// "derive from BaseURL scheme".
	SecureCookies *bool
}

var ErrNoCookieSecret = errors.New("auth: CookieSecret is required")

func (d Deps) secure() bool {
	if d.SecureCookies != nil {
		return *d.SecureCookies
	}
	return strings.HasPrefix(strings.ToLower(d.BaseURL), "https://")
}

// surface describes one HTML surface's auth routes.
type surface struct {
	kind   service.Surface
	prefix string // "/app" or "/client"
	title  string
}

var surfaces = []surface{
	{service.SurfaceApp, "/app", "Sign in"},
	{service.SurfaceClient, "/client", "Client sign in"},
}

// Mount attaches the shared middleware (principal resolution and CSRF) to
// mux and registers /app and /client login, logout, and reset routes plus the
// assessment link entry point. Routes registered on mux afterwards inherit the
// middleware, so callers must Mount before their own surface routes. The
// returned Auth supplies the assessment middleware bound to the same seal key.
func Mount(mux *chi.Mux, d Deps) (*Auth, error) {
	if len(d.CookieSecret) == 0 {
		return nil, ErrNoCookieSecret
	}
	mux.Use(middleware.CSRF(d.secure()), middleware.Authenticate(d.Auth))
	h := &handlers{d: d, seal: newSealer(d.CookieSecret), secure: d.secure()}
	for _, s := range surfaces {
		s := s
		mux.Get(s.prefix+"/login", h.loginPage(s))
		mux.Post(s.prefix+"/login", h.login(s))
		mux.Post(s.prefix+"/logout", h.logout(s))
		mux.Get(s.prefix+"/reset", h.resetRequestPage(s))
		mux.Post(s.prefix+"/reset", h.resetRequest(s))
		mux.Get(s.prefix+"/reset/{token}", h.resetPage(s))
		mux.Post(s.prefix+"/reset/{token}", h.reset(s))
	}
	mux.Get("/assess/{token}", h.assessmentEntry)
	return &Auth{h: h}, nil
}

// Auth is the mounted auth surface.
type Auth struct{ h *handlers }

// Assessment resolves the sealed assessment cookie set by /assess/{token}
// and attaches the link's principal; use it on every /assess/ page.
func (a *Auth) Assessment() func(http.Handler) http.Handler {
	return a.h.assessment
}

type handlers struct {
	d      Deps
	seal   *sealer
	secure bool
}

func render(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = c.Render(r.Context(), w)
}

func (h *handlers) loginPage(s surface) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := middleware.PrincipalFrom(r.Context()); ok {
			http.Redirect(w, r, s.prefix+"/", http.StatusSeeOther)
			return
		}
		render(w, r, http.StatusOK, loginPage(loginForm{Title: s.title, Action: s.prefix + "/login", ResetPath: s.prefix + "/reset", CSRF: middleware.CSRFToken(r)}))
	}
}

func (h *handlers) login(s surface) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		email := strings.TrimSpace(r.PostFormValue("email"))
		prior := ""
		if c, err := r.Cookie(middleware.SessionCookie); err == nil {
			prior = c.Value
		}
		res, err := h.d.Auth.Login(r.Context(), s.kind, email, r.PostFormValue("password"), prior)
		if errors.Is(err, service.ErrInvalidCredentials) {
			render(w, r, http.StatusUnprocessableEntity, loginPage(loginForm{
				Title: s.title, Action: s.prefix + "/login", ResetPath: s.prefix + "/reset", Email: email,
				Error: "Invalid email or password.", CSRF: middleware.CSRFToken(r),
			}))
			return
		}
		if err != nil {
			http.Error(w, "login failed", http.StatusInternalServerError)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name: middleware.SessionCookie, Value: res.Token, Path: "/", HttpOnly: true,
			SameSite: http.SameSiteLaxMode, Secure: h.secure, Expires: res.ExpiresAt,
		})
		http.Redirect(w, r, s.prefix+"/", http.StatusSeeOther)
	}
}

func (h *handlers) logout(s surface) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(middleware.SessionCookie); err == nil {
			if err := h.d.Auth.Logout(r.Context(), c.Value); err != nil {
				http.Error(w, "logout failed", http.StatusInternalServerError)
				return
			}
		}
		http.SetCookie(w, &http.Cookie{
			Name: middleware.SessionCookie, Value: "", Path: "/", HttpOnly: true, MaxAge: -1,
			SameSite: http.SameSiteLaxMode, Secure: h.secure,
		})
		http.Redirect(w, r, s.prefix+"/login", http.StatusSeeOther)
	}
}

func (h *handlers) resetRequestPage(s surface) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		render(w, r, http.StatusOK, resetRequestPage(resetRequestForm{Action: s.prefix + "/reset", CSRF: middleware.CSRFToken(r)}))
	}
}

func (h *handlers) resetRequest(s surface) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		email := strings.TrimSpace(r.PostFormValue("email"))
		token, err := h.d.Auth.RequestPasswordReset(r.Context(), s.kind, email)
		if err != nil {
			http.Error(w, "reset failed", http.StatusInternalServerError)
			return
		}
		if token != "" && h.d.SendPasswordReset != nil {
			link := h.origin(r) + s.prefix + "/reset/" + token
			if err := h.d.SendPasswordReset(r.Context(), email, link); err != nil {
				http.Error(w, "could not send reset email", http.StatusInternalServerError)
				return
			}
		}
		// Same response whether or not the account exists.
		render(w, r, http.StatusOK, resetRequestPage(resetRequestForm{Action: s.prefix + "/reset", Sent: true}))
	}
}

func (h *handlers) origin(r *http.Request) string {
	if h.d.BaseURL != "" {
		return strings.TrimRight(h.d.BaseURL, "/")
	}
	scheme := "http"
	if h.secure {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (h *handlers) resetPage(s surface) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := chi.URLParam(r, "token")
		if err := h.d.Auth.CheckPasswordReset(r.Context(), token); err != nil {
			renderResetError(w, r, err)
			return
		}
		render(w, r, http.StatusOK, resetPage(resetForm{Action: s.prefix + "/reset/" + token, CSRF: middleware.CSRFToken(r)}))
	}
}

func (h *handlers) reset(s surface) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := chi.URLParam(r, "token")
		err := h.d.Auth.ResetPassword(r.Context(), token, r.PostFormValue("password"))
		switch {
		case errors.Is(err, service.ErrPasswordTooShort):
			render(w, r, http.StatusUnprocessableEntity, resetPage(resetForm{Action: s.prefix + "/reset/" + token, Error: "Password must be at least 10 characters.", CSRF: middleware.CSRFToken(r)}))
		case err != nil:
			renderResetError(w, r, err)
		default:
			http.Redirect(w, r, s.prefix+"/login", http.StatusSeeOther)
		}
	}
}

func renderResetError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, service.ErrResetInvalid) {
		render(w, r, http.StatusGone, errorPage("Reset link no longer valid", "This password reset link is no longer valid. Request a new one from the sign-in page."))
		return
	}
	http.Error(w, "reset failed", http.StatusInternalServerError)
}
