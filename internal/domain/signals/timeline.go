// Package signals computes the integrity signals of an assessment attempt
// from its recorded event stream. It does no I/O: the caller loads the
// stream, the submissions, and the problem metadata, and stores what comes
// back.
package signals

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

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

// At is when the event happened: the client's clock, which orders edits at
// the resolution typing needs, or the server's when the client sent none.
func (e Event) At() time.Time {
	if e.ClientTs != nil {
		return *e.ClientTs
	}
	return e.ServerTs
}

// PasteMatchWindow is how close to a paste event an edit inserting exactly
// the pasted length must land to be read as the paste itself rather than
// typing. The editor emits both for one paste, the changeset carrying the text.
const PasteMatchWindow = 2 * time.Second

// Edit is one changeset applied to a problem's editor.
type Edit struct {
	Seq       int64
	At        time.Time
	Inserted  int
	Deleted   int
	FromPaste bool
}

// Paste is one paste event. Internal pastes moved text the candidate copied
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
	First     time.Time // first event on the problem
	Last      time.Time
	Edits     []Edit
	Pastes    []Paste
	Blurs     []Blur
	// Source is the editor text replayed from the changesets, starting from
	// an empty document. Reconstructed is false once a changeset did not fit
	// the document it was applied to; Source is then unreliable and only the
	// per-edit counts are used.
	Source        string
	Reconstructed bool
}

// Build folds the stream into one timeline per problem. Events are taken in
// seq order regardless of the order given.
func Build(events []Event) map[uuid.UUID]*Timeline {
	sorted := make([]Event, len(events))
	copy(sorted, events)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Seq < sorted[j].Seq })

	out := map[uuid.UUID]*Timeline{}
	docs := map[uuid.UUID]*strings.Builder{}
	open := map[uuid.UUID]int{} // index of the unclosed blur
	for _, ev := range sorted {
		if ev.ProblemID == nil {
			continue
		}
		id := *ev.ProblemID
		tl := out[id]
		if tl == nil {
			tl = &Timeline{ProblemID: id, First: ev.At(), Reconstructed: true}
			out[id] = tl
			docs[id] = &strings.Builder{}
			open[id] = -1
		}
		at := ev.At()
		tl.Last = at
		switch ev.Kind {
		case "edit":
			cs, ok := parseChangeset(ev.Payload)
			if !ok {
				tl.Reconstructed = false
				continue
			}
			ins, del, next, applied := cs.apply(docs[id].String())
			if applied {
				docs[id].Reset()
				docs[id].WriteString(next)
			} else {
				tl.Reconstructed = false
			}
			tl.Edits = append(tl.Edits, Edit{Seq: ev.Seq, At: at, Inserted: ins, Deleted: del})
		case "paste":
			var p struct {
				Len      int  `json:"len"`
				Internal bool `json:"internal"`
			}
			_ = json.Unmarshal(ev.Payload, &p)
			tl.Pastes = append(tl.Pastes, Paste{Seq: ev.Seq, At: at, Len: p.Len, Internal: p.Internal})
		case "blur":
			if open[id] < 0 {
				tl.Blurs = append(tl.Blurs, Blur{Seq: ev.Seq, From: at, To: at})
				open[id] = len(tl.Blurs) - 1
			}
		case "focus":
			if i := open[id]; i >= 0 {
				tl.Blurs[i].To = at
				open[id] = -1
			}
		}
	}
	for id, tl := range out {
		if i := open[id]; i >= 0 {
			tl.Blurs[i].To = tl.Last
		}
		tl.Source = docs[id].String()
		markPastes(tl)
	}
	return out
}

// markPastes attributes to the paste the edit that carried its text, so the
// typing signals do not read a paste as a burst of keystrokes.
func markPastes(tl *Timeline) {
	for _, p := range tl.Pastes {
		if p.Len == 0 {
			continue
		}
		for i := range tl.Edits {
			e := &tl.Edits[i]
			if e.FromPaste || e.Inserted != p.Len {
				continue
			}
			if d := e.At.Sub(p.At); d >= -PasteMatchWindow && d <= PasteMatchWindow {
				e.FromPaste = true
				break
			}
		}
	}
}

// changeset is a CodeMirror ChangeSet as ChangeSet.toJSON() emits it: a
// list of sections over the old document, each either a number (keep that
// many characters) or an array whose first element is how many characters
// are replaced and whose remaining elements are the inserted lines, joined
// with newlines. The payload is accepted bare or under a "changes" key.
type changeset []section

type section struct {
	keep     int
	replaced int
	inserted string
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
	if err := json.Unmarshal(body, &parts); err != nil {
		return nil, false
	}
	cs := make(changeset, 0, len(parts))
	for _, part := range parts {
		var n int
		if err := json.Unmarshal(part, &n); err == nil {
			cs = append(cs, section{keep: n, isKeep: true})
			continue
		}
		var arr []json.RawMessage
		if err := json.Unmarshal(part, &arr); err != nil || len(arr) == 0 {
			return nil, false
		}
		var s section
		if err := json.Unmarshal(arr[0], &s.replaced); err != nil {
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
		s.inserted = strings.Join(lines, "\n")
		cs = append(cs, s)
	}
	return cs, true
}

// apply replays the changeset over doc, in UTF-16 units as CodeMirror
// counts them, which the stored source approximates with runes. It reports
// the inserted and deleted counts, and the new document when the sections
// covered exactly the old one.
func (cs changeset) apply(doc string) (inserted, deleted int, next string, applied bool) {
	runes := []rune(doc)
	var b strings.Builder
	pos := 0
	applied = true
	for _, s := range cs {
		if s.isKeep {
			if end := pos + s.keep; end <= len(runes) {
				b.WriteString(string(runes[pos:end]))
			} else {
				applied = false
			}
			pos += s.keep
			continue
		}
		deleted += s.replaced
		inserted += len([]rune(s.inserted))
		b.WriteString(s.inserted)
		pos += s.replaced
	}
	if pos != len(runes) {
		applied = false
	}
	if !applied {
		return inserted, deleted, "", false
	}
	return inserted, deleted, b.String(), true
}
