package interviews

import (
	"strconv"
	"time"

	"recruiting/internal/service"
)

func day(t time.Time) string   { return t.UTC().Format("Mon 2 Jan") }
func clock(t time.Time) string { return t.UTC().Format("15:04") + " UTC" }
func itoa(n int) string        { return strconv.Itoa(n) }

func formatLabel(s service.InterviewRow) string {
	if s.Video {
		return "Video"
	}
	return "Phone"
}

func formatTag(s service.InterviewRow) string {
	if s.Video {
		return "tag-accent"
	}
	return "tag-neutral"
}
