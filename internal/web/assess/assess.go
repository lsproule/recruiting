// Package assess serves the candidate's assessment session at /assess/ and
// the recruiter's assessment screens at /app/assessments. Handlers call
// internal/service only.
package assess

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
	"recruiting/internal/web/markdown"
	"recruiting/internal/web/middleware"
)

// SessionPath is the candidate page; auth's /assess/{token} redirects here
// once the link is swapped for the sealed cookie.
const SessionPath = "/assess/"

// APIBase is where the island reaches the attempt operations. It sits under
// /assess/ because the sealed cookie is scoped to that path: the browser
// would never send it to /api/v1. Requests are re-addressed to apiPrefix
// and handed to the outer mux, so /assess/api/attempts/{id} becomes
// /api/v1/attempts/{id} there.
const APIBase = SessionPath + "api"

// apiPrefix is where the JSON API is mounted on the outer mux.
const apiPrefix = "/api/v1"

// ConsentPath takes the consent screen's answer. Agreeing records the
// consent and starts the session in one step, so the island can store the
// photo of an ID against a sitting that has begun.
const ConsentPath = SessionPath + "consent"

// BeaconPath takes the island's last event batch as a form post, which is
// what navigator.sendBeacon can send along with the CSRF field.
const BeaconPath = SessionPath + "events"

// Deps is what Mount needs. Assessment is the cookie middleware from
// (*auth.Auth).Assessment(), built from the same seal key as the entry
// route. API is the outer mux the JSON API is mounted on ((*api.Router).Mux);
// it receives re-addressed requests with the principal already in context.
type Deps struct {
	Attempts   *service.AttemptService
	Assessment func(http.Handler) http.Handler
	API        http.Handler
	Logger     *slog.Logger
}

// Mount registers the candidate session routes on r. Routes are registered
// directly rather than as a sub-router so they sit beside auth's
// /assess/{token} entry without a mount conflict. r carries the HTML
// surface's CSRF middleware, so the island sends the token as a header.
func Mount(r chi.Router, d Deps) {
	h := &handlers{d: d}
	r.With(d.Assessment).Get(SessionPath, h.session)
	r.With(d.Assessment).Post(SessionPath+"start", h.start)
	r.With(d.Assessment).Post(ConsentPath, h.consent)
	r.With(d.Assessment).Post(BeaconPath, h.beacon)
	if d.API != nil {
		r.With(d.Assessment).Handle(APIBase+"/*", forwardAPI(d.API))
	}
}

// forwardAPI hands the request to the outer mux as if it had arrived at
// /api/v1 + rest. chi routes by the route context's remaining path when one
// is set, so that is cleared along with rewriting the URL.
func forwardAPI(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, APIBase)
		r2 := r.Clone(r.Context())
		r2.URL.Path = apiPrefix + rest
		if r.URL.RawPath != "" {
			r2.URL.RawPath = apiPrefix + strings.TrimPrefix(r.URL.RawPath, APIBase)
		}
		r2.RequestURI = r2.URL.RequestURI()
		if rctx := chi.RouteContext(r2.Context()); rctx != nil {
			rctx.RoutePath = ""
		}
		next.ServeHTTP(w, r2)
	})
}

type handlers struct{ d Deps }

func render(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = c.Render(r.Context(), w)
}

func (h *handlers) session(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	s, err := h.d.Attempts.Session(r.Context(), p)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.renderSession(w, r, s)
}

func (h *handlers) start(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	switch _, err := h.d.Attempts.Start(r.Context(), p); {
	case err == nil, errors.Is(err, service.ErrAttemptClosed):
	case errors.Is(err, service.ErrConsentRequired):
		// The session records something; the consent screen is where the
		// candidate answers for it.
	default:
		h.fail(w, r, err)
		return
	}
	http.Redirect(w, r, SessionPath, http.StatusSeeOther)
}

// consent takes the one answer the consent screen asks for. Agreeing starts
// the session; declining leaves the invite alone and tells the recruiter.
func (h *handlers) consent(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	if r.PostFormValue("decision") == "decline" {
		if err := h.d.Attempts.Decline(r.Context(), p); err != nil {
			h.fail(w, r, err)
			return
		}
		render(w, r, http.StatusOK, closedPage("Assessment declined",
			"Nothing was recorded and the timer never started. Your recruiter has been told, and can send you a new invite."))
		return
	}
	if err := h.d.Attempts.Consent(r.Context(), p); err != nil {
		h.fail(w, r, err)
		return
	}
	h.start(w, r)
}

func (h *handlers) renderSession(w http.ResponseWriter, r *http.Request, s service.AttemptSession) {
	c := configFor(s)
	c.CSRF = middleware.CSRFToken(r)
	if s.Attempt.Status == service.AttemptInvited {
		// The consent screen boots from this too, and no problem is the
		// candidate's to read before the clock starts.
		c.Problems = []islandProblem{}
	}
	cfg, err := json.Marshal(c)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, http.StatusOK, sessionPage(s, string(cfg), middleware.CSRFToken(r)))
}

// islandConfig is the JSON the island boots from. Only what the candidate
// may see is included: hidden cases never leave the server.
type islandConfig struct {
	// Mode tells the island which surface it is on; the candidate page is
	// always an attempt, with the recorder and the timer running.
	Mode      string          `json:"mode"`
	AttemptID string          `json:"attempt_id"`
	APIBase   string          `json:"api_base"`
	BeaconURL string          `json:"beacon_url"`
	CSRF      string          `json:"csrf"`
	Status    string          `json:"status"`
	ExpiresAt int64           `json:"expires_at"`
	Integrity islandIntegrity `json:"integrity"`
	Problems  []islandProblem `json:"problems"`
}

// islandIntegrity is the assessment's integrity settings plus where the
// session posts what they produce: the webcam beats and the consent step's
// photo of an ID.
type islandIntegrity struct {
	Fullscreen  bool   `json:"fullscreen"`
	BlockPaste  bool   `json:"block_paste"`
	Webcam      bool   `json:"webcam"`
	WebcamEvery int    `json:"webcam_interval_s"`
	PhotoID     bool   `json:"photo_id"`
	SnapshotURL string `json:"snapshot_url"`
	ConsentURL  string `json:"consent_url"`
}

type islandProblem struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Kind      string `json:"kind"`
	Statement string `json:"statement"`
	// StatementHTML is the statement rendered from Markdown on the server;
	// the island has no Markdown parser and shows this instead.
	StatementHTML string       `json:"statement_html"`
	Languages     []string     `json:"languages"`
	Language      string       `json:"language"`
	Source        string       `json:"source"`
	SQLSchema     string       `json:"sql_schema"`
	PublicTests   []islandTest `json:"public_tests"`
}

type islandTest struct {
	Input    string `json:"input"`
	Expected string `json:"expected"`
}

func configFor(s service.AttemptSession) islandConfig {
	integrity := s.Assessment.Integrity
	out := islandConfig{
		Mode: "attempt", AttemptID: s.Attempt.ID.String(), APIBase: APIBase, BeaconURL: BeaconPath,
		Status: s.Attempt.Status, Problems: []islandProblem{},
		Integrity: islandIntegrity{
			Fullscreen: integrity.Fullscreen, BlockPaste: integrity.BlockPaste, Webcam: integrity.Webcam,
			WebcamEvery: integrity.WebcamEvery, PhotoID: integrity.PhotoID,
			SnapshotURL: APIBase + "/attempts/" + s.Attempt.ID.String() + "/snapshots",
			ConsentURL:  APIBase + "/attempts/" + s.Attempt.ID.String() + "/identity",
		},
	}
	if !s.Attempt.ExpiresAt.IsZero() {
		out.ExpiresAt = s.Attempt.ExpiresAt.UnixMilli()
	}
	for _, p := range s.Problems {
		ip := islandProblem{
			ID: p.ID.String(), Title: p.Title, Kind: p.Kind, Statement: p.Statement,
			StatementHTML: markdown.ToHTML(p.Statement), Languages: p.Languages,
			Language: p.Language, Source: p.Source, SQLSchema: p.SQLSchema, PublicTests: []islandTest{},
		}
		if ip.Languages == nil {
			ip.Languages = []string{}
		}
		for _, t := range p.PublicTests {
			ip.PublicTests = append(ip.PublicTests, islandTest{Input: t.Input, Expected: t.Expected})
		}
		out.Problems = append(out.Problems, ip)
	}
	return out
}

// beacon ingests a batch posted as a form: the CSRF field and the JSON
// batch in "batch". It answers like the API so the island treats both the
// same; the body is the batch's last seq or a problem detail.
func (h *handlers) beacon(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	var batch struct {
		AttemptID uuid.UUID              `json:"attempt_id"`
		Events    []service.AttemptEvent `json:"events"`
	}
	if err := json.Unmarshal([]byte(r.PostFormValue("batch")), &batch); err != nil {
		http.Error(w, "batch is not JSON", http.StatusUnprocessableEntity)
		return
	}
	last, err := h.d.Attempts.RecordEvents(r.Context(), p, batch.AttemptID, batch.Events)
	if err != nil {
		status := http.StatusUnprocessableEntity
		switch {
		case errors.Is(err, service.ErrEventSeq):
			status = http.StatusConflict
		case errors.Is(err, service.ErrAttemptExpired), errors.Is(err, service.ErrInviteExpired):
			status = http.StatusGone
		case errors.Is(err, service.ErrNotFound), errors.Is(err, service.ErrForbidden):
			status = http.StatusNotFound
		case statusOf(err) == http.StatusInternalServerError:
			if h.d.Logger != nil {
				h.d.Logger.ErrorContext(r.Context(), "beacon ingest failed", "error", err)
			}
			status = http.StatusInternalServerError
		}
		http.Error(w, userMessage(err), status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]int64{"last_seq": last})
}

// statusOf tells an expected refusal from a failure.
func statusOf(err error) int {
	for _, known := range []error{service.ErrEventInvalid, service.ErrEventKind, service.ErrAttemptClosed, service.ErrAttemptNotStarted} {
		if errors.Is(err, known) {
			return http.StatusUnprocessableEntity
		}
	}
	return http.StatusInternalServerError
}

// fail answers a closed or expired attempt with its own page and anything
// unexpected with a generic 500, logging the cause.
func (h *handlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, service.ErrAttemptExpired):
		render(w, r, http.StatusGone, closedPage("Time is up", "The assessment time has run out. The code you had synced was submitted for review."))
	case errors.Is(err, service.ErrInviteExpired):
		render(w, r, http.StatusGone, closedPage("Invite expired", "This assessment invite is no longer valid. Please contact the recruiter for a new one."))
	case errors.Is(err, service.ErrAttemptClosed):
		render(w, r, http.StatusOK, closedPage("Assessment submitted", "Thank you. Your work has been submitted for review."))
	case errors.Is(err, service.ErrForbidden), errors.Is(err, service.ErrNotFound):
		render(w, r, http.StatusNotFound, closedPage("Not found", "There is no assessment session for this link."))
	default:
		if h.d.Logger != nil {
			h.d.Logger.ErrorContext(r.Context(), "assessment session failed", "path", r.URL.Path, "error", err)
		}
		http.Error(w, "Something went wrong. Try again.", http.StatusInternalServerError)
	}
}

func userMessage(err error) string {
	return strings.TrimPrefix(err.Error(), "service: ")
}
