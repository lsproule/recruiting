package domain_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"recruiting/internal/domain"
)

// A pipeline with one stage of every kind, in the order the spec lists them.
var (
	applied   = domain.Stage{ID: uuid.New(), Position: 1, Name: "Applied", Kind: domain.StageGeneric}
	interview = domain.Stage{ID: uuid.New(), Position: 2, Name: "Interview", Kind: domain.StageInterview}
	assess    = domain.Stage{ID: uuid.New(), Position: 3, Name: "Assessment", Kind: domain.StageAssessment}
	review    = domain.Stage{ID: uuid.New(), Position: 4, Name: "Client Review", Kind: domain.StageClientReview}
	review2   = domain.Stage{ID: uuid.New(), Position: 5, Name: "Client Onsite", Kind: domain.StageClientReview}
	hired     = domain.Stage{ID: uuid.New(), Position: 6, Name: "Hired", Kind: domain.StageTerminal, Terminal: domain.StatusHired}
	rejected  = domain.Stage{ID: uuid.New(), Position: 7, Name: "Rejected", Kind: domain.StageTerminal, Terminal: domain.StatusRejected}
)

func activeIn(s domain.Stage) domain.Application {
	return domain.Application{ID: uuid.New(), StageID: s.ID, Status: domain.StatusActive}
}

func TestValidateMoveMatrix(t *testing.T) {
	both := domain.Prereqs{HasScorecard: true, HasVerdict: true}
	none := domain.Prereqs{}
	cases := []struct {
		name     string
		actor    domain.ActorRole
		from, to domain.Stage
		prereqs  domain.Prereqs
		req      domain.MoveRequest
		want     error
	}{
		// generic: recruiter and admin move out; nobody else.
		{"recruiter advances from generic", domain.ActorRecruiter, applied, interview, none, domain.MoveRequest{}, nil},
		{"admin advances from generic", domain.ActorAdmin, applied, interview, none, domain.MoveRequest{}, nil},
		{"vetter cannot leave generic", domain.ActorVetter, applied, interview, both, domain.MoveRequest{}, domain.ErrForbiddenMove},
		{"client cannot leave generic", domain.ActorClient, applied, review, both, domain.MoveRequest{}, domain.ErrForbiddenMove},
		{"system cannot move", domain.ActorSystem, applied, interview, both, domain.MoveRequest{}, domain.ErrForbiddenMove},
		{"a move to the same stage is not a move", domain.ActorAdmin, applied, applied, both, domain.MoveRequest{}, domain.ErrForbiddenMove},

		// interview: recruiter/admin; vetter advances once a scorecard exists.
		{"recruiter advances after scorecard", domain.ActorRecruiter, interview, assess, both, domain.MoveRequest{}, nil},
		{"recruiter blocked without scorecard", domain.ActorRecruiter, interview, assess, none, domain.MoveRequest{}, domain.ErrPrereqMissing},
		{"recruiter overrides with reason", domain.ActorRecruiter, interview, assess, none, domain.MoveRequest{OverridePrereq: true, Reason: "phone screen was enough"}, nil},
		{"override needs a reason", domain.ActorRecruiter, interview, assess, none, domain.MoveRequest{OverridePrereq: true}, domain.ErrReasonRequired},
		{"vetter advances after scorecard", domain.ActorVetter, interview, assess, domain.Prereqs{HasScorecard: true}, domain.MoveRequest{}, nil},
		{"vetter blocked without scorecard", domain.ActorVetter, interview, assess, none, domain.MoveRequest{}, domain.ErrPrereqMissing},
		{"vetter cannot override", domain.ActorVetter, interview, assess, none, domain.MoveRequest{OverridePrereq: true, Reason: "trust me"}, domain.ErrForbiddenMove},
		{"vetter cannot move backwards", domain.ActorVetter, interview, applied, both, domain.MoveRequest{}, domain.ErrForbiddenMove},
		{"vetter cannot reject", domain.ActorVetter, interview, rejected, both, domain.MoveRequest{Reason: "no"}, domain.ErrForbiddenMove},
		{"client cannot leave interview", domain.ActorClient, interview, assess, both, domain.MoveRequest{}, domain.ErrForbiddenMove},

		// assessment: same shape, keyed on the verdict.
		{"recruiter advances after verdict", domain.ActorRecruiter, assess, review, both, domain.MoveRequest{}, nil},
		{"recruiter blocked without verdict", domain.ActorRecruiter, assess, review, domain.Prereqs{HasScorecard: true}, domain.MoveRequest{}, domain.ErrPrereqMissing},
		{"admin overrides missing verdict", domain.ActorAdmin, assess, review, none, domain.MoveRequest{OverridePrereq: true, Reason: "reviewed by hand"}, nil},
		{"vetter advances after verdict", domain.ActorVetter, assess, review, domain.Prereqs{HasVerdict: true}, domain.MoveRequest{}, nil},
		{"vetter blocked without verdict", domain.ActorVetter, assess, review, none, domain.MoveRequest{}, domain.ErrPrereqMissing},

		// client_review: client moves only to another client review or to rejected.
		{"client advances to the next client review", domain.ActorClient, review, review2, none, domain.MoveRequest{}, nil},
		{"client rejects with a reason", domain.ActorClient, review, rejected, none, domain.MoveRequest{Reason: "not a fit"}, nil},
		{"client reject needs a reason", domain.ActorClient, review, rejected, none, domain.MoveRequest{}, domain.ErrReasonRequired},
		{"client cannot hire", domain.ActorClient, review, hired, none, domain.MoveRequest{}, domain.ErrForbiddenMove},
		{"client cannot send back to assessment", domain.ActorClient, review, assess, none, domain.MoveRequest{}, domain.ErrForbiddenMove},
		{"recruiter hires from client review", domain.ActorRecruiter, review, hired, none, domain.MoveRequest{}, nil},
		{"admin moves back from client review", domain.ActorAdmin, review, interview, none, domain.MoveRequest{}, nil},
		{"vetter cannot leave client review", domain.ActorVetter, review, review2, both, domain.MoveRequest{}, domain.ErrForbiddenMove},

		// rejection always carries a reason.
		{"recruiter reject needs a reason", domain.ActorRecruiter, applied, rejected, none, domain.MoveRequest{}, domain.ErrReasonRequired},
		{"recruiter reject with reason", domain.ActorRecruiter, applied, rejected, none, domain.MoveRequest{Reason: "salary mismatch"}, nil},
		{"blank reason does not count", domain.ActorAdmin, applied, rejected, none, domain.MoveRequest{Reason: "  "}, domain.ErrReasonRequired},
		{"hiring needs no reason", domain.ActorAdmin, applied, hired, none, domain.MoveRequest{}, nil},

		// terminal: nobody leaves.
		{"admin cannot leave hired", domain.ActorAdmin, hired, applied, both, domain.MoveRequest{}, domain.ErrTerminal},
		{"admin cannot leave rejected", domain.ActorAdmin, rejected, applied, both, domain.MoveRequest{Reason: "oops"}, domain.ErrTerminal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := activeIn(tc.from)
			if tc.from.Kind == domain.StageTerminal {
				app.Status = tc.from.Terminal
			}
			req := tc.req
			req.ApplicationID, req.ToStageID = app.ID, tc.to.ID
			err := domain.ValidateMove(tc.actor, app, tc.from, tc.to, tc.prereqs, req)
			if !errors.Is(err, tc.want) {
				t.Fatalf("ValidateMove = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestValidateMoveRefusesAClosedApplication(t *testing.T) {
	for _, status := range []domain.ApplicationStatus{domain.StatusHired, domain.StatusRejected, domain.StatusWithdrawn} {
		app := activeIn(applied)
		app.Status = status
		err := domain.ValidateMove(domain.ActorAdmin, app, applied, interview, domain.Prereqs{}, domain.MoveRequest{ApplicationID: app.ID, ToStageID: interview.ID})
		if !errors.Is(err, domain.ErrTerminal) {
			t.Errorf("status %s: ValidateMove = %v, want ErrTerminal", status, err)
		}
	}
}

func TestValidateMoveRefusesAMismatchedRequest(t *testing.T) {
	app := activeIn(applied)
	err := domain.ValidateMove(domain.ActorAdmin, app, applied, interview, domain.Prereqs{}, domain.MoveRequest{ApplicationID: app.ID, ToStageID: assess.ID})
	if !errors.Is(err, domain.ErrForbiddenMove) {
		t.Errorf("request naming another stage: %v, want ErrForbiddenMove", err)
	}
	err = domain.ValidateMove(domain.ActorAdmin, app, interview, assess, domain.Prereqs{}, domain.MoveRequest{ApplicationID: app.ID, ToStageID: assess.ID})
	if !errors.Is(err, domain.ErrForbiddenMove) {
		t.Errorf("from stage the application is not in: %v, want ErrForbiddenMove", err)
	}
}
