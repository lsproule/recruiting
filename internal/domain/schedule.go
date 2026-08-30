package domain

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

// ChangeCutoff is how long before an interview starts the candidate may
// still reschedule or cancel it.
const ChangeCutoff = 2 * time.Hour

// ErrBadTimezone reports a timezone name the IANA database does not know.
var ErrBadTimezone = errors.New("domain: unknown timezone")

// AvailabilityRule is one weekly block of a vetter's availability, expressed
// as wall-clock minutes in the vetter's zone so it survives DST changes.
type AvailabilityRule struct {
	Weekday       time.Weekday
	StartMinute   int // minutes since local midnight
	EndMinute     int
	Timezone      string
	SlotMinutes   int
	BufferMinutes int
	// ValidFrom and ValidTo bound the rule by date; zero means unbounded.
	ValidFrom time.Time
	ValidTo   time.Time
}

// Exception is a blocked interval; nothing is offered inside it.
type Exception struct{ Start, End time.Time }

// Booking is an existing interview that consumes time plus its buffer.
type Booking struct{ Start, End time.Time }

// Window bounds the instants slots are generated for: [From, To).
type Window struct{ From, To time.Time }

// Slot is one bookable interval, always in UTC.
type Slot struct{ Start, End time.Time }

// GenerateSlots is the bookable slots the rules yield within window, in UTC,
// sorted and de-duplicated. Each rule walks its local wall clock — computed
// with time.Date in the rule's zone per slot, not by adding durations across
// a day — so a DST shift moves the UTC instant and never the local hour.
// Exceptions remove overlapping slots; bookings remove overlapping slots and
// those within the rule's buffer either side, measured on UTC instants.
func GenerateSlots(rules []AvailabilityRule, exceptions []Exception, booked []Booking, window Window) ([]Slot, error) {
	seen := map[time.Time]Slot{}
	for _, r := range rules {
		if r.SlotMinutes <= 0 || r.EndMinute <= r.StartMinute {
			continue
		}
		loc, err := time.LoadLocation(r.Timezone)
		if err != nil || r.Timezone == "" {
			return nil, fmt.Errorf("%w: %q", ErrBadTimezone, r.Timezone)
		}
		buffer := time.Duration(r.BufferMinutes) * time.Minute
		slotLen := time.Duration(r.SlotMinutes) * time.Minute
		// Walk local dates one day beyond the window on each side: a UTC
		// window edge can fall on a different local date.
		first := window.From.In(loc).AddDate(0, 0, -1)
		last := window.To.In(loc).AddDate(0, 0, 1)
		for y, m, d := first.Date(); ; {
			day := time.Date(y, m, d, 0, 0, 0, 0, loc)
			if day.After(last) {
				break
			}
			if day.Weekday() == r.Weekday && r.validOn(day) {
				for min := r.StartMinute; min+r.SlotMinutes <= r.EndMinute; min += r.SlotMinutes {
					local := time.Date(y, m, d, 0, min, 0, 0, loc)
					if local.Hour()*60+local.Minute() != min {
						// Wall-clock time that does not exist on this day
						// (spring-forward); time.Date normalised it away.
						continue
					}
					start := local.UTC()
					s := Slot{Start: start, End: start.Add(slotLen)}
					if start.Before(window.From) || s.End.After(window.To) {
						continue
					}
					if blocked(s, exceptions, booked, buffer) {
						continue
					}
					if _, dup := seen[start]; !dup {
						seen[start] = s
					}
				}
			}
			y, m, d = day.AddDate(0, 0, 1).Date()
		}
	}
	out := make([]Slot, 0, len(seen))
	for _, s := range seen {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, nil
}

// validOn compares calendar dates: the rule's local date against the bound's
// own date, whatever zone it was stored in.
func (r AvailabilityRule) validOn(day time.Time) bool {
	d := dateOnly(day)
	if !r.ValidFrom.IsZero() && d.Before(dateOnly(r.ValidFrom)) {
		return false
	}
	if !r.ValidTo.IsZero() && d.After(dateOnly(r.ValidTo)) {
		return false
	}
	return true
}

func dateOnly(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func overlaps(aStart, aEnd, bStart, bEnd time.Time) bool {
	return aStart.Before(bEnd) && bStart.Before(aEnd)
}

func blocked(s Slot, exceptions []Exception, booked []Booking, buffer time.Duration) bool {
	for _, e := range exceptions {
		if overlaps(s.Start, s.End, e.Start, e.End) {
			return true
		}
	}
	for _, b := range booked {
		if overlaps(s.Start, s.End, b.Start.Add(-buffer), b.End.Add(buffer)) {
			return true
		}
	}
	return false
}

// LocalSlot is a slot as a viewer sees it.
type LocalSlot struct {
	Start time.Time // UTC; the value a booking form submits
	Label string    // local wall clock, "15:04"
}

// SlotDay groups a viewer's slots by local date.
type SlotDay struct {
	Date  string // "2006-01-02" in the viewer's zone
	Title string // "Mon 2 Jan"
	Slots []LocalSlot
}

// PresentSlots lays UTC slots out by local day in tz.
func PresentSlots(slots []Slot, tz string) ([]SlotDay, error) {
	loc, err := time.LoadLocation(tz)
	if err != nil || tz == "" {
		return nil, fmt.Errorf("%w: %q", ErrBadTimezone, tz)
	}
	var days []SlotDay
	for _, s := range slots {
		local := s.Start.In(loc)
		date := local.Format("2006-01-02")
		if len(days) == 0 || days[len(days)-1].Date != date {
			days = append(days, SlotDay{Date: date, Title: local.Format("Mon 2 Jan")})
		}
		d := &days[len(days)-1]
		d.Slots = append(d.Slots, LocalSlot{Start: s.Start.UTC(), Label: local.Format("15:04")})
	}
	return days, nil
}

// CanChangeBooking reports whether a booking starting at start may still be
// rescheduled or cancelled at now.
func CanChangeBooking(start, now time.Time) bool {
	return now.Before(start.Add(-ChangeCutoff))
}

// Reminder is one scheduled reminder for a booking.
type Reminder struct {
	Offset string // how long before the start, as the job payload names it
	At     time.Time
}

// ReminderTimes are the reminders a booking at start gets: a day and an hour
// before.
func ReminderTimes(start time.Time) []Reminder {
	return []Reminder{
		{Offset: "24h", At: start.Add(-24 * time.Hour)},
		{Offset: "1h", At: start.Add(-time.Hour)},
	}
}

// ValidTimezone reports whether tz names an IANA zone.
func ValidTimezone(tz string) bool {
	if tz == "" || tz == "Local" {
		return false
	}
	_, err := time.LoadLocation(tz)
	return err == nil
}
