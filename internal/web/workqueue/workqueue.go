// Package workqueue serves the recruiter's work queue: every pending action
// across the org on one screen, each row carrying the one link that resolves
// it and a snooze for the ones that can wait. Handlers call
// internal/service only.
package workqueue

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
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

// Prefix is where the queue lives on the app surface.
const Prefix = "/app/queue"

// SnoozePath is where a row's "not now" posts.
const SnoozePath = Prefix + "/snooze"

// DecidePath is where a row's inline decision posts: advance, reject, or
// forward to the client.
const DecidePath = Prefix + "/decide"

// filterParam names the rule the screen is narrowed to; absent means all.
const filterParam = "kind"

// Deps is what Mount needs. Org supplies the signed-in user's display name.
type Deps struct {
	Queue *service.WorkQueueService
	Org   *service.OrgService
	// Logger records the errors behind a 500; the visitor only ever sees a
	// generic message. Nil disables that logging.
	Logger *slog.Logger
}

// Mount registers the queue on r. It expects the shared auth middleware
// (CSRF and Authenticate) to be installed already; every org user has a
// queue, and the service decides what is in it.
func Mount(r chi.Router, d Deps) {
	h := &handlers{d: d}
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth("/app/login"))
		r.Get(Prefix, h.show)
		r.Post(SnoozePath, h.snooze)
		r.Post(DecidePath, h.decide)
	})
}

type handlers struct{ d Deps }

func render(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = c.Render(r.Context(), w)
}

func (h *handlers) page(r *http.Request, title string, flashes ...layout.Flash) layout.Page {
	p, _ := middleware.PrincipalFrom(r.Context())
	return layout.Page{
		Title: title, Surface: layout.SurfaceApp, Nav: layout.AppNav(p, Prefix),
		UserRole: layout.RoleLabel(p), Menu: layout.AppMenu(p, Prefix),
		Flashes: flashes, CSRF: middleware.CSRFToken(r), UserName: h.displayName(r, p),
	}
}

// displayName is the signed-in user's name for the chrome. A lookup failure
// hides the name rather than failing the page it decorates.
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
	h.render(w, r, parseFilter(r.URL.Query().Get(filterParam)))
}

// render draws the queue as it stands, with any flash the last action left.
func (h *handlers) render(w http.ResponseWriter, r *http.Request, filter service.QueueKind, flashes ...layout.Flash) {
	p, _ := middleware.PrincipalFrom(r.Context())
	items, err := h.d.Queue.List(r.Context(), p, filter)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	counts, err := h.d.Queue.Counts(r.Context(), p)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, http.StatusOK, queuePage(h.page(r, "Work queue", flashes...), queueView{
		Items: items, Counts: counts, Filter: filter,
	}, middleware.CSRFToken(r)))
}

// decide takes a row's inline decision and returns to the filter the
// recruiter was reading. A refused decision (a reject without a reason, a
// stage whose prerequisite is missing, a stale row) comes back as a flash on
// the same screen rather than an error page.
func (h *handlers) decide(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	appID, err := uuid.Parse(r.PostFormValue("application_id"))
	if err != nil {
		http.Error(w, "bad decision", http.StatusBadRequest)
		return
	}
	req := service.DecideRequest{
		ApplicationID: appID,
		Action:        strings.TrimSpace(r.PostFormValue("action")),
		Reason:        strings.TrimSpace(r.PostFormValue("reason")),
	}
	if to, err := uuid.Parse(r.PostFormValue("to_stage_id")); err == nil {
		req.ToStageID = to
	}
	back := backTo(r.PostFormValue("back"))
	if err := h.d.Queue.Decide(r.Context(), p, req); err != nil {
		if status := decideStatus(err); status == http.StatusInternalServerError {
			h.fail(w, r, err)
			return
		}
		h.render(w, r, filterOf(back), layout.Flash{Kind: "error", Message: strings.TrimPrefix(err.Error(), "service: ")})
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// decideStatus sorts a refused decision from a failure: the rules' own
// refusals are the recruiter's to read, anything else is ours.
func decideStatus(err error) int {
	switch {
	case errors.Is(err, service.ErrForbidden), errors.Is(err, domain.ErrForbiddenMove):
		return http.StatusForbidden
	case errors.Is(err, service.ErrNotFound), errors.Is(err, service.ErrBadDecision):
		return http.StatusNotFound
	case errors.Is(err, domain.ErrReasonRequired), errors.Is(err, domain.ErrPrereqMissing),
		errors.Is(err, domain.ErrTerminal), errors.Is(err, service.ErrStale):
		return http.StatusUnprocessableEntity
	}
	return http.StatusInternalServerError
}

// filterOf reads the filter back out of a return URL.
func filterOf(back string) service.QueueKind {
	if u, err := url.Parse(back); err == nil {
		return parseFilter(u.Query().Get(filterParam))
	}
	return ""
}

// snooze puts one row out of this user's sight for a day and returns to the
// filter they were reading.
func (h *handlers) snooze(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	kind := parseFilter(r.PostFormValue(filterParam))
	subject, err := uuid.Parse(r.PostFormValue("subject_id"))
	if kind == "" || err != nil {
		http.Error(w, "bad snooze", http.StatusBadRequest)
		return
	}
	if err := h.d.Queue.Snooze(r.Context(), p, kind, subject, time.Now().Add(service.SnoozeWindow)); err != nil {
		h.fail(w, r, err)
		return
	}
	http.Redirect(w, r, backTo(r.PostFormValue("back")), http.StatusSeeOther)
}

// backTo keeps the recruiter on the filter they snoozed from, and refuses
// anything that is not this screen.
func backTo(raw string) string {
	if strings.HasPrefix(raw, Prefix) && !strings.HasPrefix(raw, "//") {
		return raw
	}
	return Prefix
}

// parseFilter reads a rule name off the request; anything unknown, including
// the "all" chip, reads as no filter.
func parseFilter(raw string) service.QueueKind {
	for _, kind := range service.QueueKinds {
		if string(kind) == strings.TrimSpace(raw) {
			return kind
		}
	}
	return ""
}

func (h *handlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, service.ErrForbidden):
		status = http.StatusForbidden
	case errors.Is(err, service.ErrNotFound):
		status = http.StatusNotFound
	}
	if status == http.StatusInternalServerError {
		if h.d.Logger != nil {
			h.d.Logger.ErrorContext(r.Context(), "work queue failed", "path", r.URL.Path, "error", err)
		}
		http.Error(w, "Something went wrong. Try again.", status)
		return
	}
	http.Error(w, strings.TrimPrefix(err.Error(), "service: "), status)
}
