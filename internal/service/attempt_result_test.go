package service

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

func TestCandidateResultHidesHiddenCases(t *testing.T) {
	pub, hid1, hid2 := uuid.New(), uuid.New(), uuid.New()
	cases := []ProblemTestCase{
		{ID: pub, Position: 1, Visibility: "public", Input: "1 2", Expected: "3"},
		{ID: hid1, Position: 2, Visibility: "hidden", Input: "secret", Expected: "s"},
		{ID: hid2, Position: 3, Visibility: "hidden"},
	}
	raw := `{"id":"x","status":"ok","results":[
		{"test_id":"` + pub.String() + `","status":"fail","stderr_tail":"boom","time_ms":12},
		{"test_id":"` + hid1.String() + `","status":"pass","stderr_tail":"secret leak"},
		{"test_id":"` + hid2.String() + `","status":"fail"},
		{"test_id":"gone","status":"pass"}]}`
	got := CandidateResultOf(json.RawMessage(raw), cases)
	if len(got.Public) != 1 || got.Public[0].Position != 1 || got.Public[0].Status != "fail" || got.Public[0].Actual != "boom" {
		t.Errorf("public = %+v", got.Public)
	}
	if got.HiddenPassed != 1 || got.HiddenTotal != 2 {
		t.Errorf("hidden = %d/%d, want 1/2", got.HiddenPassed, got.HiddenTotal)
	}
	b, _ := json.Marshal(got)
	if s := string(b); contains1(s, "secret") || contains1(s, hid1.String()) {
		t.Errorf("projection leaks hidden case detail: %s", s)
	}
}

func contains1(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
