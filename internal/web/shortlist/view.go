package shortlist

import (
	"strconv"
	"time"

	"github.com/google/uuid"

	"recruiting/internal/service"
)

// builderView is everything the builder screen draws: the qualified pool at
// the threshold it was asked for, and the job's packets newest first.
type builderView struct {
	JobID     uuid.UUID
	Pool      []service.ShortlistCandidate
	Packets   []service.ShortlistPacket
	Threshold float64
}

// Draft is the packet the builder edits: the newest one still unsent. Nil
// means the recruiter is starting from nothing.
func (v builderView) Draft() *service.ShortlistPacket {
	for i := range v.Packets {
		if !v.Packets[i].Sent() {
			return &v.Packets[i]
		}
	}
	return nil
}

// Sent is the packets the client has already read, newest first.
func (v builderView) Sent() []service.ShortlistPacket {
	out := make([]service.ShortlistPacket, 0, len(v.Packets))
	for _, packet := range v.Packets {
		if packet.Sent() {
			out = append(out, packet)
		}
	}
	return out
}

// DraftID is the draft's id for the form's hidden field, empty when the save
// is to start a new packet.
func (v builderView) DraftID() string {
	if draft := v.Draft(); draft != nil {
		return draft.ID.String()
	}
	return ""
}

// DraftNote is what the note field starts with.
func (v builderView) DraftNote() string {
	if draft := v.Draft(); draft != nil {
		return draft.Note
	}
	return ""
}

// islandCandidate is one candidate as the picks column reads them. The
// column is rebuilt in the browser as the recruiter ranks, so both sides of
// the screen boot from the same list.
type islandCandidate struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Score string `json:"score"`
	Fit   string `json:"fit"`
}

// islandState is the builder's boot JSON: everyone who qualifies, whoever is
// already picked, and how many picks a packet carries.
type islandState struct {
	Pool  []islandCandidate `json:"pool"`
	Picks []islandCandidate `json:"picks"`
	Max   int               `json:"max"`
}

func newIslandState(v builderView) islandState {
	out := islandState{Pool: []islandCandidate{}, Picks: []islandCandidate{}, Max: service.MaxShortlistPicks}
	known := make(map[uuid.UUID]islandCandidate, len(v.Pool))
	for _, c := range v.Pool {
		entry := islandCandidate{ID: c.ApplicationID.String(), Name: c.CandidateName, Score: num(c.Score), Fit: num(c.Fit)}
		known[c.ApplicationID] = entry
		out.Pool = append(out.Pool, entry)
	}
	draft := v.Draft()
	if draft == nil {
		return out
	}
	for _, pick := range draft.Picks {
		entry, ok := known[pick.ApplicationID]
		if !ok {
			// Picked before the threshold moved, or before a later sitting
			// changed their score: keep them rather than drop them silently.
			entry = islandCandidate{ID: pick.ApplicationID.String(), Name: pick.CandidateName, Score: score(pick.Score)}
		}
		out.Picks = append(out.Picks, entry)
	}
	return out
}

// num renders a plain number without trailing zeroes.
func num(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// score renders a stored score, or an em dash when there is none.
func score(v *float64) string {
	if v == nil {
		return "—"
	}
	return num(*v)
}

func itoa(n int) string { return strconv.Itoa(n) }

func at(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2 Jan 2006, 15:04")
}

// sentAt is the moment a packet went out; a draft has none.
func sentAt(p service.ShortlistPacket) time.Time {
	if p.SentAt == nil {
		return time.Time{}
	}
	return *p.SentAt
}
