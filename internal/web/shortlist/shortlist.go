// Package shortlist serves the recruiter's shortlist builder: the qualified
// pool on one side, the ranked picks and the note on the other, and the send
// that hands the packet to the client. Handlers call internal/service only.
package shortlist

import (
	"encoding/json"
	"errors"
	"log/slog"
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

// jobsPrefix is where the job screens live. The builder hangs off a job, and
// naming the prefix here keeps package jobs free of this package.
const jobsPrefix = "/app/jobs"

// Path is a job's shortlist builder.
func Path(jobID uuid.UUID) string { return jobsPrefix + "/" + jobID.String() + "/shortlist" }

func savePath(jobID uuid.UUID) string { return Path(jobID) + "/save" }
func sendPath(jobID uuid.UUID) string { return Path(jobID) + "/send" }

// Deps is what Mount needs. Org supplies the signed-in user's display name.
type Deps struct {
	Shortlists *service.ShortlistService
	Org        *service.OrgService
	// Logger records the errors behind a 500; the visitor only ever sees a
	// generic message. Nil disables that logging.
	Logger *slog.Logger
}

// Mount registers the builder on r. It expects the shared auth middleware
// (CSRF and Authenticate) to be installed already, and must be mounted after
// jobs.Mount, whose subrouter owns the rest of the job screens.
func Mount(r chi.Router, d Deps) {
	h := &handlers{d: d}
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth("/app/login"))
		r.Use(requireRecruiter)
		r.Get(jobsPrefix+"/{jobID}/shortlist", h.builder)
		r.Post(jobsPrefix+"/{jobID}/shortlist/save", h.save)
		r.Post(jobsPrefix+"/{jobID}/shortlist/send", h.send)
	})
}

// requireRecruiter refuses anyone but a recruiter or an admin: deciding what
// the client is shown is the recruiter's, which is where the service draws
// the line too.
func requireRecruiter(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := middleware.PrincipalFrom(r.Context())
		if !ok || p.Kind != service.PrincipalOrgUser || (!p.HasRole(service.RoleRecruiter) && !p.HasRole(service.RoleAdmin)) {
			http.Error(w, "recruiter role required", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
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
		Title: title, Surface: layout.SurfaceApp, Nav: layout.AppNav(p, jobsPrefix),
		UserRole: layout.RoleLabel(p), Menu: layout.AppMenu(p, jobsPrefix),
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

func (h *handlers) builder(w http.ResponseWriter, r *http.Request) {
	h.renderBuilder(w, r, http.StatusOK, nil)
}

// renderBuilder draws the builder over the job's newest packet: the sent one
// is shown as the record of what the client got, a draft as the working copy.
func (h *handlers) renderBuilder(w http.ResponseWriter, r *http.Request, status int, flashes []layout.Flash) {
	p, _ := middleware.PrincipalFrom(r.Context())
	jobID, ok := param(w, r, "jobID")
	if !ok {
		return
	}
	pool, err := h.d.Shortlists.Pool(r.Context(), p, jobID, minScore(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	packets, err := h.d.Shortlists.ListByJob(r.Context(), p, jobID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	view := builderView{JobID: jobID, Pool: pool, Packets: packets, Threshold: minScore(r)}
	config, err := json.Marshal(newIslandState(view))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, status, builderPage(h.page(r, "Shortlist builder", flashes...), view, string(config), middleware.CSRFToken(r)))
}

// minScore is the score the qualified pool starts at, from the query when the
// recruiter has widened or narrowed it, the service's default otherwise.
func minScore(r *http.Request) float64 {
	raw := strings.TrimSpace(r.URL.Query().Get("min_score"))
	if raw == "" {
		return service.DefaultShortlistMinScore
	}
	n, err := strconv.ParseFloat(raw, 64)
	if err != nil || n <= 0 {
		return service.DefaultShortlistMinScore
	}
	return n
}

func (h *handlers) save(w http.ResponseWriter, r *http.Request) {
	packet, err := h.savePacket(w, r)
	if err != nil {
		h.afterAction(w, r, err)
		return
	}
	if packet.ID == uuid.Nil {
		return
	}
	http.Redirect(w, r, Path(packet.JobID), http.StatusSeeOther)
}

// send saves what is on the screen and hands it to the client, so the packet
// the client reads is the one the recruiter was looking at.
func (h *handlers) send(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	packet, err := h.savePacket(w, r)
	if err != nil {
		h.afterAction(w, r, err)
		return
	}
	if packet.ID == uuid.Nil {
		return
	}
	if _, err := h.d.Shortlists.Send(r.Context(), p, packet.ID); err != nil {
		h.afterAction(w, r, err)
		return
	}
	http.Redirect(w, r, Path(packet.JobID), http.StatusSeeOther)
}

// savePacket writes the form as a draft. A zero packet id with a nil error
// means the request was already answered.
func (h *handlers) savePacket(w http.ResponseWriter, r *http.Request) (service.ShortlistPacket, error) {
	p, _ := middleware.PrincipalFrom(r.Context())
	jobID, ok := param(w, r, "jobID")
	if !ok {
		return service.ShortlistPacket{}, nil
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return service.ShortlistPacket{}, nil
	}
	var id *uuid.UUID
	if raw := r.PostFormValue("packet_id"); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			http.Error(w, "bad packet_id", http.StatusBadRequest)
			return service.ShortlistPacket{}, nil
		}
		id = &parsed
	}
	picks := make([]uuid.UUID, 0, len(r.PostForm["application_ids"]))
	for _, raw := range r.PostForm["application_ids"] {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			http.Error(w, "bad application_ids", http.StatusBadRequest)
			return service.ShortlistPacket{}, nil
		}
		picks = append(picks, parsed)
	}
	return h.d.Shortlists.Save(r.Context(), p, id, service.ShortlistInput{
		JobID: jobID, Note: r.PostFormValue("note"), ApplicationIDs: picks,
	})
}

// afterAction re-renders the builder carrying the refusal, or answers with
// the status when nothing on the screen could show it.
func (h *handlers) afterAction(w http.ResponseWriter, r *http.Request, err error) {
	status := statusFor(err)
	if status == http.StatusInternalServerError {
		h.fail(w, r, err)
		return
	}
	h.renderBuilder(w, r, status, []layout.Flash{{Kind: "error", Message: userMessage(err)}})
}

func (h *handlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	status := statusFor(err)
	if status == http.StatusInternalServerError && h.d.Logger != nil {
		h.d.Logger.ErrorContext(r.Context(), "shortlist builder failed", "path", r.URL.Path, "error", err)
	}
	http.Error(w, userMessage(err), status)
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, service.ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, service.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, service.ErrPacketSent):
		return http.StatusConflict
	case errors.Is(err, service.ErrTooManyPicks), errors.Is(err, service.ErrDuplicatePick),
		errors.Is(err, service.ErrNoPicks), errors.Is(err, service.ErrPickRejected):
		return http.StatusUnprocessableEntity
	}
	return http.StatusInternalServerError
}

func userMessage(err error) string {
	if statusFor(err) == http.StatusInternalServerError {
		return "Something went wrong. Try again."
	}
	return strings.TrimPrefix(err.Error(), "service: ")
}

func param(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		http.Error(w, "bad "+name, http.StatusBadRequest)
		return uuid.Nil, false
	}
	return id, true
}
