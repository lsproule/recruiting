package service

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"recruiting/internal/domain"
)

func stage(name string, kind domain.StageKind, terminal domain.ApplicationStatus) domain.Stage {
	return domain.Stage{ID: uuid.New(), Name: name, Kind: kind, Terminal: terminal}
}

func validPipeline() []domain.Stage {
	return []domain.Stage{
		stage("Applied", domain.StageGeneric, ""),
		stage("Client Review", domain.StageClientReview, ""),
		stage("Hired", domain.StageTerminal, domain.StatusHired),
		stage("Rejected", domain.StageTerminal, domain.StatusRejected),
	}
}

func TestValidatePipelineAcceptsTheDefaultShape(t *testing.T) {
	if err := domain.ValidatePipeline(validPipeline()); err != nil {
		t.Fatalf("valid pipeline rejected: %v", err)
	}
}

func TestValidatePipelineRequiresExactlyOneOfEachTerminal(t *testing.T) {
	cases := map[string][]domain.Stage{
		"two hired": append(validPipeline(), stage("Hired again", domain.StageTerminal, domain.StatusHired)),
		"two rejected": append(validPipeline(),
			stage("Rejected again", domain.StageTerminal, domain.StatusRejected)),
		"no hired": {
			stage("Applied", domain.StageGeneric, ""),
			stage("Rejected", domain.StageTerminal, domain.StatusRejected),
		},
		"no rejected": {
			stage("Applied", domain.StageGeneric, ""),
			stage("Hired", domain.StageTerminal, domain.StatusHired),
		},
		"no open stage": {
			stage("Hired", domain.StageTerminal, domain.StatusHired),
			stage("Rejected", domain.StageTerminal, domain.StatusRejected),
		},
		"terminal without an outcome": {
			stage("Applied", domain.StageGeneric, ""),
			stage("Done", domain.StageTerminal, ""),
			stage("Rejected", domain.StageTerminal, domain.StatusRejected),
		},
		"outcome on a non-terminal stage": {
			stage("Applied", domain.StageGeneric, domain.StatusHired),
			stage("Hired", domain.StageTerminal, domain.StatusHired),
			stage("Rejected", domain.StageTerminal, domain.StatusRejected),
		},
		"unnamed stage": {
			stage("  ", domain.StageGeneric, ""),
			stage("Hired", domain.StageTerminal, domain.StatusHired),
			stage("Rejected", domain.StageTerminal, domain.StatusRejected),
		},
		"unknown kind": {
			stage("Applied", domain.StageKind("wizard"), ""),
			stage("Hired", domain.StageTerminal, domain.StatusHired),
			stage("Rejected", domain.StageTerminal, domain.StatusRejected),
		},
	}
	for name, stages := range cases {
		t.Run(name, func(t *testing.T) {
			err := domain.ValidatePipeline(stages)
			if !errors.Is(err, domain.ErrInvalidPipeline) {
				t.Fatalf("got %v, want ErrInvalidPipeline", err)
			}
			if err.Error() == domain.ErrInvalidPipeline.Error() {
				t.Errorf("error does not say which rule broke: %v", err)
			}
		})
	}
}

// ValidateMove's full matrix lands with application moves; what must hold now
// is that a closed application cannot be moved at all.
func TestValidateMoveRefusesAClosedApplication(t *testing.T) {
	stages := validPipeline()
	from, to := stages[2], stages[1]
	app := domain.Application{ID: uuid.New(), StageID: from.ID, Status: domain.StatusHired}
	req := domain.MoveRequest{ApplicationID: app.ID, ToStageID: to.ID, Reason: "reopening"}
	if err := domain.ValidateMove(domain.ActorAdmin, app, from, to, domain.Prereqs{}, req); !errors.Is(err, domain.ErrTerminal) {
		t.Fatalf("got %v, want ErrTerminal", err)
	}

	open := domain.Application{ID: uuid.New(), StageID: stages[0].ID, Status: domain.StatusActive}
	req.ApplicationID = open.ID
	if err := domain.ValidateMove(domain.ActorAdmin, open, stages[0], to, domain.Prereqs{}, req); err != nil {
		t.Fatalf("active application: %v", err)
	}
}
