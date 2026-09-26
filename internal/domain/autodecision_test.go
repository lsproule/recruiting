package domain_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"recruiting/internal/domain"
)

func TestAutoDecisionFollowsTheStagesOwnRule(t *testing.T) {
	both := domain.Stage{Kind: domain.StageAssessment, PassScore: 70, AutoAdvance: true, AutoReject: true}
	cases := []struct {
		name            string
		stage           domain.Stage
		score           float64
		advance, reject bool
	}{
		{"at the mark passes", both, 70, true, false},
		{"above the mark passes", both, 99.5, true, false},
		{"below the mark fails", both, 69.9, false, true},
		{"advance only never rejects", domain.Stage{Kind: domain.StageAssessment, PassScore: 70, AutoAdvance: true}, 10, false, false},
		{"reject only never advances", domain.Stage{Kind: domain.StageAssessment, PassScore: 70, AutoReject: true}, 100, false, false},
		{"no mark decides nothing", domain.Stage{Kind: domain.StageAssessment, AutoAdvance: true, AutoReject: true}, 100, false, false},
		{"another kind decides nothing", domain.Stage{Kind: domain.StageInterview, PassScore: 70, AutoAdvance: true}, 100, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			advance, reject := domain.AutoDecision(c.stage, c.score)
			if advance != c.advance || reject != c.reject {
				t.Fatalf("AutoDecision = advance %v reject %v, want %v %v", advance, reject, c.advance, c.reject)
			}
		})
	}
}

func TestAssessmentSettingsNeedAMarkToDecide(t *testing.T) {
	s := domain.NormalizeStage(domain.Stage{Name: "Exam", Kind: domain.StageAssessment, AutoReject: true})
	if err := domain.ValidateStageSettings(s); !errors.Is(err, domain.ErrInvalidPipeline) {
		t.Fatalf("auto-reject without a mark: %v, want ErrInvalidPipeline", err)
	}
	s.PassScore = 101
	if err := domain.ValidateStageSettings(s); !errors.Is(err, domain.ErrInvalidPipeline) {
		t.Fatalf("mark of 101: %v, want ErrInvalidPipeline", err)
	}
	s.PassScore = 60
	if err := domain.ValidateStageSettings(s); err != nil {
		t.Fatalf("a mark of 60 with auto-reject: %v", err)
	}
	// The settings belong to assessment stages alone.
	other := domain.NormalizeStage(domain.Stage{Name: "Call", Kind: domain.StageInterview, PassScore: 60, AutoAdvance: true})
	if other.PassScore != 0 || other.AutoAdvance {
		t.Fatalf("an interview stage kept assessment settings: %+v", other)
	}
}

func TestTheSystemMovesWithTheRecruitersAuthority(t *testing.T) {
	from := domain.Stage{ID: uuid.New(), Position: 2, Kind: domain.StageAssessment}
	next := domain.Stage{ID: uuid.New(), Position: 3, Kind: domain.StageClientReview}
	rejected := domain.Stage{ID: uuid.New(), Position: 9, Kind: domain.StageTerminal, Terminal: domain.StatusRejected}
	app := domain.Application{ID: uuid.New(), StageID: from.ID, Status: domain.StatusActive}
	none := domain.Prereqs{}

	// Without a verdict the system, like a recruiter, needs an override with a reason.
	err := domain.ValidateMove(domain.ActorSystem, app, from, next, none, domain.MoveRequest{ApplicationID: app.ID, ToStageID: next.ID})
	if !errors.Is(err, domain.ErrPrereqMissing) {
		t.Fatalf("system advance without verdict or override: %v, want ErrPrereqMissing", err)
	}
	req := domain.MoveRequest{ApplicationID: app.ID, ToStageID: next.ID, OverridePrereq: true, Reason: "scored 90, pass mark 70"}
	if err := domain.ValidateMove(domain.ActorSystem, app, from, next, none, req); err != nil {
		t.Fatalf("system advance with override: %v", err)
	}
	req = domain.MoveRequest{ApplicationID: app.ID, ToStageID: rejected.ID, OverridePrereq: true, Reason: "scored 20, pass mark 70"}
	if err := domain.ValidateMove(domain.ActorSystem, app, from, rejected, none, req); err != nil {
		t.Fatalf("system reject with override: %v", err)
	}
	// A vetter still cannot override.
	if err := domain.ValidateMove(domain.ActorVetter, app, from, next, none, req); !errors.Is(err, domain.ErrForbiddenMove) {
		t.Fatalf("vetter override: %v, want ErrForbiddenMove", err)
	}
}
