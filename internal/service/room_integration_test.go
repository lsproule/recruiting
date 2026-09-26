//go:build integration

package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"recruiting/internal/domain"
	"recruiting/internal/service"
)

// TestSlotRoomWindowAndAuthorisation books a video interview and walks the
// room's window: shut before, open ten minutes ahead, the candidate through
// their own link only.
func TestSlotRoomWindowAndAuthorisation(t *testing.T) {
	f := newScheduleFixture(t)
	ctx := context.Background()
	if _, err := f.sys.Exec(ctx, `update stage set interview_format = 'video', duration_minutes = 60 where id = $1`, f.stages[domain.StageInterview]); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, time.June, 2, 9, 0, 0, 0, time.UTC)
	slot, err := f.sched.Book(ctx, f.candidate(), "tok", start, "Europe/Berlin")
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	// The confirmation carries the join note for a video interview.
	var payload string
	if err := f.sys.QueryRow(ctx, `select args->>'payload' from river_job where kind = 'email.send' and args->>'payload' like '%booking_confirmation%' and args->>'payload' like '%'||$1||'%' limit 1`, f.orgID.String()).Scan(&payload); err != nil {
		t.Fatalf("no confirmation queued: %v", err)
	}
	var mail struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(payload), &mail); err != nil {
		t.Fatal(err)
	}
	if note, _ := mail.Data["JoinNote"].(string); note == "" {
		t.Fatalf("confirmation carries no join note: %v", mail.Data)
	}

	rooms := service.NewRoomService(f.st, nil)
	rooms.Now = func() time.Time { return f.now }
	key := service.RoomKey{Kind: service.RoomSlot, ID: slot.ID}

	// Well before the start: shut, but described.
	room, err := rooms.Open(ctx, f.vetter(), key)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if room.Open || room.Role != service.RoleInterviewer || !room.OpensAt.Equal(start.Add(-service.RoomOpensBefore)) {
		t.Fatalf("room = %+v", room)
	}
	booking, err := f.sched.Booking(ctx, f.candidate())
	if err != nil {
		t.Fatal(err)
	}
	if !booking.Video || booking.RoomOpen {
		t.Fatalf("booking = video %v open %v", booking.Video, booking.RoomOpen)
	}

	// Nine minutes before: open for both, and the booking page says so.
	f.now = start.Add(-9 * time.Minute)
	room, err = rooms.Open(ctx, f.candidate(), key)
	if err != nil || !room.Open || room.Role != service.RoleCandidate {
		t.Fatalf("candidate room = %+v %v", room, err)
	}
	booking, _ = f.sched.Booking(ctx, f.candidate())
	if !booking.RoomOpen {
		t.Fatal("booking page does not offer the room")
	}
	interviewer, member, err := rooms.Join(ctx, f.vetter(), key)
	if err != nil || !interviewer.Open {
		t.Fatalf("interviewer join: %v", err)
	}
	defer member.Leave()
	if peers := rooms.Hub.Peers(key.String()); len(peers) != 1 || peers[0].Role != service.RoleInterviewer {
		t.Fatalf("peers = %+v", peers)
	}

	// A different application's link, a client user, and a vetter who is not
	// this slot's are all refused.
	other := f.candidate()
	other.SubjectID = uuid.New()
	if _, err := rooms.Open(ctx, other, key); !errors.Is(err, service.ErrForbidden) && !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("another link opened the room: %v", err)
	}
	client := service.Principal{Kind: service.PrincipalClientUser, OrgID: f.orgID, ClientCompanyID: uuid.New()}
	if _, err := rooms.Open(ctx, client, key); !errors.Is(err, service.ErrForbidden) && !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("a client opened the room: %v", err)
	}
	otherVetter := uuid.New()
	if _, err := f.sys.Exec(ctx, `insert into org_user (id, org_id, email, name) values ($1, $2, $3, 'Other Vetter')`, otherVetter, f.orgID, otherVetter.String()+"@example.com"); err != nil {
		t.Fatal(err)
	}
	stranger := service.Principal{Kind: service.PrincipalOrgUser, OrgID: f.orgID, UserID: otherVetter, Roles: []string{service.RoleVetter}}
	if _, err := rooms.Open(ctx, stranger, key); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("another vetter opened the room: %v", err)
	}

	// Two hours after the start the room has closed.
	f.now = start.Add(service.RoomStaysOpen + time.Minute)
	room, _ = rooms.Open(ctx, f.vetter(), key)
	if room.Open {
		t.Fatal("room still open two hours after the start")
	}

	// The interviews screen lists the slot with its format.
	interviews := service.NewInterviewService(f.st)
	interviews.Now = func() time.Time { return start.Add(-time.Hour) }
	rows, err := interviews.Upcoming(ctx, f.vetter())
	if err != nil || len(rows) != 1 || !rows[0].Video || rows[0].ID != slot.ID {
		t.Fatalf("upcoming = %+v %v", rows, err)
	}
}

// TestRoomHubRelaysAndCompacts exercises the in-memory hub on its own: the
// hello, joins and leaves, targeted and broadcast signals, and the doc log.
func TestRoomHubRelaysAndCompacts(t *testing.T) {
	hub := service.NewRoomHub()
	a := hub.Join("k", "Ada", service.RoleInterviewer, "python", "print(1)")
	hello := <-a.Events
	var h struct {
		Self     string `json:"self"`
		Source   string `json:"source"`
		Language string `json:"language"`
	}
	if err := json.Unmarshal(hello.Data, &h); err != nil || h.Self != a.Peer.ID || h.Source != "print(1)" || h.Language != "python" {
		t.Fatalf("hello = %s %v", hello.Data, err)
	}
	b := hub.Join("k", "Bo", service.RoleCandidate, "", "")
	<-b.Events // hello
	if ev := <-a.Events; ev.Type != service.EventPeerJoined || ev.From != b.Peer.ID {
		t.Fatalf("a saw %+v", ev)
	}
	if !hub.Signal("k", a.Peer.ID, b.Peer.ID, json.RawMessage(`{"x":1}`)) {
		t.Fatal("signal from a member refused")
	}
	if ev := <-b.Events; ev.Type != service.EventSignal || ev.From != a.Peer.ID || string(ev.Data) != `{"x":1}` {
		t.Fatalf("b saw %+v", ev)
	}
	if hub.Signal("k", "nobody", "", nil) {
		t.Fatal("signal from a stranger accepted")
	}
	member, want := hub.Doc("k", a.Peer.ID, []byte{1, 2, 3}, false)
	if !member || want {
		t.Fatalf("doc = member %v want %v", member, want)
	}
	if ev := <-b.Events; ev.Type != service.EventDoc {
		t.Fatalf("b saw %+v", ev)
	}
	c := hub.Join("k", "Cy", service.RoleObserver, "", "")
	hello = <-c.Events
	var h2 struct {
		Doc   []string `json:"doc"`
		Peers []any    `json:"peers"`
	}
	if err := json.Unmarshal(hello.Data, &h2); err != nil || len(h2.Doc) != 1 || len(h2.Peers) != 2 {
		t.Fatalf("late hello = %s", hello.Data)
	}
	if _, want := hub.Doc("k", a.Peer.ID, []byte{9}, true); want {
		t.Fatal("a snapshot still wants a snapshot")
	}
	if ev := <-b.Events; ev.Type != service.EventPeerJoined || ev.From != c.Peer.ID {
		t.Fatalf("b saw %+v", ev)
	}
	b.Leave()
	if ev := <-a.Events; ev.Type != service.EventPeerJoined { // c's join came first
		t.Fatalf("a saw %+v", ev)
	}
	if ev := <-a.Events; ev.Type != service.EventPeerLeft || ev.From != b.Peer.ID {
		t.Fatalf("a saw %+v", ev)
	}
	if _, ok := <-b.Events; ok {
		t.Fatal("b's channel still open after leaving")
	}
	if peers := hub.Peers("k"); len(peers) != 2 {
		t.Fatalf("peers = %+v", peers)
	}
}
