//go:build integration

package service_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"recruiting/internal/domain"
	"recruiting/internal/queue"
	"recruiting/internal/service"
)

type scheduleFixture struct {
	*pipelineFixture
	sched    *service.ScheduleService
	vetterID uuid.UUID
	now      time.Time
}

// newScheduleFixture builds a vetter available every day 09:00–17:00 Berlin
// time, moves the application into the interview stage, and assigns the
// vetter through the stage default.
func newScheduleFixture(t *testing.T) *scheduleFixture {
	t.Helper()
	pf := newPipelineFixture(t)
	f := &scheduleFixture{pipelineFixture: pf, vetterID: uuid.New()}
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pf.sys.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`insert into org_user (id, org_id, email, name, timezone) values ($1, $2, $3, 'Vera Vetter', 'Europe/Berlin')`, f.vetterID, pf.orgID, "vet-"+pf.orgID.String()+"@example.com")
	exec(`insert into org_user_role (org_user_id, org_id, role) values ($1, $2, 'vetter')`, f.vetterID, pf.orgID)
	exec(`update stage set default_vetter_id = $1 where id = $2`, f.vetterID, pf.stages[domain.StageInterview])
	exec(`update application set stage_id = $1 where id = $2`, pf.stages[domain.StageInterview], pf.appID)

	q, err := queue.New(pf.st.Pool(), queue.Config{})
	if err != nil {
		t.Fatal(err)
	}
	f.sched = service.NewScheduleService(pf.st, q, "https://example.test/")
	// A fixed Monday so the slots are predictable.
	f.now = time.Date(2026, time.June, 1, 6, 0, 0, 0, time.UTC)
	f.sched.Now = func() time.Time { return f.now }

	vetter := service.Principal{Kind: service.PrincipalOrgUser, OrgID: pf.orgID, UserID: f.vetterID, Roles: []string{service.RoleVetter}}
	var rules []service.RuleInput
	for d := time.Sunday; d <= time.Saturday; d++ {
		rules = append(rules, service.RuleInput{Weekday: d, Start: "09:00", End: "17:00"})
	}
	if err := f.sched.SetAvailability(ctx, vetter, service.AvailabilityInput{Timezone: "Europe/Berlin", SlotMinutes: 30, BufferMinutes: 15, Rules: rules}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *scheduleFixture) candidate() service.Principal {
	return service.Principal{Kind: service.PrincipalMagicLink, OrgID: f.orgID, MagicPurpose: service.LinkBook, SubjectID: f.appID}
}

func (f *scheduleFixture) vetter() service.Principal {
	return service.Principal{Kind: service.PrincipalOrgUser, OrgID: f.orgID, UserID: f.vetterID, Roles: []string{service.RoleVetter}}
}

func TestBookingPageOffersSlotsAndAssignsStageDefaultVetter(t *testing.T) {
	f := newScheduleFixture(t)
	b, err := f.sched.Booking(context.Background(), f.candidate())
	if err != nil {
		t.Fatal(err)
	}
	if b.VetterName != "Vera Vetter" || b.Current != nil || len(b.Slots) == 0 {
		t.Fatalf("booking = %+v", b)
	}
	// 09:00 Berlin is 07:00Z; that's within the 2h lead so the first slot is 08:00Z... 07:00Z+ lead from 06:00Z = 08:00Z.
	if want := time.Date(2026, time.June, 1, 8, 0, 0, 0, time.UTC); !b.Slots[0].Start.Equal(want) {
		t.Fatalf("first slot %v, want %v", b.Slots[0].Start, want)
	}
	var vetter uuid.NullUUID
	if err := f.sys.QueryRow(context.Background(), `select vetter_id from application where id = $1`, f.appID).Scan(&vetter); err != nil {
		t.Fatal(err)
	}
	if !vetter.Valid || vetter.UUID != f.vetterID {
		t.Fatalf("application vetter = %v, want stage default", vetter)
	}
}

func TestBookQueuesRemindersAndConfirmations(t *testing.T) {
	f := newScheduleFixture(t)
	start := time.Date(2026, time.June, 2, 9, 0, 0, 0, time.UTC)
	slot, err := f.sched.Book(context.Background(), f.candidate(), "tok", start, "Asia/Ho_Chi_Minh")
	if err != nil {
		t.Fatal(err)
	}
	if !slot.Start.Equal(start) || slot.Timezone != "Asia/Ho_Chi_Minh" {
		t.Fatalf("slot = %+v", slot)
	}
	if n := f.jobs(t, queue.KindInterviewRemind); n != 2 {
		t.Fatalf("reminder jobs = %d, want 2", n)
	}
	if n := f.jobs(t, queue.KindEmailSend); n != 2 {
		t.Fatalf("confirmation jobs = %d, want 2 (candidate and vetter)", n)
	}
	var scheduled []time.Time
	rows, err := f.sys.Query(context.Background(), `select scheduled_at from river_job where kind = $1 and args->>'payload' like '%' || $2 || '%' order by scheduled_at`, queue.KindInterviewRemind, f.orgID.String())
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var at time.Time
		if err := rows.Scan(&at); err != nil {
			t.Fatal(err)
		}
		scheduled = append(scheduled, at)
	}
	rows.Close()
	if len(scheduled) != 2 || !scheduled[0].Equal(start.Add(-24*time.Hour)) || !scheduled[1].Equal(start.Add(-time.Hour)) {
		t.Fatalf("reminders scheduled at %v", scheduled)
	}

	// Rescheduling cancels the old slot and books a new one; the page shows it.
	b, err := f.sched.Booking(context.Background(), f.candidate())
	if err != nil {
		t.Fatal(err)
	}
	if b.Current == nil || !b.Current.Start.Equal(start) || !b.CanChange {
		t.Fatalf("booking = %+v", b)
	}
	next := start.Add(2 * time.Hour)
	if _, err := f.sched.Book(context.Background(), f.candidate(), "tok", next, "Asia/Ho_Chi_Minh"); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := f.sys.QueryRow(context.Background(), `select status from interview_slot where id = $1`, slot.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != service.SlotCancelled {
		t.Fatalf("old slot status = %s, want cancelled", status)
	}
	// The old slot's reminder now finds nothing booked and does nothing.
	handler := service.RemindHandler(f.st, nil, "https://example.test")
	before := f.jobs(t, queue.KindEmailSend)
	payload, _ := queue.EncodePayload(service.RemindPayload{SlotID: slot.ID, Offset: "24h", OrgID: f.orgID, StartsAt: start, BookingURL: "https://example.test/book/tok"})
	if err := handler(context.Background(), queue.Job{ID: 1, Kind: queue.KindInterviewRemind, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if after := f.jobs(t, queue.KindEmailSend); after != before {
		t.Fatalf("cancelled slot's reminder queued %d emails", after-before)
	}
}

func TestConcurrentBookingsOneWins(t *testing.T) {
	f := newScheduleFixture(t)
	// A second application for a second candidate on the same job.
	app2 := uuid.New()
	candID := uuid.New()
	ctx := context.Background()
	if _, err := f.sys.Exec(ctx, `insert into candidate (id, org_id, email, name) values ($1, $2, $3, 'Bob Builder')`, candID, f.orgID, "bob-"+f.orgID.String()+"@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sys.Exec(ctx, `insert into application (id, org_id, job_id, candidate_id, client_company_id, stage_id, vetter_id)
		select $1, org_id, job_id, $2, client_company_id, stage_id, $3 from application where id = $4`, app2, candID, f.vetterID, f.appID); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, time.June, 2, 9, 0, 0, 0, time.UTC)
	principals := []service.Principal{f.candidate(), {Kind: service.PrincipalMagicLink, OrgID: f.orgID, MagicPurpose: service.LinkBook, SubjectID: app2}}
	results := make([]error, len(principals))
	var wg sync.WaitGroup
	for i, p := range principals {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[i] = f.sched.Book(ctx, p, "tok", start, "UTC")
		}()
	}
	wg.Wait()
	var ok, taken int
	for _, err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, service.ErrSlotTaken):
			taken++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 1 || taken != 1 {
		t.Fatalf("ok=%d taken=%d, want one of each", ok, taken)
	}
}

func TestCancelWithinTwoHoursIsRefused(t *testing.T) {
	f := newScheduleFixture(t)
	start := time.Date(2026, time.June, 2, 9, 0, 0, 0, time.UTC)
	if _, err := f.sched.Book(context.Background(), f.candidate(), "tok", start, "UTC"); err != nil {
		t.Fatal(err)
	}
	f.now = start.Add(-90 * time.Minute)
	if err := f.sched.Cancel(context.Background(), f.candidate(), "tok"); !errors.Is(err, service.ErrTooLate) {
		t.Fatalf("cancel within 2h: %v, want ErrTooLate", err)
	}
	if _, err := f.sched.Book(context.Background(), f.candidate(), "tok", start.Add(24*time.Hour), "UTC"); !errors.Is(err, service.ErrTooLate) {
		t.Fatalf("reschedule within 2h: %v, want ErrTooLate", err)
	}
	f.now = start.Add(-3 * time.Hour)
	if err := f.sched.Cancel(context.Background(), f.candidate(), "tok"); err != nil {
		t.Fatal(err)
	}
	b, err := f.sched.Booking(context.Background(), f.candidate())
	if err != nil {
		t.Fatal(err)
	}
	if b.Current != nil {
		t.Fatalf("booking after cancel still shows %+v", b.Current)
	}
}

func TestVetterRecordsNoShowAndSeesCalendar(t *testing.T) {
	f := newScheduleFixture(t)
	start := time.Date(2026, time.June, 2, 9, 0, 0, 0, time.UTC)
	slot, err := f.sched.Book(context.Background(), f.candidate(), "tok", start, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.sched.SetOutcome(context.Background(), f.vetter(), slot.ID, service.SlotNoShow); err != nil {
		t.Fatal(err)
	}
	av, err := f.sched.Availability(context.Background(), f.vetter())
	if err != nil {
		t.Fatal(err)
	}
	if len(av.Rules) != 7 || av.Timezone != "Europe/Berlin" || av.BufferMinutes != 15 {
		t.Fatalf("availability = %+v", av)
	}
	if len(av.Slots) != 1 || av.Slots[0].Status != service.SlotNoShow || av.Slots[0].CandidateName != "Ada Lovelace" {
		t.Fatalf("slots = %+v", av.Slots)
	}
	if err := f.sched.SetOutcome(context.Background(), f.principal(service.RoleRecruiter), slot.ID, service.SlotNoShow); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("recruiter set outcome: %v", err)
	}
}

func TestExceptionHidesSlotsAndAssignNeedsRecruiter(t *testing.T) {
	f := newScheduleFixture(t)
	ctx := context.Background()
	// Block all of Tuesday 2 June.
	if err := f.sched.AddException(ctx, f.vetter(), time.Date(2026, 6, 1, 22, 0, 0, 0, time.UTC), time.Date(2026, 6, 2, 22, 0, 0, 0, time.UTC), "off"); err != nil {
		t.Fatal(err)
	}
	b, err := f.sched.Booking(ctx, f.candidate())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range b.Slots {
		if s.Start.Day() == 2 {
			t.Fatalf("slot %v offered on a blocked day", s.Start)
		}
	}
	if _, err := f.sched.Book(ctx, f.candidate(), "tok", time.Date(2026, 6, 2, 9, 0, 0, 0, time.UTC), "UTC"); !errors.Is(err, service.ErrSlotTaken) {
		t.Fatalf("booking a blocked slot: %v", err)
	}
	if err := f.sched.Assign(ctx, f.vetter(), f.appID, f.vetterID); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("vetter assign: %v", err)
	}
	if err := f.sched.Assign(ctx, f.principal(service.RoleRecruiter), f.appID, f.vetterID); err != nil {
		t.Fatal(err)
	}
}

func TestCancelledSlotCanBeRebookedByAnotherCandidate(t *testing.T) {
	f := newScheduleFixture(t)
	ctx := context.Background()
	start := time.Date(2026, time.June, 2, 9, 0, 0, 0, time.UTC)
	if _, err := f.sched.Book(ctx, f.candidate(), "tok", start, "UTC"); err != nil {
		t.Fatal(err)
	}
	if err := f.sched.Cancel(ctx, f.candidate(), "tok"); err != nil {
		t.Fatal(err)
	}
	app2, candID := uuid.New(), uuid.New()
	if _, err := f.sys.Exec(ctx, `insert into candidate (id, org_id, email, name) values ($1, $2, $3, 'Bob Builder')`, candID, f.orgID, "bob-"+f.orgID.String()+"@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sys.Exec(ctx, `insert into application (id, org_id, job_id, candidate_id, client_company_id, stage_id, vetter_id)
		select $1, org_id, job_id, $2, client_company_id, stage_id, $3 from application where id = $4`, app2, candID, f.vetterID, f.appID); err != nil {
		t.Fatal(err)
	}
	other := service.Principal{Kind: service.PrincipalMagicLink, OrgID: f.orgID, MagicPurpose: service.LinkBook, SubjectID: app2}
	if _, err := f.sched.Book(ctx, other, "tok2", start, "UTC"); err != nil {
		t.Fatalf("rebooking a cancelled time: %v", err)
	}
}

func TestReminderSkipsSlotAtAnotherTimeAndSetAvailabilityMovesUserTimezone(t *testing.T) {
	f := newScheduleFixture(t)
	ctx := context.Background()
	start := time.Date(2026, time.June, 2, 9, 0, 0, 0, time.UTC)
	slot, err := f.sched.Book(ctx, f.candidate(), "tok", start, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	handler := service.RemindHandler(f.st, nil, "https://example.test")
	before := f.jobs(t, queue.KindEmailSend)
	payload, _ := queue.EncodePayload(service.RemindPayload{SlotID: slot.ID, Offset: "24h", OrgID: f.orgID, StartsAt: start.Add(time.Hour), BookingURL: "u"})
	if err := handler(ctx, queue.Job{ID: 2, Kind: queue.KindInterviewRemind, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if after := f.jobs(t, queue.KindEmailSend); after != before {
		t.Fatalf("reminder for a moved slot queued %d emails", after-before)
	}
	if err := f.sched.SetAvailability(ctx, f.vetter(), service.AvailabilityInput{Timezone: "Asia/Tokyo", SlotMinutes: 30, Rules: nil}); err != nil {
		t.Fatal(err)
	}
	var tz string
	if err := f.sys.QueryRow(ctx, `select timezone from org_user where id = $1`, f.vetterID).Scan(&tz); err != nil {
		t.Fatal(err)
	}
	if tz != "Asia/Tokyo" {
		t.Fatalf("org_user.timezone = %s, want Asia/Tokyo", tz)
	}
	if err := f.sched.Assign(ctx, f.principal(service.RoleRecruiter), f.appID, f.userID); !errors.Is(err, service.ErrNotVetter) {
		t.Fatalf("assigning a non-vetter: %v", err)
	}
}

// liveReminders counts the org's reminder jobs river would still work.
func (f *scheduleFixture) liveReminders(t *testing.T) int {
	t.Helper()
	var n int
	err := f.sys.QueryRow(context.Background(),
		`select count(*) from river_job where kind = $1 and state in ('available', 'scheduled', 'retryable') and args->>'payload' like '%' || $2 || '%'`,
		queue.KindInterviewRemind, f.orgID.String()).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCancelAndRescheduleCancelReminderJobs(t *testing.T) {
	f := newScheduleFixture(t)
	ctx := context.Background()
	start := time.Date(2026, time.June, 2, 9, 0, 0, 0, time.UTC)
	if _, err := f.sched.Book(ctx, f.candidate(), "tok", start, "UTC"); err != nil {
		t.Fatal(err)
	}
	if n := f.liveReminders(t); n != 2 {
		t.Fatalf("live reminders after book = %d, want 2", n)
	}
	// Rescheduling cancels the old slot's reminders and queues the new slot's.
	if _, err := f.sched.Book(ctx, f.candidate(), "tok", start.Add(time.Hour), "UTC"); err != nil {
		t.Fatal(err)
	}
	if n := f.liveReminders(t); n != 2 {
		t.Fatalf("live reminders after reschedule = %d, want 2", n)
	}
	if n := f.jobs(t, queue.KindInterviewRemind); n != 4 {
		t.Fatalf("reminder jobs total = %d, want 4", n)
	}
	if err := f.sched.Cancel(ctx, f.candidate(), "tok"); err != nil {
		t.Fatal(err)
	}
	if n := f.liveReminders(t); n != 0 {
		t.Fatalf("live reminders after cancel = %d, want 0", n)
	}
	var ids []int64
	if err := f.sys.QueryRow(ctx, `select remind_job_ids from interview_slot where org_id = $1 and status = 'cancelled' order by created_at desc limit 1`, f.orgID).Scan(&ids); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("cancelled slot still holds job ids %v", ids)
	}
}
