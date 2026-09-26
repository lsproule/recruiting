package service

import (
	"encoding/base64"
	"encoding/json"
	"sync"
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

// maxDocLog is how many updates a room keeps before asking for a snapshot;
// past it, a joining client would replay a long history for nothing.
const maxDocLog = 400

// memberBuffer is how many events a slow connection may fall behind before
// it is dropped: a client that far behind has lost the stream anyway.
const memberBuffer = 256

type roomState struct {
	members  map[string]*RoomMember
	doc      [][]byte
	language string
	source   string
}

// RoomHub keeps the live rooms of this process: who is connected to each
// and the shared editor's update log. Nothing here outlives the process;
// the persisted editor state is the store's.
type RoomHub struct {
	mu    sync.Mutex
	rooms map[string]*roomState
	next  uint64
}

func NewRoomHub() *RoomHub { return &RoomHub{rooms: map[string]*roomState{}} }

func (h *RoomHub) room(key string) *roomState {
	r, ok := h.rooms[key]
	if !ok {
		r = &roomState{members: map[string]*RoomMember{}}
		h.rooms[key] = r
	}
	return r
}

// Join subscribes a connection. language and source seed the editor when
// the room has no live document yet, which is what a restart leaves behind.
// The hello event is queued first, then the others are told.
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
	doc := make([]string, 0, len(r.doc))
	for _, u := range r.doc {
		doc = append(doc, base64.StdEncoding.EncodeToString(u))
	}
	h.deliver(m, RoomEvent{Type: EventHello, Data: mustJSON(hello{Self: m.Peer.ID, Peers: peers, Doc: doc, Language: r.language, Source: r.source})})
	r.members[m.Peer.ID] = m
	h.broadcast(r, RoomEvent{Type: EventPeerJoined, From: m.Peer.ID, Data: mustJSON(m.Peer)}, m.Peer.ID)
	return m
}

func (h *RoomHub) leave(m *RoomMember) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r, ok := h.rooms[m.key]
	if !ok {
		return
	}
	if _, present := r.members[m.Peer.ID]; !present {
		return
	}
	delete(r.members, m.Peer.ID)
	close(m.ch)
	h.broadcast(r, RoomEvent{Type: EventPeerLeft, From: m.Peer.ID, Data: mustJSON(m.Peer)}, "")
	if len(r.members) == 0 && len(r.doc) == 0 {
		delete(h.rooms, m.key)
	}
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
	ev := RoomEvent{Type: EventSignal, From: from, To: to, Data: data}
	if to == "" {
		h.broadcast(r, ev, from)
		return true
	}
	if target := r.members[to]; target != nil {
		h.deliver(target, ev)
	}
	return true
}

// Doc appends a shared-editor update and relays it. A snapshot replaces the
// whole log with the one update, which is how a client answers the request
// to compact. It reports whether the sender is a member and whether the log
// has grown long enough to want a snapshot.
func (h *RoomHub) Doc(key, from string, update []byte, snapshot bool) (member, wantSnapshot bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r, ok := h.rooms[key]
	if !ok || r.members[from] == nil {
		return false, false
	}
	if snapshot {
		r.doc = [][]byte{update}
	} else {
		r.doc = append(r.doc, update)
		h.broadcast(r, RoomEvent{Type: EventDoc, From: from, Data: mustJSON(base64.StdEncoding.EncodeToString(update))}, from)
	}
	return true, len(r.doc) >= maxDocLog
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

func (h *RoomHub) broadcast(r *roomState, ev RoomEvent, except string) {
	for id, m := range r.members {
		if id == except {
			continue
		}
		h.deliver(m, ev)
	}
}

// deliver queues an event without blocking; a connection that cannot keep
// up is closed rather than stalling the room.
func (h *RoomHub) deliver(m *RoomMember, ev RoomEvent) {
	select {
	case m.ch <- ev:
	default:
		if r, ok := h.rooms[m.key]; ok {
			delete(r.members, m.Peer.ID)
		}
		close(m.ch)
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
