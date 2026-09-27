package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"recruiting/internal/domain"
	"recruiting/internal/runner/server"
	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

// Room kinds: the subject a room hangs off.
const (
	RoomSlot    = "slot"    // a booked interview
	RoomPairing = "pairing" // one conversation of a sprint
)

// Room participant roles, as the tiles label them.
const (
	RoleInterviewer = "interviewer"
	RoleCandidate   = "candidate"
	RoleObserver    = "observer"
)

// How long before a booked interview its room opens, and how long after its
// start it stays open, so a call that runs over is not cut off.
const (
	RoomOpensBefore = 10 * time.Minute
	RoomStaysOpen   = 2 * time.Hour
)

// RunWallMs bounds one run in a room. An interview scratchpad is not a
// judged submission; it gets a little more rope than a test case.
const RunWallMs = 10_000

// MaxRoomSource bounds the editor text a room persists.
const MaxRoomSource = 256 << 10

var (
	ErrRoomClosed   = errors.New("service: this room is not open right now")
	ErrRoomKind     = errors.New("service: unknown room kind")
	ErrNotPeer      = errors.New("service: not connected to this room")
	ErrRoomLanguage = errors.New("service: unknown language")
	ErrSourceTooBig = errors.New("service: the editor text is too large to keep")
)

// RoomKey names a room.
type RoomKey struct {
	Kind string
	ID   uuid.UUID
}

func (k RoomKey) String() string { return k.Kind + ":" + k.ID.String() }

// ParseRoomKey reads a key back from its two parts.
func ParseRoomKey(kind, id string) (RoomKey, error) {
	if kind != RoomSlot && kind != RoomPairing {
		return RoomKey{}, ErrRoomKind
	}
	u, err := uuid.Parse(id)
	if err != nil {
		return RoomKey{}, ErrNotFound
	}
	return RoomKey{Kind: kind, ID: u}, nil
}

// Room is what the room page needs to know: who the visitor is in it, what
// it is about, when it is open, and the editor as last saved.
type Room struct {
	Key           RoomKey
	Title         string // the job
	Subtitle      string // the stage, or the sprint and round
	Role          string // the visitor's role
	Name          string // the visitor's name
	OpensAt       time.Time
	ClosesAt      time.Time
	Open          bool
	Language      string
	Source        string
	Languages     []string
	ApplicationID uuid.UUID
	StageID       uuid.UUID
	JobID         uuid.UUID
	CandidateName string
	// SprintID and Round are set on a pairing's room.
	SprintID uuid.UUID
	Round    int
	// PairingID is set on a pairing's room, for the rating card.
	PairingID uuid.UUID
}

// RoomCode is the editor's persisted state.
type RoomCode struct {
	Language  string
	Source    string
	UpdatedAt time.Time
}

// RunInput is one execution from a room's editor.
type RunInput struct {
	Language string
	Source   string
	Stdin    string
}

// RunResult is what the program printed.
type RunResult struct {
	Status        string
	CompileOutput string
	Stdout        string
	Stderr        string
	TimeMs        int64
}

// RoomService authorises rooms and runs their shared editor. Live state is
// the hub's; only the editor's text is persisted.
type RoomService struct {
	st   *store.Store
	exec Executor
	Hub  *RoomHub
	// Now is the clock; tests move it.
	Now func() time.Time
}

// NewRoomService wires the store and the runner client Run goes through; a
// nil exec disables Run.
func NewRoomService(st *store.Store, exec Executor) *RoomService {
	return &RoomService{st: st, exec: exec, Hub: NewRoomHub(), Now: time.Now}
}

// Open authorises p for the room and describes it. A room outside its
// window still describes itself, with Open false, so the page can say when
// it opens; joining it is refused.
func (s *RoomService) Open(ctx context.Context, p Principal, key RoomKey) (Room, error) {
	var out Room
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		var err error
		switch key.Kind {
		case RoomSlot:
			out, err = s.openSlot(ctx, tx, p, key.ID)
		case RoomPairing:
			out, err = s.openPairing(ctx, tx, p, key.ID)
		default:
			return ErrRoomKind
		}
		if err != nil {
			return err
		}
		out.Key = key
		out.Languages = domain.CodeLanguageIDs()
		code, err := tx.Q.GetInterviewRoom(ctx, db.GetInterviewRoomParams{Kind: key.Kind, SubjectID: key.ID})
		switch {
		case err == nil:
			out.Language, out.Source = code.Language, code.Source
		case errors.Is(err, pgx.ErrNoRows):
			out.Language = domain.Languages[0].ID
		default:
			return err
		}
		return nil
	})
	if err != nil {
		return Room{}, wrapRoom("open room", err)
	}
	return out, nil
}

func (s *RoomService) openSlot(ctx context.Context, tx *store.Tx, p Principal, id uuid.UUID) (Room, error) {
	slot, err := tx.Q.GetInterviewSlotRoom(ctx, id)
	if err != nil {
		return Room{}, err
	}
	if slot.Status == SlotCancelled {
		return Room{}, ErrNotFound
	}
	out := Room{
		Title: slot.JobTitle, Subtitle: slot.StageName,
		ApplicationID: slot.ApplicationID.UUID, StageID: slot.StageID.UUID, JobID: slot.JobID, CandidateName: slot.CandidateName,
		OpensAt: slot.StartsAt.Time.Add(-RoomOpensBefore), ClosesAt: slot.StartsAt.Time.Add(RoomStaysOpen),
	}
	if slot.EndsAt.Time.After(out.ClosesAt) {
		out.ClosesAt = slot.EndsAt.Time
	}
	switch p.Kind {
	case PrincipalOrgUser:
		user, err := tx.Q.GetOrgUser(ctx, db.GetOrgUserParams{ID: p.UserID, OrgID: p.OrgID})
		if err != nil {
			return Room{}, err
		}
		out.Name, out.Role = user.Name, RoleObserver
		if p.UserID == slot.VetterID {
			out.Role = RoleInterviewer
		} else if !p.HasRole(RoleRecruiter) && !p.HasRole(RoleAdmin) {
			return Room{}, ErrForbidden
		}
	case PrincipalMagicLink:
		if p.MagicPurpose != LinkBook || !slot.ApplicationID.Valid || p.SubjectID != slot.ApplicationID.UUID {
			return Room{}, ErrForbidden
		}
		out.Name, out.Role = slot.CandidateName, RoleCandidate
	default:
		return Room{}, ErrForbidden
	}
	now := s.Now()
	out.Open = !now.Before(out.OpensAt) && now.Before(out.ClosesAt)
	return out, nil
}

func (s *RoomService) openPairing(ctx context.Context, tx *store.Tx, p Principal, id uuid.UUID) (Room, error) {
	pair, err := tx.Q.GetSprintPairing(ctx, id)
	if err != nil {
		return Room{}, err
	}
	sp, err := loadSprint(ctx, tx, pair.SprintID)
	if err != nil {
		return Room{}, err
	}
	if sp.Status != SprintScheduled {
		return Room{}, ErrNotFound
	}
	pairing, _ := sp.Pairing(id)
	clock := sp.Clock()
	out := Room{
		Title: sp.JobTitle, Subtitle: fmt.Sprintf("%s · round %d of %d", sp.Name, pairing.Round+1, sp.Rounds()),
		ApplicationID: pairing.ApplicationID, StageID: sp.StageID, JobID: sp.JobID, CandidateName: pairing.CandidateName,
		SprintID: sp.ID, Round: pairing.Round, PairingID: pairing.ID,
		OpensAt: clock.RoundStart(pairing.Round), ClosesAt: clock.RoundStart(pairing.Round + 1),
	}
	switch p.Kind {
	case PrincipalOrgUser:
		user, err := tx.Q.GetOrgUser(ctx, db.GetOrgUserParams{ID: p.UserID, OrgID: p.OrgID})
		if err != nil {
			return Room{}, err
		}
		out.Name, out.Role = user.Name, RoleObserver
		if p.UserID == pairing.InterviewerID {
			out.Role = RoleInterviewer
		} else if !p.HasRole(RoleRecruiter) && !p.HasRole(RoleAdmin) {
			return Room{}, ErrForbidden
		}
	case PrincipalMagicLink:
		if p.MagicPurpose != LinkSprint {
			return Room{}, ErrForbidden
		}
		c, err := tx.Q.GetSprintCandidate(ctx, p.SubjectID)
		if err != nil || c.SprintID != sp.ID || c.ApplicationID != pairing.ApplicationID {
			return Room{}, ErrForbidden
		}
		out.Name, out.Role = c.CandidateName, RoleCandidate
	default:
		return Room{}, ErrForbidden
	}
	out.Open = clock.RoomOpen(pairing.Round, s.Now())
	return out, nil
}

// Join authorises and subscribes p to the room's stream.
func (s *RoomService) Join(ctx context.Context, p Principal, key RoomKey) (Room, *RoomMember, error) {
	room, err := s.Open(ctx, p, key)
	if err != nil {
		return Room{}, nil, err
	}
	if !room.Open {
		return Room{}, nil, ErrRoomClosed
	}
	member := s.Hub.Join(key.String(), room.Name, room.Role, room.Language, room.Source)
	return room, member, nil
}

// Signal relays a signalling message from the peer to another or to all.
func (s *RoomService) Signal(ctx context.Context, p Principal, key RoomKey, from, to string, data json.RawMessage) error {
	if _, err := s.Open(ctx, p, key); err != nil {
		return err
	}
	if !s.Hub.Signal(key.String(), from, to, data) {
		return ErrNotPeer
	}
	return nil
}

// Doc appends a shared-editor update. It reports whether the client should
// answer with a snapshot to compact the room's log, and fails with
// ErrDocLogFull once the log cannot take more without one.
func (s *RoomService) Doc(ctx context.Context, p Principal, key RoomKey, from string, update []byte, snapshot bool) (wantSnapshot bool, err error) {
	if _, err := s.Open(ctx, p, key); err != nil {
		return false, err
	}
	return s.Hub.Doc(key.String(), from, update, snapshot)
}

// SaveCode persists the editor's language and text.
func (s *RoomService) SaveCode(ctx context.Context, p Principal, key RoomKey, from, language, source string) error {
	language = domain.NormalizeLanguageID(language)
	if _, ok := domain.LanguageByID(language); !ok {
		return ErrRoomLanguage
	}
	if len(source) > MaxRoomSource {
		return ErrSourceTooBig
	}
	if _, err := s.Open(ctx, p, key); err != nil {
		return err
	}
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		_, err := tx.Q.UpsertInterviewRoom(ctx, db.UpsertInterviewRoomParams{
			OrgID: p.OrgID, Kind: key.Kind, SubjectID: key.ID, Language: language, Source: source,
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("save room code: %w", err)
	}
	s.Hub.Code(key.String(), from, language, source)
	return nil
}

// Code is the editor as last saved, for the application page. Any org user
// may read it.
func (s *RoomService) Code(ctx context.Context, p Principal, key RoomKey) (RoomCode, error) {
	if p.Kind != PrincipalOrgUser {
		return RoomCode{}, ErrForbidden
	}
	var out RoomCode
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		row, err := tx.Q.GetInterviewRoom(ctx, db.GetInterviewRoomParams{Kind: key.Kind, SubjectID: key.ID})
		if err != nil {
			return err
		}
		out = RoomCode{Language: row.Language, Source: row.Source, UpdatedAt: row.UpdatedAt.Time.UTC()}
		return nil
	})
	if err != nil {
		return RoomCode{}, wrapRoom("room code", err)
	}
	return out, nil
}

// Run executes the editor's text with the given stdin through the runner
// and returns what it printed. Nothing is stored.
func (s *RoomService) Run(ctx context.Context, p Principal, key RoomKey, in RunInput) (RunResult, error) {
	if s.exec == nil {
		return RunResult{}, ErrNoExecutor
	}
	language := domain.NormalizeLanguageID(in.Language)
	lang, ok := domain.LanguageByID(language)
	if !ok || lang.Kind != domain.ProblemKindCode {
		return RunResult{}, ErrRoomLanguage
	}
	if _, err := s.Open(ctx, p, key); err != nil {
		return RunResult{}, err
	}
	limits := server.DefaultLimits
	limits.WallMs = RunWallMs
	limits.CPUMs = RunWallMs
	req := server.Request{
		ID: uuid.NewString(), Language: language, Source: in.Source,
		Tests:  []server.Test{{ID: "stdin", Input: in.Stdin, Weight: 1}},
		Limits: limits,
	}
	res, err := s.exec.Execute(ctx, req)
	if err != nil {
		return RunResult{}, fmt.Errorf("run in room: %w", err)
	}
	out := RunResult{Status: res.Status, CompileOutput: res.CompileOutput}
	if len(res.Results) > 0 {
		r := res.Results[0]
		out.Stdout, out.Stderr, out.TimeMs = r.StdoutTail, r.StderrTail, r.TimeMs
		if res.Status == server.StatusOK && r.Status == server.TestTimeout {
			out.Status = server.StatusTimeout
		}
	}
	return out, nil
}

// ClosedMessage says why a room is not open right now, for the waiting page.
func (r Room) ClosedMessage(now time.Time) string {
	if now.Before(r.OpensAt) {
		return "The room opens at " + r.OpensAt.UTC().Format("15:04 UTC") + " on " + r.OpensAt.UTC().Format("Mon 2 Jan") + "."
	}
	return "This room has closed."
}

func wrapRoom(what string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pgx.ErrNoRows):
		return ErrNotFound
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrForbidden), errors.Is(err, ErrRoomClosed),
		errors.Is(err, ErrRoomKind), errors.Is(err, ErrNotPeer), errors.Is(err, ErrRoomLanguage),
		errors.Is(err, ErrSourceTooBig), errors.Is(err, ErrSprintNotScheduled), errors.Is(err, ErrDocLogFull):
		return err
	}
	return fmt.Errorf("%s: %w", what, err)
}
