package client

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/a-h/templ"
	"github.com/google/uuid"

	"recruiting/internal/service"
)

// The client's replay manifest is its own type rather than the reviewer's.
// A field a client may not see cannot be added to it by accident, because
// adding one here is the only way it could ever be written.

// clientReplayEvent is one recorded event. At is Unix milliseconds on the
// client clock, clamped to the server's.
type clientReplayEvent struct {
	Seq       int64           `json:"seq"`
	Kind      string          `json:"kind"`
	ProblemID string          `json:"problem_id,omitempty"`
	At        int64           `json:"at"`
	Payload   json.RawMessage `json:"payload"`
}

// clientReplayMarker is a point on the timeline the viewer offers to jump
// to. Only runs and submits survive the service's filter, so nothing here
// can point at a paste, a blur, or a webcam beat.
type clientReplayMarker struct {
	Seq  int64  `json:"seq"`
	Kind string `json:"kind"`
	At   int64  `json:"at"`
	Note string `json:"note"`
}

// clientReplayProblem is one editor of the replay.
type clientReplayProblem struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	Language      string `json:"language"`
	InitialSource string `json:"initial_source"`
	FinalSource   string `json:"final_source"`
}

// clientReplayManifest is one page of the sitting as a client reads it.
type clientReplayManifest struct {
	AttemptID     uuid.UUID             `json:"attempt_id"`
	StartedAt     int64                 `json:"started_at"`
	FinishedAt    int64                 `json:"finished_at"`
	SnapshotEvery int                   `json:"snapshot_every"`
	Problems      []clientReplayProblem `json:"problems"`
	Events        []clientReplayEvent   `json:"events"`
	Markers       []clientReplayMarker  `json:"markers"`
	NextAfterSeq  int64                 `json:"next_after_seq"`
}

// clientIslandConfig is the boot JSON of the read-only viewer. It names no
// snapshots endpoint: the frames behind a webcam beat are the org's.
type clientIslandConfig struct {
	AttemptID   string `json:"attempt_id"`
	ManifestURL string `json:"manifest_url"`
	ReadOnly    bool   `json:"readonly"`
}

func replayManifest(rep service.Replay) clientReplayManifest {
	out := clientReplayManifest{
		AttemptID: rep.AttemptID, SnapshotEvery: rep.SnapshotEvery, NextAfterSeq: rep.NextAfterSeq,
		Problems: make([]clientReplayProblem, 0, len(rep.Problems)),
		Events:   make([]clientReplayEvent, 0, len(rep.Events)),
		Markers:  make([]clientReplayMarker, 0, len(rep.Markers)),
	}
	if !rep.StartedAt.IsZero() {
		out.StartedAt = rep.StartedAt.UnixMilli()
	}
	if !rep.FinishedAt.IsZero() {
		out.FinishedAt = rep.FinishedAt.UnixMilli()
	}
	for _, p := range rep.Problems {
		out.Problems = append(out.Problems, clientReplayProblem{
			ID: p.ID.String(), Title: p.Title, Language: p.Language,
			InitialSource: p.InitialSource, FinalSource: p.FinalSource,
		})
	}
	for _, ev := range rep.Events {
		e := clientReplayEvent{Seq: ev.Seq, Kind: ev.Kind, At: service.RecordedEventAt(ev).UnixMilli(), Payload: ev.Payload}
		if ev.ProblemID != nil {
			e.ProblemID = ev.ProblemID.String()
		}
		out.Events = append(out.Events, e)
	}
	for _, m := range rep.Markers {
		out.Markers = append(out.Markers, clientReplayMarker{Seq: m.Seq, Kind: m.Kind, At: m.At.UnixMilli(), Note: m.Note})
	}
	return out
}

// replayConfig is the island's boot JSON for one pick.
func replayConfig(jobID uuid.UUID, d service.ClientShortlistDetail) (string, error) {
	b, err := json.Marshal(clientIslandConfig{
		AttemptID:   d.AttemptID.String(),
		ManifestURL: ShortlistReplayPath(jobID, d.Pick.ApplicationID),
		ReadOnly:    true,
	})
	return string(b), err
}

// replayQuery is the page the island asked for, bounded by the service.
func replayQuery(r *http.Request) service.ReplayQuery {
	var q service.ReplayQuery
	if n, err := strconv.ParseInt(r.URL.Query().Get("after_seq"), 10, 64); err == nil && n > 0 {
		q.AfterSeq = n
	}
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
		q.Limit = n
	}
	return q
}

// ConfigScript is the island's boot JSON as a whole script element. templ
// treats the content of a script element as raw text, so the element is
// written here rather than interpolated inside one in a template.
func ConfigScript(id, body string) templ.Component {
	return templ.Raw(`<script id="` + id + `" type="application/json">` + jsonForScript(body) + `</script>`)
}

// jsonForScript makes a JSON document safe to inline in a script element.
// Escaping every "<" is what makes that true whatever follows it, and the
// escape is JSON's own, so the document still parses. The line separators
// are escaped because they end a statement in JavaScript but not in JSON.
func jsonForScript(s string) string {
	r := strings.NewReplacer("<", "\\u003c", " ", "\\u2028", " ", "\\u2029")
	return r.Replace(s)
}

// testName is a case's own name, or its position when it has none.
func testName(t service.ClientTest) string {
	if t.Name != "" {
		return t.Name
	}
	return "Case " + strconv.Itoa(t.Position)
}

// testStatus reads a case's outcome back, with a dash for one the runner
// never reached.
func testStatus(t service.ClientTest) string {
	if t.Status == "" {
		return "—"
	}
	return t.Status
}
