// Package availability serves the vetter's own schedule: the weekly grid of
// hours they interview in, one-off exceptions, and the interviews booked
// against them with their outcomes. Handlers call internal/service only.
package availability

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"recruiting/internal/domain"
	"recruiting/internal/service"
	"recruiting/internal/web/layout"
	"recruiting/internal/web/middleware"
)

// Prefix is where the page lives on the app surface.
const Prefix = "/app/availability"

// Deps is what Mount needs. Org supplies the signed-in user's display name.
type Deps struct {
	Schedule *service.ScheduleService
	Org      *service.OrgService
	// Logger records the errors behind a 500; nil disables that logging.
	Logger *slog.Logger
}

// Mount registers the availability screens on r. It expects the shared auth
// middleware to be installed already; the service refuses non-vetters.
func Mount(r chi.Router, d Deps) {
	h := &handlers{d: d}
	r.Route(Prefix, func(r chi.Router) {
		r.Use(middleware.RequireAuth("/app/login"))
		r.Get("/", h.show)
		r.Post("/rules", h.saveRules)
		r.Post("/exceptions", h.addException)
		r.Post("/exceptions/{id}/delete", h.deleteException)
		r.Post("/slots/{id}/outcome", h.outcome)
	})
}

type handlers struct{ d Deps }

func render(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = c.Render(r.Context(), w)
}

func (h *handlers) page(r *http.Request, flashes ...layout.Flash) layout.Page {
	p, _ := middleware.PrincipalFrom(r.Context())
	return layout.Page{
		Title: "Availability", Surface: layout.SurfaceApp, Nav: layout.AppNav(p, Prefix), UserRole: layout.RoleLabel(p), Menu: layout.AppMenu(p, Prefix),
		Flashes: flashes, CSRF: middleware.CSRFToken(r), UserName: h.displayName(r, p),
	}
}

func (h *handlers) displayName(r *http.Request, p service.Principal) string {
	if h.d.Org == nil {
		return ""
	}
	u, err := h.d.Org.User(r.Context(), p, p.UserID)
	if err != nil {
		return ""
	}
	if u.Name != "" {
		return u.Name
	}
	return u.Email
}

func (h *handlers) show(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, http.StatusOK, nil)
}

func (h *handlers) render(w http.ResponseWriter, r *http.Request, status int, flashes []layout.Flash) {
	p, _ := middleware.PrincipalFrom(r.Context())
	av, err := h.d.Schedule.Availability(r.Context(), p)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, status, availabilityPage(h.page(r, flashes...), av, middleware.CSRFToken(r)))
}

// ruleForm is one row of the grid as the browser posts it.
type ruleForm struct {
	Weekday int    `json:"weekday"`
	Start   string `json:"start"`
	End     string `json:"end"`
}

func (h *handlers) saveRules(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	var rules []ruleForm
	if raw := strings.TrimSpace(r.PostFormValue("rules")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &rules); err != nil {
			http.Error(w, "bad rules", http.StatusBadRequest)
			return
		}
	}
	in := service.AvailabilityInput{
		Timezone:      strings.TrimSpace(r.PostFormValue("timezone")),
		SlotMinutes:   atoi(r.PostFormValue("slot_minutes")),
		BufferMinutes: atoi(r.PostFormValue("buffer_minutes")),
	}
	for _, rule := range rules {
		in.Rules = append(in.Rules, service.RuleInput{Weekday: time.Weekday(rule.Weekday), Start: rule.Start, End: rule.End})
	}
	h.after(w, r, h.d.Schedule.SetAvailability(r.Context(), p, in), "Availability saved.")
}

func (h *handlers) addException(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	tz := strings.TrimSpace(r.PostFormValue("timezone"))
	loc, err := time.LoadLocation(tz)
	if err != nil || tz == "" {
		h.after(w, r, service.ErrBadTimezone, "")
		return
	}
	// datetime-local values carry no zone; they are read in the vetter's.
	start, err1 := time.ParseInLocation("2006-01-02T15:04", r.PostFormValue("start"), loc)
	end, err2 := time.ParseInLocation("2006-01-02T15:04", r.PostFormValue("end"), loc)
	if err1 != nil || err2 != nil {
		h.after(w, r, service.ErrBadException, "")
		return
	}
	h.after(w, r, h.d.Schedule.AddException(r.Context(), p, start, end, r.PostFormValue("reason")), "Exception added.")
}

func (h *handlers) deleteException(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := param(w, r, "id")
	if !ok {
		return
	}
	h.after(w, r, h.d.Schedule.DeleteException(r.Context(), p, id), "Exception removed.")
}

func (h *handlers) outcome(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := param(w, r, "id")
	if !ok {
		return
	}
	h.after(w, r, h.d.Schedule.SetOutcome(r.Context(), p, id, r.PostFormValue("outcome")), "Outcome recorded.")
}

// after redirects back to the page on success, or re-renders it with the
// refusal.
func (h *handlers) after(w http.ResponseWriter, r *http.Request, err error, success string) {
	if err == nil {
		http.Redirect(w, r, Prefix, http.StatusSeeOther)
		return
	}
	status := statusFor(err)
	if status == http.StatusInternalServerError || status == http.StatusForbidden {
		h.fail(w, r, err)
		return
	}
	h.render(w, r, status, []layout.Flash{{Kind: "error", Message: userMessage(err)}})
}

func (h *handlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	status := statusFor(err)
	if status == http.StatusInternalServerError && h.d.Logger != nil {
		h.d.Logger.ErrorContext(r.Context(), "availability screen failed", "path", r.URL.Path, "error", err)
	}
	http.Error(w, userMessage(err), status)
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, service.ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, service.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, service.ErrBadTimezone), errors.Is(err, service.ErrBadRule),
		errors.Is(err, service.ErrBadOutcome), errors.Is(err, service.ErrBadException), errors.Is(err, domain.ErrBadTimezone):
		return http.StatusUnprocessableEntity
	}
	return http.StatusInternalServerError
}

func userMessage(err error) string {
	if statusFor(err) == http.StatusInternalServerError {
		return "Something went wrong. Try again."
	}
	msg := err.Error()
	for _, prefix := range []string{"service: ", "domain: "} {
		msg = strings.TrimPrefix(msg, prefix)
	}
	return msg
}

func param(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		http.Error(w, "bad "+name, http.StatusBadRequest)
		return uuid.Nil, false
	}
	return id, true
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}
