// Package book serves the candidate's booking page behind their magic link:
// the interviewer's open slots in the candidate's own timezone, and the
// booking they hold, which they may move or cancel until two hours before it
// starts. Handlers call internal/service only.
package book

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"

	"recruiting/internal/domain"
	"recruiting/internal/service"
	"recruiting/internal/web/auth"
	"recruiting/internal/web/layout"
	"recruiting/internal/web/middleware"
	"recruiting/internal/web/room"
)

// Prefix is where booking links land; the token follows it.
const Prefix = "/book"

// Deps is what Mount needs.
type Deps struct {
	Schedule *service.ScheduleService
	Links    *service.MagicLinkService
	// Room mounts the video room a booked interview opens into, under the
	// candidate's own link. A zero Room mounts none.
	Room room.Deps
	// Logger records the errors behind a 500; nil disables that logging.
	Logger *slog.Logger
}

// Mount registers the booking page on r. It sits outside any session: the
// link token is the credential, resolved by the shared magic-link middleware.
// The shared CSRF middleware still applies, so Mount must come after
// auth.Mount.
func Mount(r chi.Router, d Deps) {
	h := &handlers{d: d}
	r.Route(Prefix+"/{token}", func(r chi.Router) {
		r.Use(auth.MagicLink(d.Links, service.LinkBook))
		r.Get("/", h.show)
		r.Post("/", h.book)
		r.Post("/cancel", h.cancel)
		if d.Room.Rooms != nil {
			r.Route("/room/{roomID}", func(r chi.Router) {
				room.MountCandidate(r, service.RoomSlot, d.Room)
			})
		}
	})
}

type handlers struct{ d Deps }

func render(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = c.Render(r.Context(), w)
}

// The booking link is candidate-facing, so the page takes the slim public
// chrome rather than the recruiter shell.
func page(r *http.Request, title string, flashes ...layout.Flash) layout.Page {
	return layout.Page{Title: title, Flashes: flashes, CSRF: middleware.CSRFToken(r)}
}

func (h *handlers) show(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, http.StatusOK, "", nil)
}

// render draws the page around the current state; tz is the candidate's
// last choice so the picker reopens on it.
func (h *handlers) render(w http.ResponseWriter, r *http.Request, status int, tz string, flashes []layout.Flash) {
	p, _ := middleware.PrincipalFrom(r.Context())
	b, err := h.d.Schedule.Booking(r.Context(), p)
	if errors.Is(err, service.ErrNoVetter) || errors.Is(err, service.ErrNotActive) {
		render(w, r, http.StatusOK, unavailablePage(page(r, "Book your interview"), userMessage(err)))
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if tz == "" && b.Current != nil {
		tz = b.Current.Timezone
	}
	render(w, r, status, bookingPage(page(r, "Book your interview", flashes...), b, tokenPath(r), tz, middleware.CSRFToken(r)))
}

func (h *handlers) book(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	tz := strings.TrimSpace(r.PostFormValue("timezone"))
	start, err := time.Parse(time.RFC3339, strings.TrimSpace(r.PostFormValue("starts_at")))
	if err != nil {
		h.render(w, r, http.StatusUnprocessableEntity, tz, []layout.Flash{{Kind: "error", Message: "Pick a time first."}})
		return
	}
	slot, err := h.d.Schedule.Book(r.Context(), p, chi.URLParam(r, "token"), start, tz)
	if err != nil {
		h.after(w, r, err, tz)
		return
	}
	h.render(w, r, http.StatusOK, tz, []layout.Flash{{Kind: "success", Message: "Booked for " + local(slot.Start, tz) + "."}})
}

func (h *handlers) cancel(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	if err := h.d.Schedule.Cancel(r.Context(), p, chi.URLParam(r, "token")); err != nil {
		h.after(w, r, err, "")
		return
	}
	h.render(w, r, http.StatusOK, "", []layout.Flash{{Kind: "success", Message: "Your interview is cancelled. Pick a new time whenever you are ready."}})
}

// after re-renders the page with the refusal. A taken slot comes back as 409
// with the list refreshed, so the candidate simply picks again.
func (h *handlers) after(w http.ResponseWriter, r *http.Request, err error, tz string) {
	status := statusFor(err)
	if status == http.StatusInternalServerError || status == http.StatusNotFound || status == http.StatusForbidden {
		h.fail(w, r, err)
		return
	}
	h.render(w, r, status, tz, []layout.Flash{{Kind: "error", Message: userMessage(err)}})
}

func (h *handlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	status := statusFor(err)
	if status == http.StatusInternalServerError && h.d.Logger != nil {
		h.d.Logger.ErrorContext(r.Context(), "booking page failed", "path", r.URL.Path, "error", err)
	}
	http.Error(w, userMessage(err), status)
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, service.ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, service.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, service.ErrSlotTaken):
		return http.StatusConflict
	case errors.Is(err, service.ErrTooLate), errors.Is(err, service.ErrNoBooking), errors.Is(err, service.ErrNoVetter),
		errors.Is(err, service.ErrNotActive), errors.Is(err, service.ErrBadTimezone), errors.Is(err, domain.ErrBadTimezone):
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

func tokenPath(r *http.Request) string {
	return Prefix + "/" + chi.URLParam(r, "token")
}

// local renders t in tz, falling back to UTC for a zone Go does not know.
func local(t time.Time, tz string) string {
	loc, err := time.LoadLocation(tz)
	if err != nil || tz == "" {
		return t.UTC().Format("Mon 2 Jan 2006 15:04") + " UTC"
	}
	return t.In(loc).Format("Mon 2 Jan 2006 15:04") + " " + tz
}
