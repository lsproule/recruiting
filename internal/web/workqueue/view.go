package workqueue

import (
	"strconv"
	"time"

	"recruiting/internal/service"
)

// queueView is the screen: the rows the filter asked for, and how many rows
// every rule holds so the chips can say.
type queueView struct {
	Items  []service.QueueItem
	Counts map[service.QueueKind]int
	Filter service.QueueKind
}

// Total is every open item, whatever the screen is filtered to.
func (v queueView) Total() int {
	n := 0
	for _, count := range v.Counts {
		n += count
	}
	return n
}

// Overdue is how many rows are already past their moment: the number the
// lede leads with.
func (v queueView) Overdue() int {
	n := 0
	now := time.Now()
	for _, item := range v.Items {
		if item.Due != nil && item.Due.Before(now) {
			n++
		}
	}
	return n
}

// chip is one filter button.
type chip struct {
	Kind   service.QueueKind
	Label  string
	Count  int
	Active bool
	Href   string
}

// Chips is "All" and one button per rule, each carrying its count.
func (v queueView) Chips() []chip {
	out := []chip{{Label: "All", Count: v.Total(), Active: v.Filter == "", Href: Prefix}}
	for _, kind := range service.QueueKinds {
		out = append(out, chip{
			Kind: kind, Label: kindLabel(kind), Count: v.Counts[kind],
			Active: v.Filter == kind, Href: Prefix + "?" + filterParam + "=" + string(kind),
		})
	}
	return out
}

// Back is the URL a snooze returns to: the filter being read.
func (v queueView) Back() string {
	if v.Filter == "" {
		return Prefix
	}
	return Prefix + "?" + filterParam + "=" + string(v.Filter)
}

// kindLabel names a rule in the recruiter's words.
func kindLabel(kind service.QueueKind) string {
	switch kind {
	case service.QueueReview:
		return "Review"
	case service.QueueExpiring:
		return "Expiring"
	case service.QueueClientWaiting:
		return "Client waiting"
	case service.QueueShortlistDraft:
		return "Draft shortlist"
	case service.QueueScorecardOverdue:
		return "Scorecard due"
	case service.QueueSprintRating:
		return "Rating due"
	case service.QueueTalentIntro:
		return "Introduction"
	}
	return string(kind)
}

// due reads a deadline as the recruiter thinks of it: how long is left, or
// how long it has been missed.
func due(item service.QueueItem) string {
	if item.Due == nil {
		return "—"
	}
	d := time.Until(*item.Due).Round(time.Minute)
	if d < 0 {
		return span(-d) + " ago"
	}
	return "in " + span(d)
}

// overdue reports whether the row's moment has already passed.
func overdue(item service.QueueItem) bool {
	return item.Due != nil && item.Due.Before(time.Now())
}

// span writes a duration in the largest unit that still says something.
func span(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return strconv.Itoa(int(d.Hours())/24) + "d"
	case d >= time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h"
	case d >= time.Minute:
		return strconv.Itoa(int(d.Minutes())) + "m"
	}
	return "moments"
}

func itoa(n int) string { return strconv.Itoa(n) }
