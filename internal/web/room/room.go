// Package room serves a live interview room: the page that mounts the video
// and shared-editor island, and the small API behind it — an event stream,
// signalling relay, editor updates, saved code, and a run. The same handlers
// serve an org user at /app/rooms/{kind}/{id} and a candidate under their
// own link. Handlers call internal/service only.
package room

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"recruiting/internal/service"
	"recruiting/internal/web/layout"
	"recruiting/internal/web/middleware"
)

// AppPrefix is where org users open rooms.
const AppPrefix = "/app/rooms"

// SlotPath is an org user's way into a booked interview's room.
func SlotPath(slotID uuid.UUID) string { return AppPrefix + "/slot/" + slotID.String() }

// PairingPath is an org user's way into one sprint conversation's room.
func PairingPath(pairingID uuid.UUID) string { return AppPrefix + "/pairing/" + pairingID.String() }

// keepalive is how often the stream carries a comment so proxies and the
// browser keep it open through a silence.
const keepalive = 15 * time.Second

// maxBody bounds a signalling, doc, or code post.
const maxBody = 1 << 20

// Deps is what Mount needs. ICEServers is the JSON array the browser hands
// to RTCPeerConnection; empty means host candidates only.
type Deps struct {
	Rooms      *service.RoomService
	Org        *service.OrgService
	ICEServers string
	// Logger records the errors behind a 500; nil disables that logging.
	Logger *slog.Logger
}

// Mount registers the org user's rooms on r. It expects the shared auth
// middleware to be installed already.
func Mount(r chi.Router, d Deps) {
	h := &handlers{d: d}
	r.Route(AppPrefix+"/{kind}/{id}", func(r chi.Router) {
		r.Use(middleware.RequireAuth("/app/login"))
		r.Get("/", h.page(keyFromPath, appBase))
		h.api(r, keyFromPath)
	})
}

// MountCandidate registers a room of a fixed kind on r, where r is already
// scoped by the candidate's link and the {roomID} path parameter names the
// subject. The page and the API share the link's prefix.
func MountCandidate(r chi.Router, kind string, d Deps) {
	h := &handlers{d: d}
	key := func(r *http.Request) (service.RoomKey, error) {
		return service.ParseRoomKey(kind, chi.URLParam(r, "roomID"))
	}
	r.Get("/", h.page(key, candidateBase))
	h.api(r, key)
}

// keyFn reads the room a request addresses.
type keyFn func(*http.Request) (service.RoomKey, error)

func keyFromPath(r *http.Request) (service.RoomKey, error) {
	return service.ParseRoomKey(chi.URLParam(r, "kind"), chi.URLParam(r, "id"))
}

// baseFn is the API base for the island: the page's own URL, which is also
// where the stream and the posts live.
type baseFn func(*http.Request) string

func appBase(r *http.Request) string {
	return AppPrefix + "/" + chi.URLParam(r, "kind") + "/" + chi.URLParam(r, "id")
}

func candidateBase(r *http.Request) string { return strings.TrimSuffix(r.URL.Path, "/") }

type handlers struct{ d Deps }

func (h *handlers) api(r chi.Router, key keyFn) {
	r.Get("/events", h.events(key))
	r.Post("/signal", h.signal(key))
	r.Post("/doc", h.doc(key))
	r.Post("/code", h.code(key))
	r.Post("/run", h.run(key))
}

func render(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = c.Render(r.Context(), w)
}

func (h *handlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	status := statusFor(err)
	if status == http.StatusInternalServerError && h.d.Logger != nil {
		h.d.Logger.ErrorContext(r.Context(), "room failed", "path", r.URL.Path, "error", err)
	}
	http.Error(w, userMessage(err), status)
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, service.ErrForbidden), errors.Is(err, service.ErrNotPeer):
		return http.StatusForbidden
	case errors.Is(err, service.ErrNotFound), errors.Is(err, service.ErrRoomKind):
		return http.StatusNotFound
	case errors.Is(err, service.ErrRoomClosed), errors.Is(err, service.ErrSprintNotScheduled):
		return http.StatusConflict
	case errors.Is(err, service.ErrRoomLanguage):
		return http.StatusUnprocessableEntity
	case errors.Is(err, service.ErrSourceTooBig):
		return http.StatusRequestEntityTooLarge
	case errors.Is(err, service.ErrNoExecutor):
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
}

func userMessage(err error) string {
	if statusFor(err) == http.StatusInternalServerError {
		return "Something went wrong. Try again."
	}
	return strings.TrimPrefix(err.Error(), "service: ")
}

// page draws the room, or the waiting page when it is not open yet.
func (h *handlers) page(key keyFn, base baseFn) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, _ := middleware.PrincipalFrom(r.Context())
		k, err := key(r)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		room, err := h.d.Rooms.Open(r.Context(), p, k)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		v := pageView{Room: room, Base: base(r), Back: backLink(p, room), Now: time.Now(), Embed: r.URL.Query().Get("embed") == "1"}
		if !room.Open {
			render(w, r, http.StatusOK, waitingPage(v))
			return
		}
		cfg, err := islandConfig(room, v.Base, middleware.CSRFToken(r), h.d.ICEServers, h.d.Rooms != nil)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		v.Config = cfg
		render(w, r, http.StatusOK, roomPage(v))
	}
}

// backLink is where Leave goes: the application for an org user, nowhere
// for a candidate (their link page reloads itself).
func backLink(p service.Principal, room service.Room) string {
	if p.Kind != service.PrincipalOrgUser {
		return ""
	}
	if room.SprintID != uuid.Nil {
		return "/app/sprints/" + room.SprintID.String() + "/console"
	}
	return "/app/applications/" + room.ApplicationID.String()
}

// events is the room's stream. The first event is hello; the connection
// stays until the client leaves or the request ends.
func (h *handlers) events(key keyFn) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, _ := middleware.PrincipalFrom(r.Context())
		k, err := key(r)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		_, member, err := h.d.Rooms.Join(r.Context(), p, k)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		defer member.Leave()

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		rc := http.NewResponseController(w)
		write := func(ev service.RoomEvent) bool {
			body, err := json.Marshal(ev)
			if err != nil {
				return false
			}
			if _, err := io.WriteString(w, "event: "+ev.Type+"\ndata: "+string(body)+"\n\n"); err != nil {
				return false
			}
			return rc.Flush() == nil
		}
		ticker := time.NewTicker(keepalive)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case ev, ok := <-member.Events:
				if !ok || !write(ev) {
					return
				}
			case <-ticker.C:
				if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil || rc.Flush() != nil {
					return
				}
			}
		}
	}
}

// signalBody is one relayed message: who it is from and for, and the
// payload the server never reads.
type signalBody struct {
	From string          `json:"from"`
	To   string          `json:"to"`
	Data json.RawMessage `json:"data"`
}

func (h *handlers) signal(key keyFn) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, _ := middleware.PrincipalFrom(r.Context())
		k, err := key(r)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		var body signalBody
		if err := decode(r, &body); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		if err := h.d.Rooms.Signal(r.Context(), p, k, body.From, body.To, body.Data); err != nil {
			h.fail(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

type docBody struct {
	From     string `json:"from"`
	Update   string `json:"update"` // base64
	Snapshot bool   `json:"snapshot"`
}

func (h *handlers) doc(key keyFn) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, _ := middleware.PrincipalFrom(r.Context())
		k, err := key(r)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		var body docBody
		if err := decode(r, &body); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		update, err := base64.StdEncoding.DecodeString(body.Update)
		if err != nil || len(update) == 0 {
			http.Error(w, "bad update", http.StatusBadRequest)
			return
		}
		want, err := h.d.Rooms.Doc(r.Context(), p, k, body.From, update, body.Snapshot)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		writeJSON(w, map[string]bool{"snapshot_wanted": want})
	}
}

type codeBody struct {
	From     string `json:"from"`
	Language string `json:"language"`
	Source   string `json:"source"`
}

func (h *handlers) code(key keyFn) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, _ := middleware.PrincipalFrom(r.Context())
		k, err := key(r)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		var body codeBody
		if err := decode(r, &body); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		if err := h.d.Rooms.SaveCode(r.Context(), p, k, body.From, body.Language, body.Source); err != nil {
			h.fail(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

type runBody struct {
	Language string `json:"language"`
	Source   string `json:"source"`
	Stdin    string `json:"stdin"`
}

func (h *handlers) run(key keyFn) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, _ := middleware.PrincipalFrom(r.Context())
		k, err := key(r)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		var body runBody
		if err := decode(r, &body); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		res, err := h.d.Rooms.Run(r.Context(), p, k, service.RunInput{Language: body.Language, Source: body.Source, Stdin: body.Stdin})
		if err != nil {
			h.fail(w, r, err)
			return
		}
		writeJSON(w, map[string]any{
			"status": res.Status, "compile_output": res.CompileOutput,
			"stdout": res.Stdout, "stderr": res.Stderr, "time_ms": res.TimeMs,
		})
	}
}

func decode(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(v)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// islandConfig is the boot JSON the room island reads.
func islandConfig(room service.Room, base, csrf, ice string, canRun bool) (string, error) {
	var iceServers json.RawMessage = json.RawMessage("[]")
	if strings.TrimSpace(ice) != "" && json.Valid([]byte(ice)) {
		iceServers = json.RawMessage(ice)
	}
	cfg := map[string]any{
		"key":       room.Key.String(),
		"base":      base,
		"csrf":      csrf,
		"name":      room.Name,
		"role":      room.Role,
		"title":     room.Title,
		"subtitle":  room.Subtitle,
		"language":  room.Language,
		"source":    room.Source,
		"languages": room.Languages,
		"ice":       iceServers,
		"run":       canRun,
		"closes_at": room.ClosesAt.UTC().UnixMilli(),
	}
	if room.PairingID != uuid.Nil {
		cfg["pairing"] = room.PairingID.String()
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ConfigScript writes an island's boot JSON as a whole script element;
// templ treats script bodies as raw text, so the element is built here.
func ConfigScript(id, body string) templ.Component {
	r := strings.NewReplacer("<", `<`, " ", ` `, " ", ` `)
	return templ.Raw(`<script id="` + id + `" type="application/json">` + r.Replace(body) + `</script>`)
}

// Page is the chrome a room draws: no navigation, the room's own header.
func Page(title string) layout.Page { return layout.Page{Title: title} }
