package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	pgtypev5 "github.com/jackc/pgx/v5/pgtype"

	"recruiting/internal/domain"
	"recruiting/internal/mail"
	"recruiting/internal/queue"
	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

// Slot outcomes a vetter records; they mirror the interview_slot status check.
const (
	SlotBooked    = "booked"
	SlotCancelled = "cancelled"
	SlotCompleted = "completed"
	SlotNoShow    = "no_show"
)

// BookingHorizon is how far ahead a candidate may book.
const BookingHorizon = 14 * 24 * time.Hour

// BookingLeadTime is the least notice a booking gives the vetter.
const BookingLeadTime = domain.ChangeCutoff

// applicationPath is where the recruiter surface shows an application; the
// vetter's copy of a booking email links there.
const applicationPath = "/app/applications/"

var (
	// ErrSlotTaken is retryable: another booking or a new exception got there
	// first; refresh the list and pick again.
	ErrSlotTaken    = errors.New("service: that time is no longer available; the times have been refreshed")
	ErrTooLate      = errors.New("service: interviews can only be changed more than 2 hours before they start")
	ErrNoVetter     = errors.New("service: no interviewer is assigned to this application yet")
	ErrNotVetter    = errors.New("service: that user is not a vetter")
	ErrNoBooking    = errors.New("service: there is no booking to change")
	ErrBadTimezone  = errors.New("service: unknown timezone")
	ErrBadRule      = errors.New("service: availability rule is invalid")
	ErrBadOutcome   = errors.New("service: unknown slot outcome")
	ErrBadException = errors.New("service: exception must end after it starts")
)

// RuleInput is one weekly block as the availability form posts it.
type RuleInput struct {
	Weekday time.Weekday
	Start   string // "15:04" local
	End     string
}

// AvailabilityInput replaces a vetter's whole weekly schedule.
type AvailabilityInput struct {
	Timezone      string
	SlotMinutes   int
	BufferMinutes int
	Rules         []RuleInput
}

// Rule is one stored weekly block.
type Rule struct {
	ID      uuid.UUID
	Weekday time.Weekday
	Start   string
	End     string
}

// ExceptionRow is one blocked interval as the availability page lists it.
type ExceptionRow struct {
	ID     uuid.UUID
	Start  time.Time
	End    time.Time
	Reason string
}

// VetterSlot is one interview on a vetter's calendar.
type VetterSlot struct {
	ID             uuid.UUID
	ApplicationID  uuid.UUID
	CandidateName  string
	CandidateEmail string
	JobTitle       string
	Start          time.Time
	End            time.Time
	Status         string
}

// Availability is the vetter's availability page.
type Availability struct {
	Timezone      string
	SlotMinutes   int
	BufferMinutes int
	Rules         []Rule
	Exceptions    []ExceptionRow
	Slots         []VetterSlot
}

// BookedSlot is the candidate's current interview.
type BookedSlot struct {
	ID       uuid.UUID
	Start    time.Time
	End      time.Time
	Timezone string // the candidate's zone when they booked
}

// Booking is the candidate's booking page.
type Booking struct {
	ApplicationID uuid.UUID
	CandidateName string
	JobTitle      string
	VetterName    string
	// Video is set when the stage hosts the interview in a room, which the
	// page offers to join from the booking once it opens.
	Video     bool
	Slots     []domain.Slot // bookable, UTC, in start order
	Current   *BookedSlot
	CanChange bool
	// RoomOpen reports whether the current booking's room may be joined
	// right now; RoomOpensAt is when it may.
	RoomOpen    bool
	RoomOpensAt time.Time
}

// ScheduleService is vetter availability and candidate booking.
type ScheduleService struct {
	st      *store.Store
	q       *queue.Client
	baseURL string
	// Now is the clock; tests move it.
	Now func() time.Time
}

// NewScheduleService wires the store, the queue reminders and emails go to,
// and the base URL links are built on. A nil queue sends nothing.
func NewScheduleService(st *store.Store, q *queue.Client, baseURL string) *ScheduleService {
	return &ScheduleService{st: st, q: q, baseURL: strings.TrimRight(baseURL, "/"), Now: time.Now}
}

func requireVetter(p Principal) error {
	if p.Kind != PrincipalOrgUser || !p.HasRole(RoleVetter) {
		return ErrForbidden
	}
	return nil
}

// Availability is the signed-in vetter's schedule, exceptions from now on,
// and upcoming interviews.
func (s *ScheduleService) Availability(ctx context.Context, p Principal) (Availability, error) {
	if err := requireVetter(p); err != nil {
		return Availability{}, err
	}
	now := s.Now()
	var out Availability
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		user, err := tx.Q.GetOrgUser(ctx, db.GetOrgUserParams{ID: p.UserID, OrgID: p.OrgID})
		if err != nil {
			return err
		}
		out.Timezone, out.SlotMinutes, out.BufferMinutes = user.Timezone, 30, 0
		rules, err := tx.Q.ListAvailabilityRules(ctx, p.UserID)
		if err != nil {
			return err
		}
		out.Rules = make([]Rule, 0, len(rules))
		for i, r := range rules {
			if i == 0 {
				out.Timezone, out.SlotMinutes, out.BufferMinutes = r.Timezone, int(r.SlotMinutes), int(r.BufferMinutes)
			}
			out.Rules = append(out.Rules, Rule{ID: r.ID, Weekday: time.Weekday(r.Weekday), Start: clock(r.StartTime), End: clock(r.EndTime)})
		}
		exceptions, err := tx.Q.ListAvailabilityExceptions(ctx, db.ListAvailabilityExceptionsParams{
			VetterID: p.UserID, EndsAt: ts(now), StartsAt: ts(now.Add(365 * 24 * time.Hour)),
		})
		if err != nil {
			return err
		}
		out.Exceptions = make([]ExceptionRow, 0, len(exceptions))
		for _, e := range exceptions {
			out.Exceptions = append(out.Exceptions, ExceptionRow{ID: e.ID, Start: e.StartsAt.Time, End: e.EndsAt.Time, Reason: deref(e.Reason)})
		}
		slots, err := tx.Q.ListVetterSlots(ctx, db.ListVetterSlotsParams{VetterID: p.UserID, EndsAt: ts(now.Add(-7 * 24 * time.Hour))})
		if err != nil {
			return err
		}
		out.Slots = make([]VetterSlot, 0, len(slots))
		for _, r := range slots {
			out.Slots = append(out.Slots, VetterSlot{
				ID: r.ID, ApplicationID: r.ApplicationID.UUID, CandidateName: r.CandidateName, CandidateEmail: r.CandidateEmail,
				JobTitle: r.JobTitle, Start: r.StartsAt.Time, End: r.EndsAt.Time, Status: r.Status,
			})
		}
		return nil
	})
	if err != nil {
		return Availability{}, fmt.Errorf("availability: %w", err)
	}
	return out, nil
}

// SetAvailability replaces the vetter's weekly rules. Every rule shares the
// zone, slot length, and buffer the form carries.
func (s *ScheduleService) SetAvailability(ctx context.Context, p Principal, in AvailabilityInput) error {
	if err := requireVetter(p); err != nil {
		return err
	}
	if !domain.ValidTimezone(in.Timezone) {
		return ErrBadTimezone
	}
	if in.SlotMinutes < 5 || in.SlotMinutes > 240 || in.BufferMinutes < 0 || in.BufferMinutes > 240 {
		return fmt.Errorf("%w: slot length or buffer out of range", ErrBadRule)
	}
	type row struct {
		weekday    int16
		start, end int
	}
	rows := make([]row, 0, len(in.Rules))
	for _, r := range in.Rules {
		start, err1 := parseClock(r.Start)
		end, err2 := parseClock(r.End)
		if err1 != nil || err2 != nil || r.Weekday < time.Sunday || r.Weekday > time.Saturday {
			return fmt.Errorf("%w: %s %s-%s", ErrBadRule, r.Weekday, r.Start, r.End)
		}
		if end <= start {
			return fmt.Errorf("%w: %s %s-%s ends before it starts", ErrBadRule, r.Weekday, r.Start, r.End)
		}
		rows = append(rows, row{weekday: int16(r.Weekday), start: start, end: end})
	}
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		// The vetter's own zone follows the schedule's, so emails and
		// reminders show their times where they set them.
		if err := tx.Q.SetOrgUserTimezone(ctx, db.SetOrgUserTimezoneParams{ID: p.UserID, OrgID: p.OrgID, Timezone: in.Timezone}); err != nil {
			return err
		}
		if err := tx.Q.DeleteAvailabilityRules(ctx, p.UserID); err != nil {
			return err
		}
		for _, r := range rows {
			if _, err := tx.Q.CreateAvailabilityRule(ctx, db.CreateAvailabilityRuleParams{
				OrgID: p.OrgID, VetterID: p.UserID, Weekday: r.weekday,
				StartTime: pgTime(r.start), EndTime: pgTime(r.end), Timezone: in.Timezone,
				BufferMinutes: int32(in.BufferMinutes), SlotMinutes: int32(in.SlotMinutes),
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("set availability: %w", err)
	}
	return nil
}

// AddException blocks [start, end) on the vetter's calendar.
func (s *ScheduleService) AddException(ctx context.Context, p Principal, start, end time.Time, reason string) error {
	if err := requireVetter(p); err != nil {
		return err
	}
	if !end.After(start) {
		return ErrBadException
	}
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		_, err := tx.Q.CreateAvailabilityException(ctx, db.CreateAvailabilityExceptionParams{
			OrgID: p.OrgID, VetterID: p.UserID, StartsAt: ts(start), EndsAt: ts(end), Reason: nullable(strings.TrimSpace(reason)),
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("add exception: %w", err)
	}
	return nil
}

// DeleteException removes one of the vetter's own exceptions.
func (s *ScheduleService) DeleteException(ctx context.Context, p Principal, id uuid.UUID) error {
	if err := requireVetter(p); err != nil {
		return err
	}
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		n, err := tx.Q.DeleteAvailabilityException(ctx, db.DeleteAvailabilityExceptionParams{ID: id, VetterID: p.UserID})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		return nil
	})
	if errors.Is(err, ErrNotFound) {
		return err
	}
	if err != nil {
		return fmt.Errorf("delete exception: %w", err)
	}
	return nil
}

// SetOutcome records how one of the vetter's interviews went: completed or
// no-show. A cancelled slot has no outcome.
func (s *ScheduleService) SetOutcome(ctx context.Context, p Principal, slotID uuid.UUID, outcome string) error {
	if err := requireVetter(p); err != nil {
		return err
	}
	if outcome != SlotCompleted && outcome != SlotNoShow {
		return ErrBadOutcome
	}
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		_, err := tx.Q.SetInterviewSlotOutcome(ctx, db.SetInterviewSlotOutcomeParams{ID: slotID, VetterID: p.UserID, Status: outcome})
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("set slot outcome: %w", err)
	}
	return nil
}

// Assign names the vetter who interviews the application. A recruiter sets
// it; the booking page falls back to the stage's default when it is unset.
func (s *ScheduleService) Assign(ctx context.Context, p Principal, applicationID, vetterID uuid.UUID) error {
	if err := requireRecruiter(p); err != nil {
		return err
	}
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		if _, err := tx.Q.GetOrgUser(ctx, db.GetOrgUserParams{ID: vetterID, OrgID: p.OrgID}); err != nil {
			return err
		}
		roles, err := tx.Q.ListOrgUserRoles(ctx, vetterID)
		if err != nil {
			return err
		}
		if !(Principal{Roles: roles}).HasRole(RoleVetter) {
			return ErrNotVetter
		}
		_, err = tx.Q.SetApplicationVetter(ctx, db.SetApplicationVetterParams{ID: applicationID, VetterID: uuid.NullUUID{UUID: vetterID, Valid: true}})
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if errors.Is(err, ErrNotVetter) {
		return err
	}
	if err != nil {
		return fmt.Errorf("assign vetter: %w", err)
	}
	return nil
}

// bookingContext is what every candidate operation loads: the application,
// its vetter, and the current booking.
type bookingContext struct {
	app     db.Application
	card    db.GetApplicationCardRow
	stage   db.Stage
	vetter  db.OrgUser
	current *db.InterviewSlot
}

// video reports whether the stage hosts the interview in a room.
func (b bookingContext) video() bool { return b.stage.InterviewFormat == domain.FormatVideo }

// joinNote is the line a confirmation carries for a video interview.
func (b bookingContext) joinNote() string {
	if !b.video() {
		return ""
	}
	return "This interview is by video. The room opens from your booking page ten minutes before the start; join from a laptop with a camera and a microphone."
}

func (s *ScheduleService) loadBooking(ctx context.Context, tx *store.Tx, p Principal) (bookingContext, error) {
	var b bookingContext
	var err error
	if b.app, err = tx.Q.GetApplicationForUpdate(ctx, p.SubjectID); err != nil {
		return b, err
	}
	if b.app.Status != string(domain.StatusActive) {
		return b, ErrNotActive
	}
	if b.card, err = tx.Q.GetApplicationCard(ctx, b.app.ID); err != nil {
		return b, err
	}
	if b.stage, err = tx.Q.GetStage(ctx, b.app.StageID); err != nil {
		return b, err
	}
	vetterID := b.app.VetterID
	if !vetterID.Valid {
		if !b.stage.DefaultVetterID.Valid {
			return b, ErrNoVetter
		}
		vetterID = b.stage.DefaultVetterID
		// Remembered so the vetter stays the same if the stage default moves.
		if b.app, err = tx.Q.SetApplicationVetter(ctx, db.SetApplicationVetterParams{ID: b.app.ID, VetterID: vetterID}); err != nil {
			return b, err
		}
	}
	if b.vetter, err = tx.Q.GetOrgUser(ctx, db.GetOrgUserParams{ID: vetterID.UUID, OrgID: p.OrgID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return b, ErrNoVetter
		}
		return b, err
	}
	cur, err := tx.Q.GetBookedSlotForApplication(ctx, uuid.NullUUID{UUID: b.app.ID, Valid: true})
	switch {
	case err == nil:
		b.current = &cur
	case errors.Is(err, pgx.ErrNoRows):
	default:
		return b, err
	}
	return b, nil
}

// window is the interval a candidate may book in.
func (s *ScheduleService) window() domain.Window {
	now := s.Now()
	return domain.Window{From: now.Add(BookingLeadTime), To: now.Add(BookingHorizon)}
}

// openSlots generates the vetter's bookable slots in window. A rescheduling
// candidate's own booking is not an obstacle; it is released on change.
func openSlots(ctx context.Context, tx *store.Tx, vetterID uuid.UUID, window domain.Window, ownSlot uuid.UUID) ([]domain.Slot, error) {
	rules, err := tx.Q.ListAvailabilityRules(ctx, vetterID)
	if err != nil {
		return nil, err
	}
	drules := make([]domain.AvailabilityRule, 0, len(rules))
	for _, r := range rules {
		drules = append(drules, domain.AvailabilityRule{
			Weekday: time.Weekday(r.Weekday), StartMinute: minutes(r.StartTime), EndMinute: minutes(r.EndTime),
			Timezone: r.Timezone, SlotMinutes: int(r.SlotMinutes), BufferMinutes: int(r.BufferMinutes),
			ValidFrom: dateOrZero(r.ValidFrom), ValidTo: dateOrZero(r.ValidTo),
		})
	}
	// Bookings and exceptions a day either side still matter through buffers
	// and local-day edges.
	pad := 24 * time.Hour
	exceptions, err := tx.Q.ListAvailabilityExceptions(ctx, db.ListAvailabilityExceptionsParams{
		VetterID: vetterID, EndsAt: ts(window.From.Add(-pad)), StartsAt: ts(window.To.Add(pad)),
	})
	if err != nil {
		return nil, err
	}
	dex := make([]domain.Exception, 0, len(exceptions))
	for _, e := range exceptions {
		dex = append(dex, domain.Exception{Start: e.StartsAt.Time, End: e.EndsAt.Time})
	}
	booked, err := tx.Q.ListInterviewSlots(ctx, db.ListInterviewSlotsParams{
		VetterID: vetterID, EndsAt: ts(window.From.Add(-pad)), StartsAt: ts(window.To.Add(pad)),
	})
	if err != nil {
		return nil, err
	}
	dbooked := make([]domain.Booking, 0, len(booked))
	for _, b := range booked {
		if b.ID == ownSlot {
			continue
		}
		dbooked = append(dbooked, domain.Booking{Start: b.StartsAt.Time, End: b.EndsAt.Time})
	}
	return domain.GenerateSlots(drules, dex, dbooked, window)
}

// Booking is the candidate's page: open slots and their current interview.
func (s *ScheduleService) Booking(ctx context.Context, p Principal) (Booking, error) {
	if p.Kind != PrincipalMagicLink || p.MagicPurpose != LinkBook {
		return Booking{}, ErrForbidden
	}
	var out Booking
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		b, err := s.loadBooking(ctx, tx, p)
		if err != nil {
			return err
		}
		out = s.view(b)
		own := uuid.Nil
		if b.current != nil {
			own = b.current.ID
		}
		out.Slots, err = openSlots(ctx, tx, b.vetter.ID, s.window(), own)
		return err
	})
	if err != nil {
		return Booking{}, wrapBooking("booking page", err)
	}
	return out, nil
}

func (s *ScheduleService) view(b bookingContext) Booking {
	out := Booking{
		ApplicationID: b.app.ID, CandidateName: b.card.CandidateName, JobTitle: b.card.JobTitle, VetterName: b.vetter.Name,
		Video: b.video(),
	}
	if b.current != nil {
		out.Current = &BookedSlot{ID: b.current.ID, Start: b.current.StartsAt.Time, End: b.current.EndsAt.Time, Timezone: deref(b.current.CandidateTimezone)}
		now := s.Now()
		out.CanChange = domain.CanChangeBooking(b.current.StartsAt.Time, now)
		out.RoomOpensAt = b.current.StartsAt.Time.Add(-RoomOpensBefore)
		closes := b.current.StartsAt.Time.Add(RoomStaysOpen)
		if b.current.EndsAt.Time.After(closes) {
			closes = b.current.EndsAt.Time
		}
		out.RoomOpen = out.Video && !now.Before(out.RoomOpensAt) && now.Before(closes)
	}
	return out
}

// Book takes the slot starting at start for the candidate, in their zone tz,
// replacing a current booking when there is one. token is the candidate's
// link, so the emails can point back here. The insert runs under the unique
// (vetter, start) index: of two candidates taking one slot at once, the
// second gets ErrSlotTaken and a fresh list.
func (s *ScheduleService) Book(ctx context.Context, p Principal, token string, start time.Time, tz string) (BookedSlot, error) {
	if p.Kind != PrincipalMagicLink || p.MagicPurpose != LinkBook {
		return BookedSlot{}, ErrForbidden
	}
	if !domain.ValidTimezone(tz) {
		return BookedSlot{}, ErrBadTimezone
	}
	start = start.UTC()
	var out BookedSlot
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		b, err := s.loadBooking(ctx, tx, p)
		if err != nil {
			return err
		}
		own := uuid.Nil
		if b.current != nil {
			if !domain.CanChangeBooking(b.current.StartsAt.Time, s.Now()) {
				return ErrTooLate
			}
			own = b.current.ID
		}
		slots, err := openSlots(ctx, tx, b.vetter.ID, s.window(), own)
		if err != nil {
			return err
		}
		var slot domain.Slot
		found := false
		for _, sl := range slots {
			if sl.Start.Equal(start) {
				slot, found = sl, true
				break
			}
		}
		if !found {
			// Taken, blocked, or past: the list the candidate chose from is stale.
			return ErrSlotTaken
		}
		if b.current != nil {
			if err := s.cancelSlot(ctx, tx, *b.current); err != nil {
				return err
			}
		}
		row, err := tx.Q.CreateInterviewSlot(ctx, db.CreateInterviewSlotParams{
			OrgID: p.OrgID, VetterID: b.vetter.ID,
			ApplicationID: uuid.NullUUID{UUID: b.app.ID, Valid: true}, StageID: uuid.NullUUID{UUID: b.app.StageID, Valid: true},
			CandidateTimezone: &tz, StartsAt: ts(slot.Start), EndsAt: ts(slot.End),
		})
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation {
				return ErrSlotTaken
			}
			return err
		}
		out = BookedSlot{ID: row.ID, Start: row.StartsAt.Time, End: row.EndsAt.Time, Timezone: tz}
		if s.q == nil {
			return nil
		}
		bookingURL := s.baseURL + bookingPath + token
		var remindJobs []int64
		for _, r := range domain.ReminderTimes(slot.Start) {
			if !r.At.After(s.Now()) {
				continue
			}
			id, err := s.q.EnqueueAt(ctx, tx, queue.KindInterviewRemind, RemindPayload{
				SlotID: row.ID, Offset: r.Offset, OrgID: p.OrgID, StartsAt: slot.Start, BookingURL: bookingURL,
			}, r.At)
			if err != nil {
				return err
			}
			remindJobs = append(remindJobs, id)
		}
		// The ids are kept on the slot so cancelling or rescheduling can
		// cancel the reminders in the same transaction.
		if err := tx.Q.SetInterviewSlotRemindJobs(ctx, db.SetInterviewSlotRemindJobsParams{ID: row.ID, RemindJobIds: remindJobs}); err != nil {
			return err
		}
		if b.current != nil {
			if err := s.notifyChange(ctx, tx, p.OrgID, b, bookingURL, "rescheduled to "+bothTimes(slot.Start, tz, b.vetter.Timezone)); err != nil {
				return err
			}
		}
		return s.confirm(ctx, tx, p.OrgID, b, slot, tz, bookingURL)
	})
	if err != nil {
		return BookedSlot{}, wrapBooking("book slot", err)
	}
	return out, nil
}

// Cancel drops the candidate's current booking.
func (s *ScheduleService) Cancel(ctx context.Context, p Principal, token string) error {
	if p.Kind != PrincipalMagicLink || p.MagicPurpose != LinkBook {
		return ErrForbidden
	}
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		b, err := s.loadBooking(ctx, tx, p)
		if err != nil {
			return err
		}
		if b.current == nil {
			return ErrNoBooking
		}
		if !domain.CanChangeBooking(b.current.StartsAt.Time, s.Now()) {
			return ErrTooLate
		}
		if err := s.cancelSlot(ctx, tx, *b.current); err != nil {
			return err
		}
		if s.q == nil {
			return nil
		}
		return s.notifyChange(ctx, tx, p.OrgID, b, s.baseURL+bookingPath+token, "cancelled")
	})
	if err != nil {
		return wrapBooking("cancel booking", err)
	}
	return nil
}

// cancelSlot marks the slot cancelled and cancels its pending reminder jobs,
// so a reminder never goes out for a booking that no longer stands.
func (s *ScheduleService) cancelSlot(ctx context.Context, tx *store.Tx, slot db.InterviewSlot) error {
	if _, err := tx.Q.UpdateInterviewSlotStatus(ctx, db.UpdateInterviewSlotStatusParams{ID: slot.ID, Status: SlotCancelled}); err != nil {
		return err
	}
	if s.q == nil || len(slot.RemindJobIds) == 0 {
		return nil
	}
	for _, id := range slot.RemindJobIds {
		if err := s.q.CancelTx(ctx, tx, id); err != nil {
			return err
		}
	}
	return tx.Q.SetInterviewSlotRemindJobs(ctx, db.SetInterviewSlotRemindJobsParams{ID: slot.ID, RemindJobIds: []int64{}})
}

// confirm sends the confirmation to both parties, each in their own zone and
// told the other's.
func (s *ScheduleService) confirm(ctx context.Context, tx *store.Tx, orgID uuid.UUID, b bookingContext, slot domain.Slot, candTZ, bookingURL string) error {
	start := slot.Start
	when := bothTimes(start, candTZ, b.vetter.Timezone)
	// Both sides get the interview as a calendar entry: the file for any
	// calendar, and a Google link for a client that ignores attachments.
	// The UID is the application and stage, so a rescheduled interview
	// replaces the earlier entry rather than sitting beside it.
	candidateEvent := queue.CalendarEvent{
		UID:     "interview-" + b.app.ID.String() + "-" + b.stage.ID.String() + "@recruiting",
		Summary: "Interview: " + b.card.JobTitle, Description: strings.TrimSpace("Interview for " + b.card.JobTitle + ".\n" + b.joinNote()),
		Location: bookingURL, URL: bookingURL, Start: start, End: slot.End,
	}
	if err := s.emailWithCalendar(ctx, tx, orgID, mail.TemplateBookingConfirmation, b.card.CandidateEmail, map[string]any{
		"CandidateName": b.card.CandidateName, "JobTitle": b.card.JobTitle,
		"StartsAt": when, "Timezone": candTZ, "BookingURL": bookingURL, "JoinNote": b.joinNote(),
		"CalendarURL": mail.GoogleCalendarURL(mailEvent(candidateEvent)),
	}, candidateEvent); err != nil {
		return err
	}
	vetterNote := ""
	if b.video() {
		vetterNote = "This interview is by video. Join the room from the application page or your interviews list when it opens."
	}
	applicationURL := s.baseURL + applicationPath + b.app.ID.String()
	vetterEvent := candidateEvent
	vetterEvent.Summary = "Interview: " + b.card.JobTitle + " with " + b.card.CandidateName
	vetterEvent.Description = strings.TrimSpace("Interview with " + b.card.CandidateName + " for " + b.card.JobTitle + ".\n" + vetterNote)
	vetterEvent.Location, vetterEvent.URL = applicationURL, applicationURL
	return s.emailWithCalendar(ctx, tx, orgID, mail.TemplateBookingConfirmation, b.vetter.Email, map[string]any{
		"CandidateName": b.vetter.Name, "JobTitle": b.card.JobTitle + " with " + b.card.CandidateName,
		"StartsAt": bothTimes(start, b.vetter.Timezone, candTZ), "Timezone": b.vetter.Timezone,
		"BookingURL": applicationURL, "JoinNote": vetterNote,
		"CalendarURL": mail.GoogleCalendarURL(mailEvent(vetterEvent)),
	}, vetterEvent)
}

// emailWithCalendar is email with the interview attached as an .ics file.
func (s *ScheduleService) emailWithCalendar(ctx context.Context, tx *store.Tx, orgID uuid.UUID, template, to string, data map[string]any, ev queue.CalendarEvent) error {
	if s.q == nil {
		return nil
	}
	return enqueued(s.q.Enqueue(ctx, tx, queue.KindEmailSend, queue.EmailPayload{Template: template, To: to, OrgID: orgID, Data: data, Calendar: &ev}))
}

// mailEvent is the queue's event as the mail package reads it.
func mailEvent(e queue.CalendarEvent) mail.Event {
	return mail.Event{UID: e.UID, Summary: e.Summary, Description: e.Description, Location: e.Location, URL: e.URL, Start: e.Start, End: e.End}
}

// notifyChange tells both parties the current booking changed.
func (s *ScheduleService) notifyChange(ctx context.Context, tx *store.Tx, orgID uuid.UUID, b bookingContext, bookingURL, change string) error {
	if s.q == nil {
		return nil
	}
	old := bothTimes(b.current.StartsAt.Time, deref(b.current.CandidateTimezone), b.vetter.Timezone)
	if err := s.email(ctx, tx, orgID, mail.TemplateRescheduleCancel, b.card.CandidateEmail, map[string]any{
		"CandidateName": b.card.CandidateName, "JobTitle": b.card.JobTitle,
		"Change": change, "Reason": "It was booked for " + old + ".", "BookingURL": bookingURL,
	}); err != nil {
		return err
	}
	return s.email(ctx, tx, orgID, mail.TemplateRescheduleCancel, b.vetter.Email, map[string]any{
		"CandidateName": b.vetter.Name, "JobTitle": b.card.JobTitle + " with " + b.card.CandidateName,
		"Change": change, "Reason": "It was booked for " + old + ".",
		"BookingURL": s.baseURL + applicationPath + b.app.ID.String(),
	})
}

func (s *ScheduleService) email(ctx context.Context, tx *store.Tx, orgID uuid.UUID, template, to string, data map[string]any) error {
	if s.q == nil {
		return nil
	}
	return enqueued(s.q.Enqueue(ctx, tx, queue.KindEmailSend, queue.EmailPayload{Template: template, To: to, OrgID: orgID, Data: data}))
}

// bothTimes renders one instant in two zones: "Mon 1 Jun 2026 14:00 Europe/Berlin (19:00 Asia/Ho_Chi_Minh)".
func bothTimes(t time.Time, tz, otherTZ string) string {
	out := localTime(t, tz)
	if otherTZ != "" && otherTZ != tz {
		out += " (" + localTime(t, otherTZ) + ")"
	}
	return out
}

func localTime(t time.Time, tz string) string {
	loc, err := time.LoadLocation(tz)
	if err != nil || tz == "" {
		loc, tz = time.UTC, "UTC"
	}
	return t.In(loc).Format("Mon 2 Jan 2006 15:04") + " " + tz
}

func wrapBooking(what string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pgx.ErrNoRows):
		return ErrNotFound
	case errors.Is(err, ErrSlotTaken), errors.Is(err, ErrTooLate), errors.Is(err, ErrNoVetter), errors.Is(err, ErrNoBooking),
		errors.Is(err, ErrBadTimezone), errors.Is(err, ErrNotActive), errors.Is(err, ErrForbidden),
		errors.Is(err, domain.ErrBadTimezone):
		return err
	}
	return fmt.Errorf("%s: %w", what, err)
}

// parseClock reads "15:04" into minutes since midnight.
func parseClock(s string) (int, error) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return 0, err
	}
	return t.Hour()*60 + t.Minute(), nil
}

func pgTime(minutes int) pgtypev5.Time {
	return pgtypev5.Time{Microseconds: int64(minutes) * int64(time.Minute/time.Microsecond), Valid: true}
}

func minutes(t pgtypev5.Time) int {
	return int(t.Microseconds / int64(time.Minute/time.Microsecond))
}

func clock(t pgtypev5.Time) string {
	m := minutes(t)
	return fmt.Sprintf("%02d:%02d", m/60, m%60)
}

func dateOrZero(d pgtypev5.Date) time.Time {
	if !d.Valid {
		return time.Time{}
	}
	return d.Time
}
