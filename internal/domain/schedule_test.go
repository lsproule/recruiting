package domain_test

import (
	"testing"
	"time"

	"recruiting/internal/domain"
)

func utc(y int, m time.Month, d, h, min int) time.Time {
	return time.Date(y, m, d, h, min, 0, 0, time.UTC)
}

func berlinRule(weekday time.Weekday) domain.AvailabilityRule {
	return domain.AvailabilityRule{
		Weekday: weekday, StartMinute: 9 * 60, EndMinute: 11 * 60,
		Timezone: "Europe/Berlin", SlotMinutes: 30, BufferMinutes: 30,
	}
}

func starts(slots []domain.Slot) []time.Time {
	out := make([]time.Time, 0, len(slots))
	for _, s := range slots {
		out = append(out, s.Start)
	}
	return out
}

func sameStarts(t *testing.T, got []domain.Slot, want ...time.Time) {
	t.Helper()
	g := starts(got)
	if len(g) != len(want) {
		t.Fatalf("slots = %v, want %v", g, want)
	}
	for i := range want {
		if !g[i].Equal(want[i]) {
			t.Fatalf("slot %d = %v, want %v", i, g[i], want[i])
		}
	}
}

func TestGenerateSlotsAcrossDSTBoundaryKeepsWallClock(t *testing.T) {
	// Berlin leaves DST on Sunday 2026-10-25: 09:00 local is 07:00Z on the
	// Saturday and 08:00Z on the Monday after.
	rules := []domain.AvailabilityRule{berlinRule(time.Saturday), berlinRule(time.Monday)}
	window := domain.Window{From: utc(2026, time.October, 24, 0, 0), To: utc(2026, time.October, 27, 0, 0)}
	slots, err := domain.GenerateSlots(rules, nil, nil, window)
	if err != nil {
		t.Fatal(err)
	}
	sameStarts(t, slots,
		utc(2026, time.October, 24, 7, 0), utc(2026, time.October, 24, 7, 30),
		utc(2026, time.October, 24, 8, 0), utc(2026, time.October, 24, 8, 30),
		utc(2026, time.October, 26, 8, 0), utc(2026, time.October, 26, 8, 30),
		utc(2026, time.October, 26, 9, 0), utc(2026, time.October, 26, 9, 30),
	)
	for _, s := range slots {
		if s.End.Sub(s.Start) != 30*time.Minute {
			t.Fatalf("slot %v has length %v", s.Start, s.End.Sub(s.Start))
		}
		if s.Start.Location() != time.UTC {
			t.Fatalf("slot %v is not in UTC", s.Start)
		}
	}
}

func TestGenerateSlotsBufferRemovesAdjacentSlots(t *testing.T) {
	rules := []domain.AvailabilityRule{berlinRule(time.Monday)} // 07:00Z–09:00Z in summer
	window := domain.Window{From: utc(2026, time.June, 1, 0, 0), To: utc(2026, time.June, 2, 0, 0)}
	booked := []domain.Booking{{Start: utc(2026, time.June, 1, 8, 0), End: utc(2026, time.June, 1, 8, 30)}}
	slots, err := domain.GenerateSlots(rules, nil, booked, window)
	if err != nil {
		t.Fatal(err)
	}
	// 07:30 and 08:30 sit inside the 30-minute buffer around the booking.
	sameStarts(t, slots, utc(2026, time.June, 1, 7, 0))
}

func TestGenerateSlotsExceptionBlocksDay(t *testing.T) {
	rules := []domain.AvailabilityRule{berlinRule(time.Monday), berlinRule(time.Tuesday)}
	window := domain.Window{From: utc(2026, time.June, 1, 0, 0), To: utc(2026, time.June, 3, 0, 0)}
	exceptions := []domain.Exception{{Start: utc(2026, time.May, 31, 22, 0), End: utc(2026, time.June, 1, 22, 0)}}
	slots, err := domain.GenerateSlots(rules, exceptions, nil, window)
	if err != nil {
		t.Fatal(err)
	}
	sameStarts(t, slots,
		utc(2026, time.June, 2, 7, 0), utc(2026, time.June, 2, 7, 30),
		utc(2026, time.June, 2, 8, 0), utc(2026, time.June, 2, 8, 30),
	)
}

func TestGenerateSlotsHonoursWindowValidityAndSorts(t *testing.T) {
	r := berlinRule(time.Monday)
	r.ValidTo = utc(2026, time.June, 5, 0, 0)
	r2 := r
	r2.StartMinute, r2.EndMinute = 8*60, 9*60 // overlaps r; duplicates collapse
	window := domain.Window{From: utc(2026, time.June, 1, 7, 30), To: utc(2026, time.June, 30, 0, 0)}
	slots, err := domain.GenerateSlots([]domain.AvailabilityRule{r, r2}, nil, nil, window)
	if err != nil {
		t.Fatal(err)
	}
	sameStarts(t, slots, utc(2026, time.June, 1, 7, 30), utc(2026, time.June, 1, 8, 0), utc(2026, time.June, 1, 8, 30))
}

func TestGenerateSlotsRejectsBadTimezone(t *testing.T) {
	r := berlinRule(time.Monday)
	r.Timezone = "Mars/Olympus"
	_, err := domain.GenerateSlots([]domain.AvailabilityRule{r}, nil, nil, domain.Window{From: utc(2026, 6, 1, 0, 0), To: utc(2026, 6, 2, 0, 0)})
	if err == nil {
		t.Fatal("expected an error for an unknown timezone")
	}
}

func TestPresentSlotsGroupsByViewerDay(t *testing.T) {
	slots := []domain.Slot{
		{Start: utc(2026, time.June, 1, 23, 30), End: utc(2026, time.June, 2, 0, 0)},
		{Start: utc(2026, time.June, 2, 0, 0), End: utc(2026, time.June, 2, 0, 30)},
	}
	days, err := domain.PresentSlots(slots, "Asia/Ho_Chi_Minh")
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 1 || days[0].Date != "2026-06-02" || len(days[0].Slots) != 2 || days[0].Slots[0].Label != "06:30" {
		t.Fatalf("days = %+v", days)
	}
	if _, err := domain.PresentSlots(slots, "Nowhere/Land"); err == nil {
		t.Fatal("expected an error for an unknown timezone")
	}
}

func TestCanChangeUntilTwoHoursBefore(t *testing.T) {
	start := utc(2026, time.June, 1, 12, 0)
	if !domain.CanChangeBooking(start, start.Add(-2*time.Hour-time.Minute)) {
		t.Fatal("change more than 2h before start should be allowed")
	}
	if domain.CanChangeBooking(start, start.Add(-2*time.Hour)) {
		t.Fatal("change exactly 2h before start should be refused")
	}
	if domain.CanChangeBooking(start, start.Add(-time.Hour)) {
		t.Fatal("change within 2h should be refused")
	}
}

func TestReminderTimes(t *testing.T) {
	start := utc(2026, time.June, 1, 12, 0)
	got := domain.ReminderTimes(start)
	if len(got) != 2 || !got[0].At.Equal(start.Add(-24*time.Hour)) || got[0].Offset != "24h" || !got[1].At.Equal(start.Add(-time.Hour)) || got[1].Offset != "1h" {
		t.Fatalf("reminders = %+v", got)
	}
}

func TestGenerateSlotsSkipsMissingHourOnSpringForward(t *testing.T) {
	// Berlin skips 02:00–03:00 on Sunday 2026-03-29.
	r := berlinRule(time.Sunday)
	r.StartMinute, r.EndMinute, r.SlotMinutes = 60, 4*60, 60
	window := domain.Window{From: utc(2026, time.March, 28, 0, 0), To: utc(2026, time.March, 30, 0, 0)}
	slots, err := domain.GenerateSlots([]domain.AvailabilityRule{r}, nil, nil, window)
	if err != nil {
		t.Fatal(err)
	}
	// 01:00 CET = 00:00Z; 02:00 does not exist; 03:00 CEST = 01:00Z.
	sameStarts(t, slots, utc(2026, time.March, 29, 0, 0), utc(2026, time.March, 29, 1, 0))
	for _, s := range slots {
		if s.End.Sub(s.Start) != time.Hour {
			t.Fatalf("slot %v lasts %v", s.Start, s.End.Sub(s.Start))
		}
	}
}

func TestGenerateSlotsKeepsSlotLengthOnFallBack(t *testing.T) {
	// Berlin repeats 02:00–03:00 on Sunday 2026-10-25.
	r := berlinRule(time.Sunday)
	r.StartMinute, r.EndMinute, r.SlotMinutes = 60, 4*60, 60
	window := domain.Window{From: utc(2026, time.October, 24, 0, 0), To: utc(2026, time.October, 26, 0, 0)}
	slots, err := domain.GenerateSlots([]domain.AvailabilityRule{r}, nil, nil, window)
	if err != nil {
		t.Fatal(err)
	}
	// 01:00 CEST = 23:00Z (24th); the ambiguous 02:00 resolves to its later
	// reading, 02:00 CET = 01:00Z; 03:00 CET = 02:00Z. Each slot stays an hour.
	sameStarts(t, slots, utc(2026, time.October, 24, 23, 0), utc(2026, time.October, 25, 1, 0), utc(2026, time.October, 25, 2, 0))
	for _, s := range slots {
		if s.End.Sub(s.Start) != time.Hour {
			t.Fatalf("slot %v lasts %v", s.Start, s.End.Sub(s.Start))
		}
	}
}

func TestValidityBoundsCompareLocalDates(t *testing.T) {
	// A rule in Los Angeles valid only on 2026-06-01: a UTC-midnight bound
	// must not lose the day when read in a zone west of UTC.
	r := domain.AvailabilityRule{Weekday: time.Monday, StartMinute: 9 * 60, EndMinute: 10 * 60, Timezone: "America/Los_Angeles", SlotMinutes: 60,
		ValidFrom: utc(2026, time.June, 1, 0, 0), ValidTo: utc(2026, time.June, 1, 0, 0)}
	slots, err := domain.GenerateSlots([]domain.AvailabilityRule{r}, nil, nil, domain.Window{From: utc(2026, time.May, 20, 0, 0), To: utc(2026, time.June, 20, 0, 0)})
	if err != nil {
		t.Fatal(err)
	}
	sameStarts(t, slots, utc(2026, time.June, 1, 16, 0))
	if domain.ValidTimezone("Local") {
		t.Fatal("Local is not an IANA zone")
	}
}
