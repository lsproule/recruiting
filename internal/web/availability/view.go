package availability

import (
	"encoding/json"
	"strconv"
	"time"

	"recruiting/internal/service"
)

// weekdays in the order the grid shows them: the working week first.
var weekdays = []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday, time.Saturday, time.Sunday}

// rulesJSON seeds the grid with the stored rules.
func rulesJSON(rules []service.Rule) string {
	out := make([]ruleForm, 0, len(rules))
	for _, r := range rules {
		out = append(out, ruleForm{Weekday: int(r.Weekday), Start: r.Start, End: r.End})
	}
	b, _ := json.Marshal(out)
	return string(b)
}

func weekdaysJSON() string {
	type day struct {
		N    int    `json:"n"`
		Name string `json:"name"`
	}
	out := make([]day, 0, len(weekdays))
	for _, d := range weekdays {
		out = append(out, day{N: int(d), Name: d.String()})
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// local renders t in tz for the vetter's own eyes.
func local(t time.Time, tz string) string {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	return t.In(loc).Format("Mon 2 Jan 2006 15:04")
}

// slotOutcomes are what a vetter can record for a booked interview.
var slotOutcomes = []struct{ Value, Label string }{
	{service.SlotCompleted, "Completed"},
	{service.SlotNoShow, "No-show"},
}

func itoa(n int) string { return strconv.Itoa(n) }
