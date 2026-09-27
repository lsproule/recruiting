package service

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

// RoomEvent is one message on a room's stream: what happened, who it is
// from, whom it is for (empty for everyone), and the payload.
type RoomEvent struct {
	Type string          `json:"type"`
	From string          `json:"from,omitempty"`
	To   string          `json:"to,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

// Room event types. Signalling (offer, answer, ice) is opaque to the server;
// doc carries a shared-editor update; the rest is roster and lifecycle.
const (
	EventHello      = "hello"
	EventPeerJoined = "peer-joined"
	EventPeerLeft   = "peer-left"
	EventSignal     = "signal"
	EventDoc        = "doc"
	EventCode       = "code"
)

// RoomPeer is one connection to a room.
type RoomPeer struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

// hello is the first event a new connection receives: its own id, who is
// already there, and the shared editor as it stands.
type hello struct {
	Self     string     `json:"self"`
	Peers    []RoomPeer `json:"peers"`
	Doc      []string   `json:"doc"` // base64 Yjs updates, in order
	Language string     `json:"language"`
	Source   string     `json:"source"`
}

// RoomMember is a subscribed connection; Events delivers what the room
// sends it until Leave.
type RoomMember struct {
	Peer   RoomPeer
	Events <-chan RoomEvent
	ch     chan RoomEvent
	hub    *RoomHub
	key    string
}

// Leave takes the member out of the room and tells the others.
func (m *RoomMember) Leave() { m.hub.leave(m) }

// The shared editor's update log has two ceilings. At maxDocLog updates or
// maxDocBytesSoft decoded bytes the room asks the next writer for a snapshot,
// which replaces the whole log with one update. At maxDocLogHard updates or
// maxDocBytes it stops taking updates until it gets one: a client that never
// compacts must not grow the process. The log is never trimmed from the
// front instead, because a newcomer replays it from the last snapshot and
// Yjs cannot apply an update whose predecessors are missing. A snapshot is
// requested long before the hard caps, and every accepted update repeats the
// request, so a client that answers it never sees them.
const (
	maxDocLog       = 400
	maxDocLogHard   = 4 * maxDocLog
	maxDocBytesSoft = 2 << 20
	maxDocBytes     = 8 << 20
)

// memberBuffer is how many events a slow connection may fall behind before
// it is dropped: a client that far behind has lost the stream anyway.
const memberBuffer = 256

// RoomIdleTTL is how long a room may go untouched — no join, signal, editor
// update or save — before the hub drops it, members and all. A live client
// re-announces its cursor over the signal channel every few seconds, so a
// room this quiet has only connections the server has not yet noticed are
// dead. A room whose last member leaves is dropped at once; this is the
// backstop for the rest.
const RoomIdleTTL = 30 * time.Minute

// roomSweepInterval is how often idle rooms are looked for while any exist.
const roomSweepInterval = time.Minute

// ErrDocLogFull is returned to a client whose editor updates cannot be kept
// because the room's log is at its hard cap and the client has not answered
// the request to compact it.
var ErrDocLogFull = errors.New("service: the shared editor's history is too long to keep; send a snapshot first")

type roomState struct {
	key     string
	members map[string]*RoomMember
	// doc is the update log since the last snapshot, each entry base64 as
	// the clients send and receive it, so a hello never re-encodes it;
	// docBytes is the decoded size of the whole log.
	doc      []string
	docBytes int
	language string
	source   string
	// touched is when the room last saw a member do anything.
	touched time.Time
}

// RoomHub keeps the live rooms of this process: who is connected to each
// and the shared editor's update log. A room lives only while someone is in
// it; the persisted editor state is the store's, and a peer joining an
// empty room is seeded from there.
type RoomHub struct {
	mu    sync.Mutex
	rooms map[string]*roomState
	next  uint64
	// now is the clock; tests move it.
	now func() time.Time
	// sweep is the pending idle sweep, nil when no room exists to sweep.
	sweep *time.Timer
}

func NewRoomHub() *RoomHub { return &RoomHub{rooms: map[string]*roomState{}, now: time.Now} }

func (h *RoomHub) room(key string) *roomState {
	r, ok := h.rooms[key]
	if !ok {
		r = &roomState{key: key, members: map[string]*RoomMember{}}
		h.rooms[key] = r
		h.schedule()
	}
	r.touched = h.now()
	return r
}

// Join subscribes a connection. language and source seed the editor when
// the room has no live document yet, which is what a restart, or the last
// member leaving, leaves behind. The hello event is queued first, then the
// others are told.
func (h *RoomHub) Join(key, name, role, language, source string) *RoomMember {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.room(key)
	if r.language == "" {
		r.language, r.source = language, source
	}
	h.next++
	ch := make(chan RoomEvent, memberBuffer)
	m := &RoomMember{Peer: RoomPeer{ID: peerID(h.next), Name: name, Role: role}, Events: ch, ch: ch, hub: h, key: key}
	peers := make([]RoomPeer, 0, len(r.members))
	for _, other := range r.members {
		peers = append(peers, other.Peer)
	}
	deliver(m, RoomEvent{Type: EventHello, Data: mustJSON(hello{Self: m.Peer.ID, Peers: peers, Doc: append([]string{}, r.doc...), Language: r.language, Source: r.source})})
	r.members[m.Peer.ID] = m
	h.broadcast(r, RoomEvent{Type: EventPeerJoined, From: m.Peer.ID, Data: mustJSON(m.Peer)}, m.Peer.ID)
	return m
}

func (h *RoomHub) leave(m *RoomMember) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if r, ok := h.rooms[m.key]; ok {
		h.remove(r, m)
	}
}

// remove takes m out of r and closes its stream. The others are told, and
// when m was the last one the room goes with it: nothing in it outlives its
// members, since the editor's text is saved to the store as it changes.
func (h *RoomHub) remove(r *roomState, m *RoomMember) {
	if r.members[m.Peer.ID] != m {
		return
	}
	delete(r.members, m.Peer.ID)
	close(m.ch)
	r.touched = h.now()
	if len(r.members) == 0 {
		if h.rooms[r.key] == r {
			delete(h.rooms, r.key)
		}
		return
	}
	h.broadcast(r, RoomEvent{Type: EventPeerLeft, From: m.Peer.ID, Data: mustJSON(m.Peer)}, "")
}

// Signal relays one message from a peer to another peer, or to everyone
// else when to is empty. It reports whether the sender is a member.
func (h *RoomHub) Signal(key, from, to string, data json.RawMessage) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	r, ok := h.rooms[key]
	if !ok || r.members[from] == nil {
		return false
	}
	r.touched = h.now()
	ev := RoomEvent{Type: EventSignal, From: from, To: to, Data: data}
	if to == "" {
		h.broadcast(r, ev, from)
		return true
	}
	if target := r.members[to]; target != nil && !deliver(target, ev) {
		h.remove(r, target)
	}
	return true
}

// Doc appends a shared-editor update and relays it. A snapshot replaces the
// whole log with the one update, which is how a client answers the request
// to compact. It reports whether the log has grown long enough to want a
// snapshot, and fails with ErrNotPeer for a sender who is not a member or
// ErrDocLogFull when the log is at its hard cap and the update is not a
// snapshot; a refused update is neither kept nor relayed.
func (h *RoomHub) Doc(key, from string, update []byte, snapshot bool) (wantSnapshot bool, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r, ok := h.rooms[key]
	if !ok || r.members[from] == nil {
		return false, ErrNotPeer
	}
	encoded := base64.StdEncoding.EncodeToString(update)
	switch {
	case snapshot && len(update) <= maxDocBytes:
		r.doc, r.docBytes = []string{encoded}, len(update)
	case snapshot, len(r.doc) >= maxDocLogHard, r.docBytes+len(update) > maxDocBytes:
		return true, ErrDocLogFull
	default:
		r.doc = append(r.doc, encoded)
		r.docBytes += len(update)
		h.broadcast(r, RoomEvent{Type: EventDoc, From: from, Data: mustJSON(encoded)}, from)
	}
	r.touched = h.now()
	return len(r.doc) >= maxDocLog || r.docBytes >= maxDocBytesSoft, nil
}

// Code records the editor's language and text as last saved, so a client
// joining after a restart of the log still sees them, and tells the others
// the language changed.
func (h *RoomHub) Code(key, from, language, source string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r, ok := h.rooms[key]
	if !ok {
		return
	}
	r.touched = h.now()
	changed := r.language != language
	r.language, r.source = language, source
	if changed {
		h.broadcast(r, RoomEvent{Type: EventCode, From: from, Data: mustJSON(map[string]string{"language": language})}, from)
	}
}

// Peers is who is connected to a room right now.
func (h *RoomHub) Peers(key string) []RoomPeer {
	h.mu.Lock()
	defer h.mu.Unlock()
	r, ok := h.rooms[key]
	if !ok {
		return nil
	}
	out := make([]RoomPeer, 0, len(r.members))
	for _, m := range r.members {
		out = append(out, m.Peer)
	}
	return out
}

// Rooms is how many rooms are live in this process.
func (h *RoomHub) Rooms() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.rooms)
}

// schedule books the next idle sweep when none is pending and there is a
// room to sweep. It runs under the lock.
func (h *RoomHub) schedule() {
	if h.sweep == nil && len(h.rooms) > 0 {
		h.sweep = time.AfterFunc(roomSweepInterval, h.sweepIdle)
	}
}

// sweepIdle drops every room untouched for RoomIdleTTL, closing the streams
// of any member still listed in it so their handlers end, then books the
// next sweep while rooms remain.
func (h *RoomHub) sweepIdle() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sweep = nil
	now := h.now()
	for key, r := range h.rooms {
		if now.Sub(r.touched) < RoomIdleTTL {
			continue
		}
		delete(h.rooms, key)
		for id, m := range r.members {
			delete(r.members, id)
			close(m.ch)
		}
	}
	h.schedule()
}

// broadcast queues ev for every member but except. A member that cannot
// take it is removed once the round is over, which tells the rest.
func (h *RoomHub) broadcast(r *roomState, ev RoomEvent, except string) {
	var dropped []*RoomMember
	for id, m := range r.members {
		if id == except {
			continue
		}
		if !deliver(m, ev) {
			dropped = append(dropped, m)
		}
	}
	for _, m := range dropped {
		h.remove(r, m)
	}
}

// deliver queues an event without blocking and reports whether it fit; a
// connection that cannot keep up is closed rather than stalling the room.
func deliver(m *RoomMember, ev RoomEvent) bool {
	select {
	case m.ch <- ev:
		return true
	default:
		return false
	}
}

func peerID(n uint64) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz"
	var b []byte
	for n > 0 {
		b = append([]byte{alphabet[n%26]}, b...)
		n /= 26
	}
	if len(b) == 0 {
		b = []byte{'a'}
	}
	return "p" + string(b)
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return b
}
