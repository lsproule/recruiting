package assess

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"recruiting/internal/service"
)

// The island boots from a JSON script element and mounts into a fixed
// element; both have to be on the started page, and the JSON must survive
// the trip through the script element intact.
func TestStartedSessionPageCarriesTheIslandConfig(t *testing.T) {
	s := service.AttemptSession{
		Attempt:       service.Attempt{ID: uuid.New(), Status: service.AttemptStarted, ExpiresAt: time.Now().Add(time.Hour)},
		Assessment:    service.Assessment{Name: "Backend screen", DurationMinutes: 30},
		JobTitle:      "Senior Go Engineer",
		CandidateName: "Ada",
		Problems: []service.SessionProblem{{
			ID: uuid.New(), Title: "Adder", Kind: "code", Statement: "Print a+b </script><!-- x  ",
		}},
	}
	cfg, err := json.Marshal(configFor(s))
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	if err := sessionPage(s, string(cfg), "csrf").Render(context.Background(), &sb); err != nil {
		t.Fatal(err)
	}
	body := sb.String()
	if !strings.Contains(body, `<div id="assess"></div>`) {
		t.Errorf("no #assess mount point on the page: %s", body)
	}
	open := `<script id="` + configID + `" type="application/json">`
	start := strings.Index(body, open)
	if start < 0 {
		t.Fatalf("no config script element on the page: %s", body)
	}
	rest := body[start+len(open):]
	end := strings.Index(rest, "</script>")
	if end < 0 {
		t.Fatalf("config script element never closes: %s", body)
	}
	raw := rest[:end]
	if strings.Contains(raw, "<") || strings.Contains(raw, " ") {
		t.Errorf("config JSON is not script-safe: %q", raw)
	}
	var got islandConfig
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("config JSON does not parse: %v\n%s", err, raw)
	}
	if got.AttemptID != s.Attempt.ID.String() || len(got.Problems) != 1 || got.Problems[0].Statement != s.Problems[0].Statement {
		t.Errorf("config = %+v, want the attempt and its problem statement round-tripped", got)
	}
}
