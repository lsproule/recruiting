// Package admin serves the org administration screens: org users and roles,
// client companies and their users, and org settings. Handlers call
// internal/service only.
package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"recruiting/internal/service"
	"recruiting/internal/web/layout"
	"recruiting/internal/web/middleware"
)

// Prefix is where the admin screens live on the app surface.
const Prefix = "/app/admin"

// Deps is what Mount needs. SendInvite, when set, mails a new account its
// password-set link; the link is shown on screen either way so an admin can
// pass it on when mail is not configured.
type Deps struct {
	Org *service.OrgService
	// BaseURL is the public origin used in password-set links. It comes from
	// required configuration; links are relative when it is empty.
	BaseURL    string
	SendInvite func(ctx context.Context, email, link string) error
}

// Mount registers the admin screens on r. It expects the shared auth
// middleware (CSRF and Authenticate) to be installed already.
func Mount(r chi.Router, d Deps) {
	h := &handlers{d: d}
	r.Route(Prefix, func(r chi.Router) {
		r.Use(middleware.RequireAuth("/app/login"), requireAdmin)
		r.Get("/", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, Prefix+"/users", http.StatusSeeOther)
		})
		r.Get("/users", h.users)
		r.Post("/users", h.createUser)
		r.Post("/users/{id}/roles", h.setRoles)
		r.Get("/clients", h.clients)
		r.Post("/clients", h.createCompany)
		r.Post("/clients/users", h.createClientUser)
		r.Get("/settings", h.settings)
		r.Post("/settings", h.saveSettings)
	})
}

// requireAdmin refuses anyone but an org admin. Authentication has already
// happened, so a non-admin is a 403 rather than a redirect to login.
func requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := middleware.PrincipalFrom(r.Context())
		if !ok || p.Kind != service.PrincipalOrgUser || !p.HasRole(service.RoleAdmin) {
			http.Error(w, "admin role required", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type handlers struct{ d Deps }

func (h *handlers) page(r *http.Request, title, current string, flashes ...layout.Flash) layout.Page {
	p, _ := middleware.PrincipalFrom(r.Context())
	return layout.Page{
		Title: title, Surface: layout.SurfaceApp, Nav: layout.AppNav(p, current), UserRole: layout.RoleLabel(p), Menu: layout.AppMenu(p, current),
		Flashes: flashes, CSRF: middleware.CSRFToken(r), UserName: h.displayName(r, p),
	}
}

// displayName is the signed-in user's name for the chrome. A lookup failure
// hides the name rather than failing the page it decorates.
func (h *handlers) displayName(r *http.Request, p service.Principal) string {
	u, err := h.d.Org.User(r.Context(), p, p.UserID)
	if err != nil {
		return ""
	}
	if u.Name != "" {
		return u.Name
	}
	return u.Email
}

func render(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = c.Render(r.Context(), w)
}

// invite turns a one-time token into a link, mailing it when a sender is
// configured. The link is returned so the page can show it too.
func (h *handlers) invite(r *http.Request, email, token string) (string, error) {
	link := strings.TrimRight(h.d.BaseURL, "/") + "/app/reset/" + token
	if h.d.SendInvite == nil {
		return link, nil
	}
	return link, h.d.SendInvite(r.Context(), email, link)
}

// statusFor maps a service error onto the response status: rejected input is
// 422, a missing row 404, and anything else a 500.
func statusFor(err error) int {
	switch {
	case errors.Is(err, service.ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, service.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, service.ErrInvalidSettings),
		errors.Is(err, service.ErrInvalidRole),
		errors.Is(err, service.ErrNameRequired),
		errors.Is(err, service.ErrEmailRequired),
		errors.Is(err, service.ErrCompanyRequired),
		errors.Is(err, service.ErrEmailTaken):
		return http.StatusUnprocessableEntity
	}
	return http.StatusInternalServerError
}

func errFlash(err error) layout.Flash {
	return layout.Flash{Kind: "error", Message: userMessage(err)}
}

// userMessage strips the service's package prefix from an error meant for a
// person; unexpected errors are not shown at all.
func userMessage(err error) string {
	if statusFor(err) == http.StatusInternalServerError {
		return "Something went wrong. Try again."
	}
	return strings.TrimPrefix(err.Error(), "service: ")
}

func (h *handlers) users(w http.ResponseWriter, r *http.Request) {
	h.renderUsers(w, r, http.StatusOK)
}

func (h *handlers) renderUsers(w http.ResponseWriter, r *http.Request, status int, flashes ...layout.Flash) {
	p, _ := middleware.PrincipalFrom(r.Context())
	users, err := h.d.Org.ListUsers(r.Context(), p)
	if err != nil {
		http.Error(w, userMessage(err), statusFor(err))
		return
	}
	render(w, r, status, usersPage(h.page(r, "Users", Prefix+"/users", flashes...), users, p.UserID))
}

func (h *handlers) createUser(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	user, token, err := h.d.Org.CreateUser(r.Context(), p, service.NewUser{
		Name: r.PostFormValue("name"), Email: r.PostFormValue("email"), Roles: r.PostForm["roles"],
	})
	if err != nil {
		h.renderUsers(w, r, statusFor(err), errFlash(err))
		return
	}
	link, err := h.invite(r, user.Email, token)
	if err != nil {
		h.renderUsers(w, r, http.StatusOK, layout.Flash{Kind: "error", Message: "User created, but the invitation email failed. Link: " + link})
		return
	}
	h.renderUsers(w, r, http.StatusOK, layout.Flash{Kind: "success", Message: "Created " + user.Email + ". Password-set link: " + link})
}

func (h *handlers) setRoles(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "bad user id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if err := h.d.Org.SetUserRoles(r.Context(), p, id, r.PostForm["roles"]); err != nil {
		h.renderUsers(w, r, statusFor(err), errFlash(err))
		return
	}
	h.renderUsers(w, r, http.StatusOK, layout.Flash{Kind: "success", Message: "Roles updated."})
}

func (h *handlers) clients(w http.ResponseWriter, r *http.Request) {
	h.renderClients(w, r, http.StatusOK)
}

func (h *handlers) renderClients(w http.ResponseWriter, r *http.Request, status int, flashes ...layout.Flash) {
	p, _ := middleware.PrincipalFrom(r.Context())
	companies, err := h.d.Org.ListClientCompanies(r.Context(), p)
	if err != nil {
		http.Error(w, userMessage(err), statusFor(err))
		return
	}
	users, err := h.d.Org.ListClientUsers(r.Context(), p)
	if err != nil {
		http.Error(w, userMessage(err), statusFor(err))
		return
	}
	render(w, r, status, clientsPage(h.page(r, "Clients", Prefix+"/clients", flashes...), companies, users))
}

func (h *handlers) createCompany(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	company, err := h.d.Org.CreateClientCompany(r.Context(), p, r.PostFormValue("name"))
	if err != nil {
		h.renderClients(w, r, statusFor(err), errFlash(err))
		return
	}
	h.renderClients(w, r, http.StatusOK, layout.Flash{Kind: "success", Message: "Added " + company.Name + "."})
}

func (h *handlers) createClientUser(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	in := service.NewClientUser{Name: r.PostFormValue("name"), Email: r.PostFormValue("email")}
	if raw := strings.TrimSpace(r.PostFormValue("client_company_id")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			h.renderClients(w, r, http.StatusUnprocessableEntity, errFlash(service.ErrCompanyRequired))
			return
		}
		in.ClientCompanyID = id
	}
	user, token, err := h.d.Org.CreateClientUser(r.Context(), p, in)
	if err != nil {
		h.renderClients(w, r, statusFor(err), errFlash(err))
		return
	}
	link, err := h.invite(r, user.Email, token)
	if err != nil {
		h.renderClients(w, r, http.StatusOK, layout.Flash{Kind: "error", Message: "User created, but the invitation email failed. Link: " + link})
		return
	}
	h.renderClients(w, r, http.StatusOK, layout.Flash{Kind: "success", Message: "Created " + user.Email + ". Password-set link: " + link})
}

func (h *handlers) settings(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	s, err := h.d.Org.Settings(r.Context(), p)
	if err != nil {
		http.Error(w, userMessage(err), statusFor(err))
		return
	}
	render(w, r, http.StatusOK, settingsPage(h.page(r, "Settings", Prefix+"/settings"), s))
}

func (h *handlers) saveSettings(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	in, err := settingsFromForm(r)
	if err == nil {
		err = h.d.Org.UpdateSettings(r.Context(), p, in)
	}
	if err != nil {
		render(w, r, statusFor(err), settingsPage(h.page(r, "Settings", Prefix+"/settings", errFlash(err)), in))
		return
	}
	render(w, r, http.StatusOK, settingsPage(h.page(r, "Settings", Prefix+"/settings", layout.Flash{Kind: "success", Message: "Settings saved."}), in))
}

// settingsFromForm reads the settings form. Unparseable numbers are reported
// as invalid settings so the form comes back with the offending field named.
func settingsFromForm(r *http.Request) (service.Settings, error) {
	s := service.DefaultSettings()
	var bad []string
	intField := func(key string, into *int) {
		v := strings.TrimSpace(r.PostFormValue(key))
		n, err := strconv.Atoi(v)
		if err != nil {
			bad = append(bad, key)
			return
		}
		*into = n
	}
	intField(service.SettingPoolScoreThreshold, &s.PoolScoreThreshold)
	intField(service.SettingAssessmentInviteDays, &s.AssessmentInviteDays)
	intField(service.SettingSnapshotRetentionDays, &s.SnapshotRetentionDays)
	s.RejectionEmail = r.PostFormValue(service.SettingRejectionEmail) == "1"
	for _, name := range service.IntegritySignalNames {
		v := strings.TrimSpace(r.PostFormValue("weight." + name))
		if v == "" {
			continue
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			bad = append(bad, name)
			continue
		}
		s.IntegrityWeights[name] = f
	}
	if len(bad) > 0 {
		return s, fmt.Errorf("%w: not a number: %s", service.ErrInvalidSettings, strings.Join(bad, ", "))
	}
	return s, s.Validate()
}
