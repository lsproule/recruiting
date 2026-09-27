// Package signals computes the integrity signals of an assessment attempt
// from its recorded event stream. It does no I/O: the caller loads the
// stream, the submissions, and the problem metadata, and stores what comes
// back.
package signals

import (
	"encoding/json"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/google/uuid"
)

// Event is one recorded editor event, in the shape of the compacted stream.
type Event struct {
	Seq       int64           `json:"seq"`
	Kind      string          `json:"kind"`
	ProblemID *uuid.UUID      `json:"problem_id,omitempty"`
	ClientTs  *time.Time      `json:"client_ts,omitempty"`
	ServerTs  time.Time       `json:"server_ts"`
	Payload   json.RawMessage `json:"payload"`
}

// MaxClientSkew is how far the client clock may lead or trail the server
// clock; a client time further out is pulled back to that bound, so a
// wrong clock cannot place a paste hours before its submit.
const MaxClientSkew = 5 * time.Minute

// At is when the event happened: the client's clock, which orders edits at
// the resolution typing needs, clamped to the server's, or the server's
// when the client sent none.
func (e Event) At() time.Time {
	if e.ClientTs == nil {
		return e.ServerTs
	}
	switch at := *e.ClientTs; {
	case at.Before(e.ServerTs.Add(-MaxClientSkew)):
		return e.ServerTs.Add(-MaxClientSkew)
	case at.After(e.ServerTs.Add(MaxClientSkew)):
		return e.ServerTs.Add(MaxClientSkew)
	default:
		return at
	}
}

// PasteMatchWindow is how close to a paste event an edit inserting exactly
// the pasted length must land to be read as the paste itself rather than
// typing. The editor emits both for one paste, the changeset carrying the text.
const PasteMatchWindow = 2 * time.Second

// Edit is one changeset applied to a problem's editor. Counts are UTF-16
// units, as CodeMirror and the paste event measure text.
type Edit struct {
	Seq       int64
	At        time.Time
	Inserted  int
	Deleted   int
	FromPaste bool
}

// Paste is one paste event; Len is in UTF-16 units. Internal pastes moved text the candidate copied
// from the page's own editor and are not evidence of anything.
type Paste struct {
	Seq      int64
	At       time.Time
	Len      int
	Internal bool
}

// Blur is one interval the page was out of focus, ending at the next focus
// or, when there was none, at the last event of the problem.
type Blur struct {
	Seq  int64
	From time.Time
	To   time.Time
}

// Timeline is one problem's history, built once from the stream.
type Timeline struct {
	ProblemID uuid.UUID
	First     time.Time // first event on the problem, client clock
	// FirstServer is the same on the server clock, for durations measured
	// against other server times.
	FirstServer time.Time
	Last        time.Time
	Edits       []Edit
	Pastes      []Paste
	Blurs       []Blur
	// Source is the editor text replayed from the changesets, starting from
	// an empty document. Reconstructed is false once a changeset did not fit
	// the document it was applied to; Source is then unreliable and only the
	// per-edit counts are used.
	Source        string
	Reconstructed bool
}

// Build folds the stream into one timeline per problem. Events are taken in
// seq order regardless of the order given. initial is each problem's editor
// text before the first event (starter text, if any); a stream that starts
// with a retain over it reconstructs only from that. A caller that decodes
// the recording as it goes folds it into a Stream instead.
func Build(events []Event, initial map[uuid.UUID]string) map[uuid.UUID]*Timeline {
	st := NewStream(initial)
	for _, ev := range bySeq(events) {
		st.Add(ev)
	}
	return st.Timelines()
}

// markPastes attributes to each paste the nearest edit that carried its
// text, so the typing signals do not read a paste as a burst of keystrokes.
func markPastes(tl *Timeline) {
	for _, p := range tl.Pastes {
		if p.Len == 0 {
			continue
		}
		best := -1
		for i := range tl.Edits {
			e := &tl.Edits[i]
			if e.FromPaste || e.Inserted != p.Len {
				continue
			}
			d := e.At.Sub(p.At).Abs()
			if d <= PasteMatchWindow && (best < 0 || d < tl.Edits[best].At.Sub(p.At).Abs()) {
				best = i
			}
		}
		if best >= 0 {
			tl.Edits[best].FromPaste = true
		}
	}
}

// changeset is a CodeMirror ChangeSet as ChangeSet.toJSON() emits it: a
// list of sections over the old document, each either a number (keep that
// many characters) or an array whose first element is how many characters
// are replaced and whose remaining elements are the inserted lines, joined
// with newlines. The payload is accepted bare or under a "changes" key.
// Lengths are UTF-16 units, so an astral character counts two. A negative
// length, or a section that is neither form, rejects the whole changeset.
type changeset []section

type section struct {
	keep     int
	replaced int
	inserted []uint16
	isKeep   bool
}

func parseChangeset(raw json.RawMessage) (changeset, bool) {
	var wrapped struct {
		Changes json.RawMessage `json:"changes"`
	}
	body := raw
	if err := json.Unmarshal(raw, &wrapped); err == nil && len(wrapped.Changes) > 0 {
		body = wrapped.Changes
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(body, &parts); err != nil || parts == nil {
		return nil, false
	}
	cs := make(changeset, 0, len(parts))
	for _, part := range parts {
		var n int
		if err := json.Unmarshal(part, &n); err == nil {
			if n < 0 {
				return nil, false
			}
			cs = append(cs, section{keep: n, isKeep: true})
			continue
		}
		var arr []json.RawMessage
		if err := json.Unmarshal(part, &arr); err != nil || len(arr) == 0 {
			return nil, false
		}
		var s section
		if err := json.Unmarshal(arr[0], &s.replaced); err != nil || s.replaced < 0 {
			return nil, false
		}
		lines := make([]string, 0, len(arr)-1)
		for _, l := range arr[1:] {
			var line string
			if err := json.Unmarshal(l, &line); err != nil {
				return nil, false
			}
			lines = append(lines, line)
		}
		s.inserted = utf16.Encode([]rune(strings.Join(lines, "\n")))
		cs = append(cs, s)
	}
	return cs, true
}

// apply replays the changeset over doc. It reports the inserted and
// deleted units, and the new document when the sections covered exactly the
// old one; a section reaching past the document fails the whole edit.
func (cs changeset) apply(doc []uint16) (inserted, deleted int, next []uint16, applied bool) {
	next = make([]uint16, 0, len(doc))
	pos := 0
	applied = true
	for _, s := range cs {
		if s.isKeep {
			if end := pos + s.keep; end >= pos && end <= len(doc) {
				next = append(next, doc[pos:end]...)
			} else {
				applied = false
			}
			pos += s.keep
			continue
		}
		deleted += s.replaced
		inserted += len(s.inserted)
		next = append(next, s.inserted...)
		pos += s.replaced
	}
	if pos != len(doc) {
		applied = false
	}
	if !applied {
		return inserted, deleted, nil, false
	}
	return inserted, deleted, next, true
}
