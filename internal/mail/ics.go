package mail

import (
	"net/url"
	"strings"
	"time"
)

// Event is one calendar entry: what an interview confirmation attaches so
// the interview lands in the recipient's calendar with one click, whichever
// calendar they use.
type Event struct {
	// UID identifies the event across updates; a rescheduled interview
	// sent with the same UID replaces the earlier entry.
	UID         string
	Summary     string
	Description string
	Location    string
	URL         string
	Start, End  time.Time
}

// ICS renders the event as an iCalendar document (RFC 5545): a single
// VEVENT in UTC, with a REQUEST method so mail clients offer to add it.
func (e Event) ICS() []byte {
	var b strings.Builder
	line := func(k, v string) {
		b.WriteString(foldLine(k + ":" + icsText(v)))
	}
	b.WriteString("BEGIN:VCALENDAR\r\n")
	b.WriteString("VERSION:2.0\r\n")
	b.WriteString("PRODID:-//recruiting//interviews//EN\r\n")
	b.WriteString("CALSCALE:GREGORIAN\r\n")
	b.WriteString("METHOD:REQUEST\r\n")
	b.WriteString("BEGIN:VEVENT\r\n")
	line("UID", e.UID)
	b.WriteString("DTSTAMP:" + icsTime(time.Now()) + "\r\n")
	b.WriteString("DTSTART:" + icsTime(e.Start) + "\r\n")
	b.WriteString("DTEND:" + icsTime(e.End) + "\r\n")
	line("SUMMARY", e.Summary)
	if e.Description != "" {
		line("DESCRIPTION", e.Description)
	}
	if e.Location != "" {
		line("LOCATION", e.Location)
	}
	if e.URL != "" {
		line("URL", e.URL)
	}
	b.WriteString("STATUS:CONFIRMED\r\n")
	b.WriteString("END:VEVENT\r\n")
	b.WriteString("END:VCALENDAR\r\n")
	return []byte(b.String())
}

// ICSAttachment is the event as a file on a message.
func ICSAttachment(e Event) Attachment {
	return Attachment{Filename: "interview.ics", ContentType: "text/calendar; charset=utf-8; method=REQUEST", Body: e.ICS()}
}

// GoogleCalendarURL is the link that opens the event pre-filled in Google
// Calendar, for a recipient whose mail client ignores the attachment.
func GoogleCalendarURL(e Event) string {
	q := url.Values{}
	q.Set("action", "TEMPLATE")
	q.Set("text", e.Summary)
	q.Set("dates", icsTime(e.Start)+"/"+icsTime(e.End))
	if e.Description != "" {
		q.Set("details", e.Description)
	}
	if e.Location != "" {
		q.Set("location", e.Location)
	}
	return "https://calendar.google.com/calendar/render?" + q.Encode()
}

// icsTime writes an instant in the UTC form the format uses.
func icsTime(t time.Time) string { return t.UTC().Format("20060102T150405Z") }

// icsText escapes a text value: backslashes, separators, and line breaks.
func icsText(s string) string {
	r := strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`, "\r\n", `\n`, "\n", `\n`)
	return r.Replace(s)
}

// foldLine wraps a content line at 75 octets, continuation lines starting
// with a space, as the format requires; it breaks on byte boundaries but
// keeps multi-byte characters whole.
func foldLine(s string) string {
	const width = 75
	var b strings.Builder
	for len(s) > width {
		cut := width
		for cut > 0 && !utf8Start(s[cut]) {
			cut--
		}
		b.WriteString(s[:cut])
		b.WriteString("\r\n ")
		s = s[cut:]
	}
	b.WriteString(s)
	b.WriteString("\r\n")
	return b.String()
}

func utf8Start(c byte) bool { return c&0xC0 != 0x80 }
