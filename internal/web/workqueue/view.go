package workqueue

import (
	"strconv"
	"time"

	"github.com/google/uuid"

	"recruiting/internal/service"
)

// queueView is the screen: the rows the filter asked for, grouped by the
// company they are for, and how many rows every rule holds so the chips can
// say.
type queueView struct {
	Items  []service.QueueItem
	Counts map[service.QueueKind]int
	Filter service.QueueKind
	Now    time.Time
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
	for _, item := range v.Items {
		if item.Due != nil && item.Due.Before(v.now()) {
			n++
		}
	}
	return n
}

func (v queueView) now() time.Time {
	if v.Now.IsZero() {
		return time.Now()
	}
	return v.Now
}

// companyGroup is one company's slice of the queue: the work that has to
// happen for their roles, most overdue first, under their name.
type companyGroup struct {
	ID    uuid.UUID
	Name  string
	Items []service.QueueItem
	Late  int
}

// Groups is the queue by company, companies ordered by their most overdue
// row so the client who has waited longest sits at the top. Work that
// belongs to no company (there is none today, but the shape allows it)
// would come last under its own heading.
func (v queueView) Groups() []companyGroup {
	index := map[string]int{}
	var out []companyGroup
	now := v.now()
	for _, item := range v.Items {
		key := item.ClientName
		i, ok := index[key]
		if !ok {
			i = len(out)
			index[key] = i
			out = append(out, companyGroup{ID: item.ClientID, Name: key})
		}
		out[i].Items = append(out[i].Items, item)
		if item.Due != nil && item.Due.Before(now) {
			out[i].Late++
		}
	}
	// Items arrive sorted most urgent first, so a group's first row is its
	// most urgent one; the groups keep that order.
	return out
}

// chip is one filter button.
type chip struct {
	Kind   service.QueueKind
	Label  string
	Count  int
	Active bool
	Href   string
}

// Chips is "All" and one button per rule that has rows, each carrying its
// count, in the order of the hiring process. A rule with nothing waiting is
// left off so the bar reads as what there is to do, not what there could be.
func (v queueView) Chips() []chip {
	out := []chip{{Label: "All", Count: v.Total(), Active: v.Filter == "", Href: Prefix}}
	for _, kind := range service.QueueKinds {
		if v.Counts[kind] == 0 && v.Filter != kind {
			continue
		}
		out = append(out, chip{
			Kind: kind, Label: kindLabel(kind), Count: v.Counts[kind],
			Active: v.Filter == kind, Href: Prefix + "?" + filterParam + "=" + string(kind),
		})
	}
	return out
}

// Back is the URL a snooze or a decision returns to: the filter being read.
func (v queueView) Back() string {
	if v.Filter == "" {
		return Prefix
	}
	return Prefix + "?" + filterParam + "=" + string(v.Filter)
}

// kindLabel names a rule in the recruiter's words.
func kindLabel(kind service.QueueKind) string {
	switch kind {
	case service.QueueResume:
		return "Résumé review"
	case service.QueueCallUnbooked:
		return "Call not booked"
	case service.QueueFeedback:
		return "Feedback due"
	case service.QueueExamUnopened:
		return "Exam sent"
	case service.QueueExamReview:
		return "Exam to review"
	case service.QueueDecision:
		return "Decision"
	case service.QueueForward:
		return "Forward to client"
	case service.QueueClientWaiting:
		return "Client asked"
	case service.QueueClientSilent:
		return "Client silent"
	case service.QueueShortlistPlan:
		return "Shortlist plan"
	case service.QueueSprintRating:
		return "Rating due"
	case service.QueueShortlistDraft:
		return "Draft shortlist"
	case service.QueueTalentIntro:
		return "Introduction"
	}
	return string(kind)
}

// stepLabel is the rule's place in the process, for the row's badge.
func stepLabel(kind service.QueueKind) string { return "step " + strconv.Itoa(kind.Step()) }

// due reads a deadline as the recruiter thinks of it: how long is left, or
// how long it has been missed.
func (v queueView) due(item service.QueueItem) string {
	if item.Due == nil {
		return "—"
	}
	d := item.Due.Sub(v.now()).Round(time.Minute)
	if d < 0 {
		return span(-d) + " late"
	}
	return "due in " + span(d)
}

// overdue reports whether the row's moment has already passed.
func (v queueView) overdue(item service.QueueItem) bool {
	return item.Due != nil && item.Due.Before(v.now())
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

// rejectID is the element id of a row's reject form, so its reason box can
// be shown on demand.
func rejectID(item service.QueueItem) string { return "reject-" + item.SubjectID.String() }

func itoa(n int) string { return strconv.Itoa(n) }
