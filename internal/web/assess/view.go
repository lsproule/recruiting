package assess

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"

	"recruiting/internal/service"
)

// configID is the element the island reads its boot JSON from.
const configID = "assess-config"

// configScript is the island's boot JSON as a whole script element. templ
// treats the content of a script element as raw text, so the element is
// written here rather than interpolated inside one in the template.
func configScript(id, body string) templ.Component {
	return templ.Raw(`<script id="` + id + `" type="application/json">` + jsonForScript(body) + `</script>`)
}

// jsonForScript makes a JSON document safe to inline in a script element.
// Escaping every "<" is what makes that true whatever follows it — a closing
// tag, a comment, anything a future parser reads specially — and the escape
// is JSON's own, so the document still parses. The line separators are
// escaped because they end a statement in JavaScript but not in JSON.
func jsonForScript(s string) string {
	r := strings.NewReplacer("<", `<`, " ", ` `, " ", ` `)
	return r.Replace(s)
}

// needsIsland reports whether the candidate page carries the script: the
// session itself, and the consent screen's camera check before it.
func needsIsland(s service.AttemptSession) bool {
	switch s.Attempt.Status {
	case service.AttemptStarted:
		return true
	case service.AttemptInvited:
		return service.ConsentRequired(s.Assessment.Integrity)
	}
	return false
}

// inviteSummary is the line under the Assessments heading: what is actually
// outstanding, since that is what the screen is opened to find out.
func inviteSummary(rows []service.AttemptInvite) string {
	waiting, sitting := 0, 0
	for _, i := range rows {
		switch i.Status {
		case service.AttemptInvited:
			waiting++
		case service.AttemptStarted:
			sitting++
		}
	}
	if waiting == 0 && sitting == 0 {
		return "Nothing is in flight."
	}
	return strconv.Itoa(waiting) + " waiting to start, " + strconv.Itoa(sitting) + " sitting now."
}

// inviteStatusClass tints a status the way the rest of the console tints an
// ordinal: the accent for the states still in play, neutral for the rest.
func inviteStatusClass(status string) string {
	switch status {
	case service.AttemptInvited, service.AttemptStarted:
		return "tag-accent"
	}
	return "tag-neutral"
}

// share renders a 0–1 progress as whole percent for a bar's width.
func share(v float64) string { return strconv.Itoa(int(v*100 + 0.5)) }

// flagLabel counts the events the recording flagged; a clean sitting says so
// rather than showing a bare zero.
func flagLabel(n int) string {
	if n == 0 {
		return "clean"
	}
	return strconv.Itoa(n) + " flagged"
}

func flagClass(n int) string {
	if n == 0 {
		return "muted"
	}
	return "flagged"
}

// expiryLabel is the deadline that still applies, or a dash once the sitting
// is closed and nothing is counting down.
func expiryLabel(i service.AttemptInvite) string {
	at := i.ExpiresAt()
	if at.IsZero() {
		return "—"
	}
	return at.UTC().Format("2 Jan 15:04 UTC")
}

func day(t time.Time) string { return t.UTC().Format("2 Jan 2006") }

// takeHomeWindow reads a take-home's window in days and hours.
func takeHomeWindow(minutes int) string {
	days, hours := minutes/(24*60), (minutes%(24*60))/60
	switch {
	case days > 0 && hours > 0:
		return fmt.Sprintf("%d days and %d hours", days, hours)
	case days == 1:
		return "1 day"
	case days > 0:
		return fmt.Sprintf("%d days", days)
	case hours == 1:
		return "1 hour"
	}
	return fmt.Sprintf("%d hours", hours)
}
