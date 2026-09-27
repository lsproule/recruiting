package service

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// drain reads every event queued for m without blocking.
func drain(m *RoomMember) []RoomEvent {
	var out []RoomEvent
	for {
		select {
		case ev, ok := <-m.Events:
			if !ok {
				return out
			}
			out = append(out, ev)
		default:
			return out
		}
	}
}

func helloOf(t *testing.T, m *RoomMember) hello {
	t.Helper()
	ev := <-m.Events
	if ev.Type != EventHello {
		t.Fatalf("first event = %+v, want hello", ev)
	}
	var h hello
	if err := json.Unmarshal(ev.Data, &h); err != nil {
		t.Fatal(err)
	}
	return h
}

// TestRoomHubDropsARoomWhenItsLastMemberLeaves is the leak the hub used to
// have: a room whose editor had seen an update stayed for the life of the
// process. Now it goes with its last member, and whoever comes next is
// seeded from what the caller persisted, not from the hub.
func TestRoomHubDropsARoomWhenItsLastMemberLeaves(t *testing.T) {
	hub := NewRoomHub()
	a := hub.Join("k", "Ada", RoleInterviewer, "python", "print(1)")
	helloOf(t, a)
	if _, err := hub.Doc("k", a.Peer.ID, []byte{1, 2, 3}, false); err != nil {
		t.Fatal(err)
	}
	hub.Code("k", a.Peer.ID, "python", "print(2)")
	a.Leave()
	if n := hub.Rooms(); n != 0 {
		t.Fatalf("%d rooms live after the last member left, want 0", n)
	}
	if _, ok := <-a.Events; ok {
		t.Fatal("a's stream still open after leaving")
	}
	// Leaving twice is harmless.
	a.Leave()

	// A rejoin is a fresh room: no log, and the editor as the store has it.
	b := hub.Join("k", "Bo", RoleCandidate, "go", "package main")
	h := helloOf(t, b)
	if len(h.Doc) != 0 || h.Language != "go" || h.Source != "package main" || len(h.Peers) != 0 {
		t.Fatalf("hello after a rejoin = %+v, want an empty log seeded from the store", h)
	}
	b.Leave()
}

// TestRoomHubDropsARoomEmptiedByEvictingASlowMember covers the other way out
// of a room: a connection that cannot keep up is closed, the others hear it
// left, and a room that empties this way is dropped too.
func TestRoomHubDropsARoomEmptiedByEvictingASlowMember(t *testing.T) {
	hub := NewRoomHub()
	slow := hub.Join("k", "Slow", RoleObserver, "python", "")
	helloOf(t, slow)
	quick := hub.Join("k", "Quick", RoleInterviewer, "", "")
	helloOf(t, quick)
	drain(slow)
	// Fill slow's buffer without reading it, then one more overflows it.
	for range memberBuffer + 1 {
		if !hub.Signal("k", quick.Peer.ID, "", json.RawMessage(`1`)) {
			t.Fatal("signal refused")
		}
	}
	if _, ok := hub.rooms["k"].members[slow.Peer.ID]; ok {
		t.Fatal("the slow member is still listed")
	}
	evs := drain(slow)
	if len(evs) != memberBuffer {
		t.Fatalf("slow received %d events before being cut off, want %d", len(evs), memberBuffer)
	}
	if _, ok := <-slow.Events; ok {
		t.Fatal("slow's stream still open after eviction")
	}
	var sawLeft bool
	for _, ev := range drain(quick) {
		if ev.Type == EventPeerLeft && ev.From == slow.Peer.ID {
			sawLeft = true
		}
	}
	if !sawLeft {
		t.Fatal("the remaining member was not told the slow one left")
	}
	// The handler behind the evicted stream still calls Leave; that must
	// not touch the room quick is in.
	slow.Leave()
	if peers := hub.Peers("k"); len(peers) != 1 || peers[0].ID != quick.Peer.ID {
		t.Fatalf("peers = %+v", peers)
	}

	// Now quick is the slow one, and the last: evicting it drops the room.
	for range memberBuffer + 1 {
		hub.Code("k", "", "go", "")
		hub.Code("k", "", "python", "")
	}
	if n := hub.Rooms(); n != 0 {
		t.Fatalf("%d rooms live after the last member was evicted, want 0", n)
	}
}

// TestRoomHubCapsTheDocLogByCount asks for a snapshot at the soft cap and
// refuses updates at the hard one, without relaying what it refused; a
// snapshot resets everything.
func TestRoomHubCapsTheDocLogByCount(t *testing.T) {
	hub := NewRoomHub()
	a := hub.Join("k", "Ada", RoleInterviewer, "python", "")
	helloOf(t, a)
	b := hub.Join("k", "Bo", RoleCandidate, "", "")
	helloOf(t, b)
	drain(a)
	relayed := 0 // b reads as it goes, as a live client would
	for i := range maxDocLogHard {
		want, err := hub.Doc("k", a.Peer.ID, []byte{byte(i)}, false)
		if err != nil {
			t.Fatalf("update %d refused: %v", i, err)
		}
		if want != (i+1 >= maxDocLog) {
			t.Fatalf("update %d: snapshot wanted %v", i, want)
		}
		relayed += len(drain(b))
	}
	want, err := hub.Doc("k", a.Peer.ID, []byte{0}, false)
	if !errors.Is(err, ErrDocLogFull) || !want {
		t.Fatalf("update past the hard cap = want %v, %v; want ErrDocLogFull", want, err)
	}
	if relayed += len(drain(b)); relayed != maxDocLogHard {
		t.Fatalf("b was relayed %d updates, want exactly the %d that were kept", relayed, maxDocLogHard)
	}
	if _, err := hub.Doc("k", a.Peer.ID, []byte{7}, true); err != nil {
		t.Fatalf("snapshot refused: %v", err)
	}
	r := hub.rooms["k"]
	if len(r.doc) != 1 || r.docBytes != 1 {
		t.Fatalf("after a snapshot the log holds %d updates of %d bytes, want 1 of 1", len(r.doc), r.docBytes)
	}
	if want, err := hub.Doc("k", a.Peer.ID, []byte{8}, false); err != nil || want {
		t.Fatalf("update after a snapshot = want %v, %v", want, err)
	}
}

// TestRoomHubCapsTheDocLogByBytes is the same with few, large updates: the
// byte ceiling asks for a snapshot first and then refuses.
func TestRoomHubCapsTheDocLogByBytes(t *testing.T) {
	hub := NewRoomHub()
	a := hub.Join("k", "Ada", RoleInterviewer, "python", "")
	helloOf(t, a)
	update := bytes.Repeat([]byte{1}, 1<<20)
	soft := maxDocBytesSoft / len(update)
	kept := 0
	for {
		want, err := hub.Doc("k", a.Peer.ID, update, false)
		if errors.Is(err, ErrDocLogFull) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		kept++
		if want != (kept >= soft) {
			t.Fatalf("after %d MiB: snapshot wanted %v", kept, want)
		}
		if kept > maxDocBytes/len(update) {
			t.Fatalf("%d MiB kept, past the %d MiB cap", kept, maxDocBytes/len(update))
		}
	}
	if kept != maxDocBytes/len(update) {
		t.Fatalf("%d MiB kept before refusing, want %d", kept, maxDocBytes/len(update))
	}
	// A snapshot larger than the whole log may be is refused as well.
	if _, err := hub.Doc("k", a.Peer.ID, bytes.Repeat([]byte{1}, maxDocBytes+1), true); !errors.Is(err, ErrDocLogFull) {
		t.Fatalf("oversized snapshot returned %v, want ErrDocLogFull", err)
	}
	if _, err := hub.Doc("k", a.Peer.ID, update, true); err != nil {
		t.Fatal(err)
	}
	if r := hub.rooms["k"]; r.docBytes != len(update) {
		t.Fatalf("after a snapshot the log holds %d bytes, want %d", r.docBytes, len(update))
	}
}

// TestRoomHubHelloCarriesTheLogAsStored proves the hello is built from the
// log as kept — the base64 the clients speak — rather than re-encoded on
// every join.
func TestRoomHubHelloCarriesTheLogAsStored(t *testing.T) {
	hub := NewRoomHub()
	a := hub.Join("k", "Ada", RoleInterviewer, "python", "")
	helloOf(t, a)
	updates := [][]byte{{1, 2, 3}, {4, 5}, {6}}
	for _, u := range updates {
		if _, err := hub.Doc("k", a.Peer.ID, u, false); err != nil {
			t.Fatal(err)
		}
	}
	b := hub.Join("k", "Bo", RoleCandidate, "", "")
	h := helloOf(t, b)
	if len(h.Doc) != len(updates) {
		t.Fatalf("hello log has %d entries, want %d", len(h.Doc), len(updates))
	}
	for i, u := range updates {
		if h.Doc[i] != base64.StdEncoding.EncodeToString(u) {
			t.Errorf("hello log[%d] = %q, want %q", i, h.Doc[i], base64.StdEncoding.EncodeToString(u))
		}
	}
	// The hello holds its own copy: appending to the room does not reach it.
	r := hub.rooms["k"]
	if &h.Doc[0] == &r.doc[0] {
		t.Error("hello aliases the room's log")
	}
}

// TestRoomHubSweepsIdleRooms is the backstop: a room nobody has touched for
// RoomIdleTTL is dropped even with members listed, and their streams end so
// the handlers behind them return.
func TestRoomHubSweepsIdleRooms(t *testing.T) {
	hub := NewRoomHub()
	now := time.Date(2026, time.June, 2, 9, 0, 0, 0, time.UTC)
	hub.now = func() time.Time { return now }
	stale := hub.Join("stale", "Ada", RoleInterviewer, "python", "")
	helloOf(t, stale)
	if hub.sweep == nil {
		t.Fatal("no sweep booked once a room exists")
	}
	now = now.Add(RoomIdleTTL - time.Minute)
	fresh := hub.Join("fresh", "Bo", RoleCandidate, "python", "")
	helloOf(t, fresh)

	hub.sweepIdle()
	if hub.Rooms() != 2 {
		t.Fatalf("a sweep before the TTL dropped rooms: %d left", hub.Rooms())
	}
	now = now.Add(2 * time.Minute)
	hub.sweepIdle()
	if hub.Rooms() != 1 || hub.Peers("stale") != nil || len(hub.Peers("fresh")) != 1 {
		t.Fatalf("after the TTL: %d rooms, stale peers %v, fresh peers %v", hub.Rooms(), hub.Peers("stale"), hub.Peers("fresh"))
	}
	if _, ok := <-stale.Events; ok {
		t.Fatal("the stale member's stream is still open")
	}
	stale.Leave() // its handler still runs this; the room is gone
	if hub.sweep == nil {
		t.Fatal("no further sweep booked while a room remains")
	}

	// Any activity keeps a room: a signal here, and the sweep after the TTL
	// spares it.
	now = now.Add(RoomIdleTTL - time.Minute)
	if !hub.Signal("fresh", fresh.Peer.ID, "", json.RawMessage(`{}`)) {
		t.Fatal("signal refused")
	}
	now = now.Add(2 * time.Minute)
	hub.sweepIdle()
	if hub.Rooms() != 1 {
		t.Fatalf("a signalled room was swept: %d rooms", hub.Rooms())
	}
	fresh.Leave()
	hub.sweepIdle()
	if hub.sweep != nil {
		t.Fatal("a sweep is still booked with no rooms")
	}
}

// The client iterates the hello's log unconditionally, so an empty room must
// say "[]" and never "null".
func TestRoomHubHelloOfAnEmptyRoomCarriesAnEmptyList(t *testing.T) {
	h := NewRoomHub()
	m := h.Join("room", "Ada", "candidate", "python", "")
	defer m.Leave()
	ev := <-m.Events
	if ev.Type != EventHello {
		t.Fatalf("first event = %+v, want hello", ev)
	}
	if !strings.Contains(string(ev.Data), `"doc":[]`) {
		t.Fatalf("hello = %s, want an empty doc list", ev.Data)
	}
}
