//go:build integration

package client_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"recruiting/internal/service"
)

// What a shortlist must never carry to a client. Each string is written into
// the fixture on a row the client's own reads run next to, so its absence
// from the page is evidence rather than an accident of the data.
const (
	guidelinesSecret = "GUIDELINES-SECRET"
	reviewSecret     = "REVIEW-NOTES-SECRET"
	rivalName        = "Unpicked Ursula"
	pickedName       = "Picked Pat"
	runnerUpName     = "Runner-up Rory"
	packetNote       = "Ranked on how they debugged the failing case."
)

// shortlistFixture is the portal fixture with a sent packet on it: two picks
// at an unblinded stage, one applicant left out, and a sitting under each
// pick complete with the integrity record the client may not see.
type shortlistFixture struct {
	*fixture
	packetID       uuid.UUID
	picked, runner uuid.UUID
	unpicked       uuid.UUID
	attemptID      uuid.UUID
}

func newShortlistFixture(t *testing.T) *shortlistFixture {
	t.Helper()
	f := &shortlistFixture{fixture: newFixture(t)}
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.sys.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	problemID, assessmentID, caseID := uuid.New(), uuid.New(), uuid.New()
	exec(`insert into problem (id, org_id, kind, title, statement, guidelines) values ($1, $2, 'code', 'Rate limiter', 'Build one', $3)`,
		problemID, f.orgID, guidelinesSecret)
	exec(`insert into test_case (id, org_id, problem_id, position, name, input, expected_output, visibility) values ($1, $2, $3, 1, 'Bursts', '5', '5', 'public')`,
		caseID, f.orgID, problemID)
	exec(`insert into assessment (id, org_id, name, duration_minutes) values ($1, $2, 'Screen', 60)`, assessmentID, f.orgID)
	exec(`insert into assessment_problem (assessment_id, org_id, problem_id, position) values ($1, $2, $3, 1)`, assessmentID, f.orgID, problemID)

	applicant := func(name string, score float64) uuid.UUID {
		t.Helper()
		candID, appID := uuid.New(), uuid.New()
		exec(`insert into candidate (id, org_id, email, name) values ($1, $2, $3, $4)`, candID, f.orgID, candID.String()+"@example.com", name)
		// f.final is a client review stage that lifts the blind, so the
		// packet reads with real names.
		exec(`insert into application (id, org_id, job_id, candidate_id, client_company_id, stage_id) values ($1, $2, $3, $4, $5, $6)`,
			appID, f.orgID, f.jobID, candID, f.companyID, f.final)
		attemptID := uuid.New()
		exec(`insert into attempt (id, org_id, application_id, assessment_id, stage_id, status, score, risk_score, started_at, finished_at)
			values ($1, $2, $3, $4, $5, 'scored', $6, 88, now(), now())`,
			attemptID, f.orgID, appID, assessmentID, f.final, score)
		if f.attemptID == uuid.Nil {
			f.attemptID = attemptID
		}
		result := `{"id":"r","status":"ok","results":[{"test_id":"` + caseID.String() + `","status":"pass","time_ms":3}]}`
		exec(`insert into submission (org_id, attempt_id, problem_id, kind, language, source, status, result, score)
			values ($1, $2, $3, 'submit', 'python', 'print("solved")', 'done', $4, $5)`,
			f.orgID, attemptID, problemID, result, score)
		exec(`insert into attempt_source (org_id, attempt_id, problem_id, language, source) values ($1, $2, $3, 'python', 'print("solved")')`,
			f.orgID, attemptID, problemID)
		// One of every recorded kind, so the manifest has something to strip.
		for i, ev := range []struct{ kind, payload string }{
			{"edit", `{"changes":[[0]]}`},
			{"paste", `{"len":42,"internal":false}`},
			{"blur", `{}`},
			{"focus", `{}`},
			{"fullscreen_exit", `{}`},
			{"snapshot", `{"seq":1,"ok":true}`},
			{"consent", `{"webcam":true,"photo_id":true}`},
			{"lang_change", `{"language":"python"}`},
			{"run", `{}`},
			{"submit", `{}`},
		} {
			exec(`insert into attempt_event (org_id, attempt_id, seq, kind, payload, client_ts, problem_id) values ($1, $2, $3, $4, $5::jsonb, now(), $6)`,
				f.orgID, attemptID, i+1, ev.kind, ev.payload, problemID)
		}
		exec(`insert into integrity_signal (org_id, attempt_id, name, value, weight, evidence) values ($1, $2, 'paste_ratio', 0.9, 25, '[]')`, f.orgID, attemptID)
		return appID
	}
	f.picked = applicant(pickedName, 94)
	f.runner = applicant(runnerUpName, 82)
	f.unpicked = applicant(rivalName, 91)

	rec := f.recruiter()
	packet, err := f.shortlists.Save(ctx, rec, nil, service.ShortlistInput{
		JobID: f.jobID, Note: packetNote, ApplicationIDs: []uuid.UUID{f.picked, f.runner},
	})
	if err != nil {
		t.Fatalf("save packet: %v", err)
	}
	f.packetID = packet.ID
	if _, err := f.shortlists.Send(ctx, rec, packet.ID); err != nil {
		t.Fatalf("send packet: %v", err)
	}
	// A reviewer's verdict lands after the send; the client's page is read
	// with it on the record, and must still not show it.
	exec(`insert into review (org_id, attempt_id, vetter_id, verdict, notes)
		select $1, $2, id, 'pass', $3 from org_user where org_id = $1 and email like 'vet-%' limit 1`,
		f.orgID, f.attemptID, reviewSecret)
	return f
}

func (f *shortlistFixture) shortlistPath() string {
	return "/client/jobs/" + f.jobID.String() + "/shortlist"
}

func (f *shortlistFixture) pickPath(appID uuid.UUID) string {
	return f.shortlistPath() + "/" + appID.String()
}

func TestClientShortlistListsPicksInRankOrder(t *testing.T) {
	f := newShortlistFixture(t)
	s := f.browser(t, f.clientEmail)

	res, body := s.get(f.shortlistPath())
	if res.StatusCode != http.StatusOK {
		t.Fatalf("shortlist: %d", res.StatusCode)
	}
	if !strings.Contains(body, packetNote) {
		t.Error("the recruiter's note is not on the page")
	}
	first, second := strings.Index(body, pickedName), strings.Index(body, runnerUpName)
	if first < 0 || second < 0 {
		t.Fatalf("picks missing: %d %d", first, second)
	}
	if first > second {
		t.Error("the picks are not in rank order")
	}
	assertNone(t, body, rivalName, guidelinesSecret, reviewSecret)
}

func TestClientShortlistIsNotFoundForAnotherCompany(t *testing.T) {
	f := newShortlistFixture(t)
	rival := f.browser(t, f.rivalMail)

	for _, path := range []string{f.shortlistPath(), f.pickPath(f.picked), f.pickPath(f.picked) + "/replay"} {
		res, body := rival.get(path)
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("%s as a rival company: %d", path, res.StatusCode)
		}
		assertNone(t, body, pickedName, runnerUpName)
	}
}

func TestClientShortlistPickShowsTheWorkAndNothingInternal(t *testing.T) {
	f := newShortlistFixture(t)
	s := f.browser(t, f.clientEmail)

	res, body := s.get(f.pickPath(f.picked))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("pick: %d", res.StatusCode)
	}
	for _, want := range []string{pickedName, "print(&#34;solved&#34;)", "Bursts", "Rate limiter", "replay-config"} {
		if !strings.Contains(body, want) {
			t.Errorf("the pick page is missing %q", want)
		}
	}
	assertNone(t, body,
		guidelinesSecret, reviewSecret, rivalName,
		"paste_ratio", "Risk score", "risk_score", "snapshot", "webcam", "Integrity")
	assertNone(t, body, secrets...)
}

// TestClientShortlistPickIsNotFoundForAnApplicantOffThePacket keeps the
// packet from becoming a way into the rest of the job's applicants.
func TestClientShortlistPickIsNotFoundForAnApplicantOffThePacket(t *testing.T) {
	f := newShortlistFixture(t)
	s := f.browser(t, f.clientEmail)

	for _, path := range []string{f.pickPath(f.unpicked), f.pickPath(f.unpicked) + "/replay"} {
		res, body := s.get(path)
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %d", path, res.StatusCode)
		}
		assertNone(t, body, rivalName)
	}
}

func TestClientShortlistReplayManifestCarriesOnlyTheCodeEvents(t *testing.T) {
	f := newShortlistFixture(t)
	s := f.browser(t, f.clientEmail)

	res, body := s.get(f.pickPath(f.picked) + "/replay")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("manifest: %d — %s", res.StatusCode, body)
	}
	var manifest struct {
		Events []struct {
			Kind string `json:"kind"`
		} `json:"events"`
		Markers []struct {
			Kind string `json:"kind"`
			Note string `json:"note"`
		} `json:"markers"`
		Problems []struct {
			FinalSource string `json:"final_source"`
		} `json:"problems"`
	}
	if err := json.Unmarshal([]byte(body), &manifest); err != nil {
		t.Fatalf("parse manifest: %v — %s", err, body)
	}
	allowed := map[string]bool{"edit": true, "run": true, "submit": true, "lang_change": true}
	if len(manifest.Events) == 0 {
		t.Fatal("the manifest carries no events at all")
	}
	for _, ev := range manifest.Events {
		if !allowed[ev.Kind] {
			t.Errorf("the manifest carries a %q event", ev.Kind)
		}
	}
	for _, m := range manifest.Markers {
		if m.Kind != "run" && m.Kind != "submit" {
			t.Errorf("the manifest carries a %q marker: %q", m.Kind, m.Note)
		}
	}
	if len(manifest.Problems) == 0 || manifest.Problems[0].FinalSource == "" {
		t.Error("the manifest carries no code to replay")
	}
	// snapshot_every is the viewer's own scrubbing cadence, so the webcam
	// beat is looked for as the event kind rather than as a substring.
	assertNone(t, body, `"snapshot"`, "paste", "blur", "focus", "fullscreen", "consent", "risk", guidelinesSecret, reviewSecret, rivalName)
}
