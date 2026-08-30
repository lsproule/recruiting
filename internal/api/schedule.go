package api

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"recruiting/internal/service"
)

// AvailabilityRule is one weekly window a vetter offers, in their own zone.
type AvailabilityRule struct {
	ID      uuid.UUID `json:"id,omitempty"`
	Weekday int       `json:"weekday" minimum:"0" maximum:"6" doc:"0 is Sunday"`
	Start   string    `json:"start" pattern:"^[0-9]{2}:[0-9]{2}$" doc:"Local time, HH:MM"`
	End     string    `json:"end" pattern:"^[0-9]{2}:[0-9]{2}$"`
}

// AvailabilityException is a window the vetter has blocked out.
type AvailabilityException struct {
	ID     uuid.UUID `json:"id"`
	Start  time.Time `json:"start"`
	End    time.Time `json:"end"`
	Reason string    `json:"reason,omitempty"`
}

// Slot is one interview on a vetter's calendar.
type Slot struct {
	ID             uuid.UUID `json:"id"`
	ApplicationID  uuid.UUID `json:"application_id"`
	CandidateName  string    `json:"candidate_name"`
	CandidateEmail string    `json:"candidate_email"`
	JobTitle       string    `json:"job_title"`
	Start          time.Time `json:"start"`
	End            time.Time `json:"end"`
	Status         string    `json:"status"`
}

// Availability is a vetter's whole schedule: the rules that generate slots,
// the exceptions that block them, and the interviews already booked.
type Availability struct {
	Timezone      string                  `json:"timezone"`
	SlotMinutes   int                     `json:"slot_minutes"`
	BufferMinutes int                     `json:"buffer_minutes"`
	Rules         []AvailabilityRule      `json:"rules"`
	Exceptions    []AvailabilityException `json:"exceptions"`
	Slots         []Slot                  `json:"slots"`
}

// BookableSlot is one open time offered to a candidate, in UTC.
type BookableSlot struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// BookedSlot is the interview a candidate holds.
type BookedSlot struct {
	ID       uuid.UUID `json:"id"`
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	Timezone string    `json:"timezone" doc:"The candidate's zone when they booked"`
}

func availabilityView(a service.Availability) Availability {
	out := Availability{
		Timezone: a.Timezone, SlotMinutes: a.SlotMinutes, BufferMinutes: a.BufferMinutes,
		Rules:      make([]AvailabilityRule, 0, len(a.Rules)),
		Exceptions: make([]AvailabilityException, 0, len(a.Exceptions)),
		Slots:      slotViews(a.Slots),
	}
	for _, r := range a.Rules {
		out.Rules = append(out.Rules, AvailabilityRule{ID: r.ID, Weekday: int(r.Weekday), Start: r.Start, End: r.End})
	}
	for _, e := range a.Exceptions {
		out.Exceptions = append(out.Exceptions, AvailabilityException{ID: e.ID, Start: e.Start, End: e.End, Reason: e.Reason})
	}
	return out
}

func slotViews(in []service.VetterSlot) []Slot {
	out := make([]Slot, 0, len(in))
	for _, s := range in {
		out = append(out, Slot{
			ID: s.ID, ApplicationID: s.ApplicationID, CandidateName: s.CandidateName,
			CandidateEmail: s.CandidateEmail, JobTitle: s.JobTitle,
			Start: s.Start, End: s.End, Status: s.Status,
		})
	}
	return out
}

type scheduleHandlers struct{ d Deps }

func (m *mounter) mountSchedule() {
	h := scheduleHandlers{d: m.d}
	register(m, accessOrg, huma.Operation{
		OperationID: "get-availability", Method: http.MethodGet, Path: "/availability",
		Summary: "The caller's interview availability", Tags: []string{"availability"},
	}, h.availability)
	register(m, accessOrg, huma.Operation{
		OperationID: "set-availability", Method: http.MethodPut, Path: "/availability",
		Summary: "Replace the caller's weekly availability", Tags: []string{"availability"},
	}, h.setAvailability)
	register(m, accessOrg, huma.Operation{
		OperationID: "create-availability-exception", Method: http.MethodPost, Path: "/availability/exceptions",
		Summary: "Block out a window", Tags: []string{"availability"}, DefaultStatus: http.StatusCreated,
	}, h.addException)
	register(m, accessOrg, huma.Operation{
		OperationID: "delete-availability-exception", Method: http.MethodDelete, Path: "/availability/exceptions/{exception_id}",
		Summary: "Remove a blocked window", Tags: []string{"availability"}, DefaultStatus: http.StatusNoContent,
	}, h.deleteException)
	register(m, accessOrg, huma.Operation{
		OperationID: "list-slots", Method: http.MethodGet, Path: "/slots",
		Summary: "Interviews on the caller's calendar", Tags: []string{"slots"},
	}, h.slots)
	register(m, accessOrg, huma.Operation{
		OperationID: "set-slot-outcome", Method: http.MethodPost, Path: "/slots/{slot_id}/outcome",
		Summary: "Record whether an interview happened", Tags: []string{"slots"}, DefaultStatus: http.StatusNoContent,
	}, h.outcome)
	register(m, accessOrg, huma.Operation{
		OperationID: "assign-vetter", Method: http.MethodPut, Path: "/applications/{application_id}/vetter",
		Summary: "Assign the interviewer an application books with", Tags: []string{"slots"}, DefaultStatus: http.StatusNoContent,
	}, h.assign)

	register(m, accessLink, huma.Operation{
		OperationID: "get-booking", Method: http.MethodGet, Path: "/bookings/{token}",
		Summary: "Open times and the booking the candidate holds", Tags: []string{"bookings"},
	}, h.booking)
	register(m, accessLink, huma.Operation{
		OperationID: "create-booking", Method: http.MethodPost, Path: "/bookings/{token}",
		Summary: "Book or move the interview", Tags: []string{"bookings"}, DefaultStatus: http.StatusCreated,
	}, h.book)
	register(m, accessLink, huma.Operation{
		OperationID: "cancel-booking", Method: http.MethodDelete, Path: "/bookings/{token}",
		Summary: "Cancel the interview", Tags: []string{"bookings"}, DefaultStatus: http.StatusNoContent,
	}, h.cancel)
}

type availabilityOutput struct{ Body Availability }

type setAvailabilityInput struct {
	Body struct {
		Timezone      string             `json:"timezone" minLength:"1" doc:"IANA zone name"`
		SlotMinutes   int                `json:"slot_minutes" minimum:"1"`
		BufferMinutes int                `json:"buffer_minutes" minimum:"0"`
		Rules         []AvailabilityRule `json:"rules"`
	}
}

type addExceptionInput struct {
	Body struct {
		Start  time.Time `json:"start"`
		End    time.Time `json:"end"`
		Reason string    `json:"reason,omitempty"`
	}
}

type exceptionInput struct {
	ExceptionID uuid.UUID `path:"exception_id"`
}

type exceptionOutput struct {
	Body struct {
		Exceptions []AvailabilityException `json:"exceptions"`
	}
}

type slotsOutput struct {
	Body struct {
		Slots []Slot `json:"slots"`
	}
}

type slotOutcomeInput struct {
	SlotID uuid.UUID `path:"slot_id"`
	Body   struct {
		Outcome string `json:"outcome"`
	}
}

type assignVetterInput struct {
	ApplicationID uuid.UUID `path:"application_id"`
	Body          struct {
		VetterID uuid.UUID `json:"vetter_id"`
	}
}

type bookingTokenInput struct {
	Token string `path:"token"`
}

type bookingOutput struct {
	Body struct {
		ApplicationID uuid.UUID      `json:"application_id"`
		CandidateName string         `json:"candidate_name"`
		JobTitle      string         `json:"job_title"`
		VetterName    string         `json:"vetter_name"`
		Slots         []BookableSlot `json:"slots"`
		Current       *BookedSlot    `json:"current,omitempty"`
		CanChange     bool           `json:"can_change"`
	}
}

type bookInput struct {
	Token string `path:"token"`
	Body  struct {
		Start    time.Time `json:"start" doc:"Start of one of the offered slots"`
		Timezone string    `json:"timezone" minLength:"1" doc:"IANA zone name the candidate booked in"`
	}
}

type bookedSlotOutput struct{ Body BookedSlot }

func (h scheduleHandlers) availability(ctx context.Context, _ *struct{}) (*availabilityOutput, error) {
	a, err := h.d.Schedule.Availability(ctx, principal(ctx))
	if err != nil {
		return nil, problemDetail(err)
	}
	return &availabilityOutput{Body: availabilityView(a)}, nil
}

func (h scheduleHandlers) setAvailability(ctx context.Context, in *setAvailabilityInput) (*availabilityOutput, error) {
	rules := make([]service.RuleInput, 0, len(in.Body.Rules))
	for _, r := range in.Body.Rules {
		rules = append(rules, service.RuleInput{Weekday: time.Weekday(r.Weekday), Start: r.Start, End: r.End})
	}
	p := principal(ctx)
	err := h.d.Schedule.SetAvailability(ctx, p, service.AvailabilityInput{
		Timezone: in.Body.Timezone, SlotMinutes: in.Body.SlotMinutes,
		BufferMinutes: in.Body.BufferMinutes, Rules: rules,
	})
	if err != nil {
		return nil, problemDetail(err)
	}
	return h.availability(ctx, nil)
}

func (h scheduleHandlers) addException(ctx context.Context, in *addExceptionInput) (*exceptionOutput, error) {
	p := principal(ctx)
	if err := h.d.Schedule.AddException(ctx, p, in.Body.Start, in.Body.End, in.Body.Reason); err != nil {
		return nil, problemDetail(err)
	}
	return h.exceptions(ctx)
}

func (h scheduleHandlers) deleteException(ctx context.Context, in *exceptionInput) (*struct{}, error) {
	if err := h.d.Schedule.DeleteException(ctx, principal(ctx), in.ExceptionID); err != nil {
		return nil, problemDetail(err)
	}
	return nil, nil
}

// exceptions re-reads the schedule so a write answers with the list it left
// behind rather than the caller having to fetch it again.
func (h scheduleHandlers) exceptions(ctx context.Context) (*exceptionOutput, error) {
	a, err := h.d.Schedule.Availability(ctx, principal(ctx))
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &exceptionOutput{}
	out.Body.Exceptions = availabilityView(a).Exceptions
	return out, nil
}

func (h scheduleHandlers) slots(ctx context.Context, _ *struct{}) (*slotsOutput, error) {
	a, err := h.d.Schedule.Availability(ctx, principal(ctx))
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &slotsOutput{}
	out.Body.Slots = slotViews(a.Slots)
	return out, nil
}

func (h scheduleHandlers) outcome(ctx context.Context, in *slotOutcomeInput) (*struct{}, error) {
	if err := h.d.Schedule.SetOutcome(ctx, principal(ctx), in.SlotID, in.Body.Outcome); err != nil {
		return nil, problemDetail(err)
	}
	return nil, nil
}

func (h scheduleHandlers) assign(ctx context.Context, in *assignVetterInput) (*struct{}, error) {
	if err := h.d.Schedule.Assign(ctx, principal(ctx), in.ApplicationID, in.Body.VetterID); err != nil {
		return nil, problemDetail(err)
	}
	return nil, nil
}

func (h scheduleHandlers) booking(ctx context.Context, _ *bookingTokenInput) (*bookingOutput, error) {
	b, err := h.d.Schedule.Booking(ctx, principal(ctx))
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &bookingOutput{}
	out.Body.ApplicationID, out.Body.CandidateName = b.ApplicationID, b.CandidateName
	out.Body.JobTitle, out.Body.VetterName, out.Body.CanChange = b.JobTitle, b.VetterName, b.CanChange
	out.Body.Slots = make([]BookableSlot, 0, len(b.Slots))
	for _, s := range b.Slots {
		out.Body.Slots = append(out.Body.Slots, BookableSlot{Start: s.Start, End: s.End})
	}
	if b.Current != nil {
		out.Body.Current = &BookedSlot{ID: b.Current.ID, Start: b.Current.Start, End: b.Current.End, Timezone: b.Current.Timezone}
	}
	return out, nil
}

func (h scheduleHandlers) book(ctx context.Context, in *bookInput) (*bookedSlotOutput, error) {
	s, err := h.d.Schedule.Book(ctx, principal(ctx), in.Token, in.Body.Start, in.Body.Timezone)
	if err != nil {
		return nil, problemDetail(err)
	}
	return &bookedSlotOutput{Body: BookedSlot{ID: s.ID, Start: s.Start, End: s.End, Timezone: s.Timezone}}, nil
}

func (h scheduleHandlers) cancel(ctx context.Context, in *bookingTokenInput) (*struct{}, error) {
	if err := h.d.Schedule.Cancel(ctx, principal(ctx), in.Token); err != nil {
		return nil, problemDetail(err)
	}
	return nil, nil
}
