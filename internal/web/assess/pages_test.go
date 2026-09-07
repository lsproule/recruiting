package assess

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"recruiting/internal/service"
	"recruiting/internal/web/layout"
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
	if got.Mode != "attempt" {
		t.Errorf("config mode = %q, want the island told it is running an attempt", got.Mode)
	}
	if got.AttemptID != s.Attempt.ID.String() || len(got.Problems) != 1 || got.Problems[0].Statement != s.Problems[0].Statement {
		t.Errorf("config = %+v, want the attempt and its problem statement round-tripped", got)
	}
}

// The assessment form is where a recruiter decides what a candidate may
// answer in and what the session watches: the languages come as ticks with
// an "any" default, and each integrity measure as its own toggle.
func TestAssessmentFormCarriesTheLanguageAndIntegrityControls(t *testing.T) {
	f := assessmentForm{
		New: true, Name: "Backend screen", DurationMinutes: 30, InviteWindowDays: 5,
		AllowedLanguages: []string{"go"},
		Integrity:        service.IntegritySettings{Fullscreen: true, Webcam: true, WebcamEvery: 90},
	}
	var sb strings.Builder
	if err := formPage(layout.Page{}, f, nil).Render(context.Background(), &sb); err != nil {
		t.Fatal(err)
	}
	body := sb.String()
	for _, want := range []string{
		`name="allowed_languages" value="any"`,
		`name="allowed_languages" value="go" checked`,
		`name="allowed_languages" value="python"`,
		`name="integrity_fullscreen" value="1" checked`,
		`name="integrity_block_paste" value="1"`,
		`name="integrity_webcam" value="1" checked`,
		`name="integrity_photo_id" value="1"`,
		`name="webcam_interval_s" value="90"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("form is missing %s", want)
		}
	}
}

// What the form posts is what the service stores.
func TestAssessmentFormInputCarriesLanguagesAndIntegrity(t *testing.T) {
	f := assessmentForm{
		AllowedLanguages: []string{"go", "python"},
		Integrity:        service.IntegritySettings{BlockPaste: true, Webcam: true, PhotoID: true, WebcamEvery: 30},
	}
	in := f.input()
	if len(in.AllowedLanguages) != 2 || in.Integrity.WebcamEvery != 30 || !in.Integrity.PhotoID {
		t.Errorf("input = %+v, want the ticks and toggles carried through", in)
	}
}

// A session that records anything opens on the consent screen: the ledger
// says what is collected, and the only way past it is one answer either way.
func TestConsentScreenListsWhatIsRecordedAndGatesStart(t *testing.T) {
	s := service.AttemptSession{
		Attempt: service.Attempt{ID: uuid.New(), Status: service.AttemptInvited, InviteExpiresAt: time.Now().Add(48 * time.Hour)},
		Assessment: service.Assessment{
			Name: "Backend screen", DurationMinutes: 30,
			Integrity: service.IntegritySettings{Webcam: true, WebcamEvery: 45, PhotoID: true, Fullscreen: true, BlockPaste: true},
		},
		CandidateName: "Ada",
		Problems:      []service.SessionProblem{{ID: uuid.New(), Title: "Adder", Statement: "a secret statement"}},
	}
	var sb strings.Builder
	if err := sessionPage(s, `{"integrity":{"webcam":true}}`, "csrf").Render(context.Background(), &sb); err != nil {
		t.Fatal(err)
	}
	body := sb.String()
	for _, want := range []string{
		`id="assess-consent-form"`,
		`action="` + ConsentPath + `"`,
		`name="agree"`,
		`required`,
		`name="decision" value="agree"`,
		`name="decision" value="decline"`,
		`id="assess-consent-camera"`,
		"45 seconds",
		"Photo ID",
		"Fullscreen",
		"Pasting",
		layout.AssessPath,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("consent screen is missing %s", want)
		}
	}
	if strings.Contains(body, s.Problems[0].Statement) {
		t.Error("the consent screen shows a problem statement before the clock starts")
	}
}

// With every measure off there is nothing to consent to, and the invited
// page is the one it has always been.
func TestInvitedPageWithoutIntegrityGoesStraightToStart(t *testing.T) {
	s := service.AttemptSession{
		Attempt:    service.Attempt{ID: uuid.New(), Status: service.AttemptInvited, InviteExpiresAt: time.Now().Add(48 * time.Hour)},
		Assessment: service.Assessment{Name: "Backend screen", DurationMinutes: 30},
		Problems:   []service.SessionProblem{{ID: uuid.New(), Title: "Adder"}},
	}
	var sb strings.Builder
	if err := sessionPage(s, "{}", "csrf").Render(context.Background(), &sb); err != nil {
		t.Fatal(err)
	}
	body := sb.String()
	if strings.Contains(body, "assess-consent-form") || strings.Contains(body, layout.AssessPath) {
		t.Error("an assessment that records nothing must not ask for consent or load the island")
	}
	if !strings.Contains(body, SessionPath+"start") {
		t.Error("the plain invited page has lost its Start form")
	}
}
