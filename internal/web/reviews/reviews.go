// Package reviews serves the vetter's assessment review screens: the queue
// of attempts waiting on them, the review page with the score, the integrity
// signals, and the replay island, and the summary the recruiter's
// application page embeds. Handlers call internal/service only.
package reviews

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"recruiting/internal/service"
	"recruiting/internal/web/layout"
	"recruiting/internal/web/middleware"
)

// Where the screens live. The summary hangs off the application the pipeline
// surface already owns.
const (
	Prefix            = "/app/reviews"
	ApplicationPrefix = "/app/applications"
)

// ManifestPrefix is where the replay island reads an attempt's manifest. It
// is the JSON API's own path, which the session cookie reaches.
const ManifestPrefix = "/api/v1/replay/"

// ReviewPath is the review screen for one attempt.
func ReviewPath(attemptID uuid.UUID) string { return Prefix + "/" + attemptID.String() }

// SummaryPath is the summary partial the application page loads.
func SummaryPath(applicationID uuid.UUID) string {
	return ApplicationPrefix + "/" + applicationID.String() + "/reviews"
}

// ManifestPath is the replay manifest for one attempt.
func ManifestPath(attemptID uuid.UUID) string { return ManifestPrefix + attemptID.String() }

// AttemptPrefix is where the per-attempt partials the replay island fetches
// live.
const AttemptPrefix = "/app/attempts"

// SnapshotsPath is the webcam frames of one attempt. The links it hands back
// are signed and short-lived, so they are minted when a reviewer asks for a
// frame rather than baked into the page.
func SnapshotsPath(attemptID uuid.UUID) string {
	return AttemptPrefix + "/" + attemptID.String() + "/snapshots"
}

// Deps is what Mount needs. Org supplies the signed-in user's display name.
type Deps struct {
	Reviews *service.ReviewService
	Org     *service.OrgService
	// Attempts serves the webcam frames behind the timeline's snapshot
	// markers. Nil serves none, and the markers open nothing.
	Attempts *service.AttemptService
	// Logger records the errors behind a 500; nil disables that logging.
	Logger *slog.Logger
}

// Mount registers the review screens on r. It expects the shared auth
// middleware to be installed already; the service decides who may read or
// write what. Routes are registered individually rather than as a subtree so
// the summary sits alongside the pipeline surface's application routes.
func Mount(r chi.Router, d Deps) {
	h := &handlers{d: d}
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth("/app/login"))
		r.Get(Prefix, h.queue)
		r.Get(Prefix+"/{attemptID}", h.review)
		r.Post(Prefix+"/{attemptID}", h.save)
		r.Get(ApplicationPrefix+"/{id}/reviews", h.summary)
		r.Get(AttemptPrefix+"/{attemptID}/snapshots", h.snapshots)
	})
}

type handlers struct{ d Deps }

func render(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = c.Render(r.Context(), w)
}

func (h *handlers) page(r *http.Request, title, nav string, flashes ...layout.Flash) layout.Page {
	p, _ := middleware.PrincipalFrom(r.Context())
	return layout.Page{
		Title: title, Surface: layout.SurfaceApp, Nav: layout.AppNav(p, nav), UserRole: layout.RoleLabel(p), Menu: layout.AppMenu(p, nav),
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

func (h *handlers) queue(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	rows, err := h.d.Reviews.Assignments(r.Context(), p)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, http.StatusOK, queuePage(h.page(r, "Reviews", Prefix), rows))
}

func (h *handlers) review(w http.ResponseWriter, r *http.Request) {
	h.renderReview(w, r, http.StatusOK, nil, nil)
}

// renderReview draws the review screen. A refused verdict comes back
// carrying what the vetter typed, so nothing has to be entered twice.
func (h *handlers) renderReview(w http.ResponseWriter, r *http.Request, status int, flashes []layout.Flash, submitted *service.ReviewInput) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := param(w, r, "attemptID")
	if !ok {
		return
	}
	detail, err := h.d.Reviews.Attempt(r.Context(), p, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if submitted != nil {
		filed := service.Review{Verdict: submitted.Verdict, Notes: submitted.Notes}
		if detail.Review != nil {
			filed.ID, filed.VetterName, filed.CreatedAt = detail.Review.ID, detail.Review.VetterName, detail.Review.CreatedAt
		}
		detail.Review = &filed
	}
	cfg, err := IslandConfig(id, service.ReplayJumps(detail.Submissions))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, status, reviewPage(h.page(r, "Review", Prefix, flashes...), detail, string(cfg), middleware.CSRFToken(r)))
}

// islandConfig is the JSON the replay island boots from; it fetches the
// manifest itself so the page stays small. The jump chips ride along because
// they are derived from the submissions, which the server already has and the
// manifest does not carry.
type islandConfig struct {
	AttemptID    string       `json:"attempt_id"`
	ManifestURL  string       `json:"manifest_url"`
	SnapshotsURL string       `json:"snapshots_url"`
	Jumps        []islandJump `json:"jumps"`
}

// islandJump is one chip: a label and the moment it seeks to, in epoch
// milliseconds, the same clock the manifest's events carry.
type islandJump struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	At    int64  `json:"at"`
}

// IslandConfig is the replay island's boot JSON for one attempt. Every page
// that mounts the island builds it here, so the viewer boots the same way
// wherever it is embedded.
func IslandConfig(attemptID uuid.UUID, jumps []service.ReplayJump) (string, error) {
	cfg := islandConfig{
		AttemptID: attemptID.String(), ManifestURL: ManifestPath(attemptID),
		SnapshotsURL: SnapshotsPath(attemptID), Jumps: []islandJump{},
	}
	for _, j := range jumps {
		cfg.Jumps = append(cfg.Jumps, islandJump{Key: j.Key, Label: j.Label, At: j.At.UnixMilli()})
	}
	b, err := json.Marshal(cfg)
	return string(b), err
}

// ConfigScript is the island's boot JSON as a whole script element, for a
// page outside this package that mounts the viewer.
func ConfigScript(id, body string) templ.Component { return configScript(id, body) }

func (h *handlers) save(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := param(w, r, "attemptID")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	in := service.ReviewInput{
		AttemptID: id,
		Verdict:   strings.TrimSpace(r.PostFormValue("verdict")),
		Notes:     r.PostFormValue("notes"),
	}
	if _, err := h.d.Reviews.Save(r.Context(), p, in); err != nil {
		status := statusFor(err)
		if status == http.StatusInternalServerError {
			h.fail(w, r, err)
			return
		}
		h.renderReview(w, r, status, []layout.Flash{{Kind: "error", Message: userMessage(err)}}, &in)
		return
	}
	http.Redirect(w, r, Prefix, http.StatusSeeOther)
}

// summary is the partial the application page pulls in: every attempt on the
// application with its score and verdict, with no chrome of its own.
func (h *handlers) summary(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := param(w, r, "id")
	if !ok {
		return
	}
	rows, err := h.d.Reviews.Summaries(r.Context(), p, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, http.StatusOK, SummaryFragment(rows))
}

// snapshots hands the island the frames behind the timeline's snapshot
// markers, each with a link that expires within service.SnapshotURLTTL. The
// service refuses anyone but the org's own users, so a client principal
// never reaches a frame.
func (h *handlers) snapshots(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := param(w, r, "attemptID")
	if !ok {
		return
	}
	if h.d.Attempts == nil {
		http.Error(w, "snapshots are not configured", http.StatusNotFound)
		return
	}
	shots, err := h.d.Attempts.Snapshots(r.Context(), p, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := make([]snapshotFrame, 0, len(shots))
	for _, shot := range shots {
		// The key is where the bytes live; naming it would hand a reader a
		// handle that outlives the signature.
		out = append(out, snapshotFrame{Seq: shot.Seq, TakenAt: shot.TakenAt.Unix() * 1000, URL: shot.URL})
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(out)
}

// snapshotFrame is one webcam beat as the island reads it.
type snapshotFrame struct {
	Seq     int    `json:"seq"`
	TakenAt int64  `json:"taken_at"`
	URL     string `json:"url"`
}

func (h *handlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	status := statusFor(err)
	if status == http.StatusInternalServerError && h.d.Logger != nil {
		h.d.Logger.ErrorContext(r.Context(), "review screen failed", "path", r.URL.Path, "error", err)
	}
	http.Error(w, userMessage(err), status)
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, service.ErrForbidden), errors.Is(err, service.ErrReviewFiled):
		return http.StatusForbidden
	case errors.Is(err, service.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, service.ErrBadVerdict), errors.Is(err, service.ErrNotScored):
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
