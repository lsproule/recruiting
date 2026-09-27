package signals

import (
	"encoding/json"
	"time"
	"unicode/utf16"

	"github.com/google/uuid"
)

// Stream folds a recording one event at a time into what the signals
// read, so a caller decoding a long stream never has to hold it. It keeps,
// per problem, the timeline (edit counts, pastes, blurs, and the document
// replayed from the changesets) and, for the sitting, the clock of the run
// and submit events naming a submission, the fullscreen exits, and the
// webcam beats: a summary sized by the session's shape, not by the number
// of edits' payloads. Events must be added in seq order, the order the
// recording was made in; nothing may be added once Compute has read it.
type Stream struct {
	initial   map[uuid.UUID]string
	timelines map[uuid.UUID]*Timeline
	docs      map[uuid.UUID][]uint16
	openBlur  map[uuid.UUID]int // index of the unclosed blur, -1 for none
	// named is the seq and clock of the run or submit event naming each
	// submission; the last one in the stream wins.
	named map[uuid.UUID]eventClock
	count int
	// first and last are when the first and the last event happened.
	first, last time.Time
	exits       []fullscreenExit
	openExit    int // index of the unclosed exit, -1 for none
	beats       map[int]bool
	reported    int // webcam beats reported, ok or not
	taken       int // webcam beats that produced a frame
	done        bool
}

type eventClock struct {
	Seq int64
	At  time.Time
}

// fullscreenExit is one departure from fullscreen: where it happened and
// how long it lasted, which is the whole evidence the signal reports.
type fullscreenExit struct {
	ProblemID uuid.UUID
	Seq       int64
	At        time.Time
	Away      time.Duration
}

// NewStream starts a fold. initial is each problem's editor text before the
// first event (starter text, if any); a stream that starts with a retain
// over it reconstructs only from that.
func NewStream(initial map[uuid.UUID]string) *Stream {
	return &Stream{
		initial:   initial,
		timelines: map[uuid.UUID]*Timeline{},
		docs:      map[uuid.UUID][]uint16{},
		openBlur:  map[uuid.UUID]int{},
		named:     map[uuid.UUID]eventClock{},
		openExit:  -1,
		beats:     map[int]bool{},
	}
}

// Count is how many events have been folded.
func (s *Stream) Count() int { return s.count }

// Add folds one event. The payload is not kept: only what the rules read
// from it is.
func (s *Stream) Add(ev Event) {
	if s.done {
		return
	}
	at := ev.At()
	if s.count == 0 {
		s.first = at
	}
	s.count++
	s.last = at
	s.addSession(ev, at)
	if ev.ProblemID != nil {
		s.addTimeline(*ev.ProblemID, ev, at)
	}
}

// addSession folds the events that describe the sitting rather than one
// editor: submissions named, fullscreen, webcam beats.
func (s *Stream) addSession(ev Event, at time.Time) {
	switch ev.Kind {
	case "run", "submit":
		var data struct {
			SubmissionID uuid.UUID `json:"submission_id"`
		}
		if err := json.Unmarshal(ev.Payload, &data); err == nil && data.SubmissionID != uuid.Nil {
			s.named[data.SubmissionID] = eventClock{Seq: ev.Seq, At: at}
		}
	case "fullscreen_exit":
		if s.openExit < 0 {
			s.exits = append(s.exits, fullscreenExit{ProblemID: problemOf(ev), Seq: ev.Seq, At: at})
			s.openExit = len(s.exits) - 1
		}
	case "fullscreen_enter":
		s.closeExit(at)
	case "snapshot":
		var d struct {
			Seq int  `json:"seq"`
			OK  bool `json:"ok"`
		}
		if err := json.Unmarshal(ev.Payload, &d); err != nil || s.beats[d.Seq] {
			return
		}
		s.beats[d.Seq] = true
		s.reported++
		if d.OK {
			s.taken++
		}
	}
}

// closeExit ends the open fullscreen exit at when, if there is one.
func (s *Stream) closeExit(when time.Time) {
	if s.openExit < 0 {
		return
	}
	exit := &s.exits[s.openExit]
	exit.Away = max(when.Sub(exit.At), 0)
	s.openExit = -1
}

// addTimeline folds one event into its problem's timeline.
func (s *Stream) addTimeline(id uuid.UUID, ev Event, at time.Time) {
	tl := s.timelines[id]
	if tl == nil {
		tl = &Timeline{ProblemID: id, First: at, FirstServer: ev.ServerTs, Reconstructed: true}
		s.timelines[id] = tl
		s.docs[id] = utf16.Encode([]rune(s.initial[id]))
		s.openBlur[id] = -1
	}
	tl.Last = at
	switch ev.Kind {
	case "edit":
		cs, ok := parseChangeset(ev.Payload)
		if !ok {
			tl.Reconstructed = false
			return
		}
		ins, del, next, applied := cs.apply(s.docs[id])
		if applied {
			s.docs[id] = next
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
		p.Len = max(p.Len, 0)
		tl.Pastes = append(tl.Pastes, Paste{Seq: ev.Seq, At: at, Len: p.Len, Internal: p.Internal})
	case "blur":
		if s.openBlur[id] < 0 {
			tl.Blurs = append(tl.Blurs, Blur{Seq: ev.Seq, From: at, To: at})
			s.openBlur[id] = len(tl.Blurs) - 1
		}
	case "focus":
		if i := s.openBlur[id]; i >= 0 {
			tl.Blurs[i].To = at
			s.openBlur[id] = -1
		}
	}
}

// finish closes what the stream left open: a blur or a fullscreen exit
// never returned from runs to the last event, the documents are decoded,
// and each paste is matched to the edit that carried it. It is idempotent
// and ends the fold.
func (s *Stream) finish() {
	if s.done {
		return
	}
	s.done = true
	for id, tl := range s.timelines {
		if i := s.openBlur[id]; i >= 0 {
			tl.Blurs[i].To = tl.Last
		}
		tl.Source = string(utf16.Decode(s.docs[id]))
		markPastes(tl)
	}
	s.docs = nil
	s.closeExit(s.last)
}

// Timelines ends the fold and returns one timeline per problem.
func (s *Stream) Timelines() map[uuid.UUID]*Timeline {
	s.finish()
	return s.timelines
}

// clockSubmissions gives each submission the client time and seq of the
// run or submit event that names it, so paste and blur comparisons sit on
// one clock. One the stream never named is measured on the server's time.
func (s *Stream) clockSubmissions(subs []Submission) []Submission {
	out := make([]Submission, len(subs))
	for i, sub := range subs {
		if c, ok := s.named[sub.ID]; ok {
			sub.ClientAt, sub.Seq = c.At, c.Seq
		}
		if sub.ClientAt.IsZero() {
			sub.ClientAt = sub.At
		}
		out[i] = sub
	}
	return out
}
