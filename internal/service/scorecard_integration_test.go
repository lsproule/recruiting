//go:build integration

package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"recruiting/internal/domain"
	"recruiting/internal/service"
)

type scorecardFixture struct {
	*pipelineFixture
	cards    *service.ScorecardService
	otherID  uuid.UUID
	criteria []service.Criterion
}

// newScorecardFixture parks the application in the interview stage, gives that
// stage a two-criterion rubric, and adds a second vetter to test authorship.
func newScorecardFixture(t *testing.T) *scorecardFixture {
	t.Helper()
	pf := newPipelineFixture(t)
	f := &scorecardFixture{pipelineFixture: pf, otherID: uuid.New()}
	ctx := context.Background()
	if _, err := pf.sys.Exec(ctx,
		`insert into org_user (id, org_id, email, name) values ($1, $2, $3, 'Vic Vetter')`,
		f.otherID, pf.orgID, "vet2-"+pf.orgID.String()+"@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := pf.sys.Exec(ctx, `update application set stage_id = $1, vetter_id = $2 where id = $3`,
		pf.stages[domain.StageInterview], pf.userID, pf.appID); err != nil {
		t.Fatal(err)
	}
	f.cards = service.NewScorecardService(pf.st, service.NewPoolService(pf.st))
	f.criteria = []service.Criterion{
		{Name: "Communication", Description: "Explains their work clearly"},
		{Name: "Depth", Description: "Knows the tools they name"},
	}
	rubric, err := f.cards.SetRubric(ctx, pf.principal(service.RoleRecruiter), pf.jobID, pf.stages[domain.StageInterview],
		service.RubricInput{Name: "Phone screen", Criteria: f.criteria})
	if err != nil {
		t.Fatal(err)
	}
	if len(rubric.Criteria) != 2 {
		t.Fatalf("rubric = %+v", rubric)
	}
	return f
}

func (f *scorecardFixture) vetter() service.Principal {
	return f.principal(service.RoleVetter)
}

func (f *scorecardFixture) other() service.Principal {
	return service.Principal{Kind: service.PrincipalOrgUser, OrgID: f.orgID, UserID: f.otherID, Roles: []string{service.RoleVetter}}
}

func (f *scorecardFixture) input(overall string, scores ...int) service.ScorecardInput {
	in := service.ScorecardInput{
		ApplicationID: f.appID, StageID: f.stages[domain.StageInterview],
		Overall: overall, Notes: "solid call",
	}
	for i, n := range scores {
		in.Scores = append(in.Scores, service.CriterionScore{Name: f.criteria[i].Name, Score: n, Notes: "ok"})
	}
	return in
}

func TestRubricBelongsToAnInterviewStage(t *testing.T) {
	f := newScorecardFixture(t)
	_, err := f.cards.SetRubric(context.Background(), f.principal(service.RoleRecruiter),
		f.jobID, f.stages[domain.StageAssessment], service.RubricInput{Criteria: f.criteria})
	if !errors.Is(err, service.ErrNotInterview) {
		t.Fatalf("rubric on an assessment stage = %v, want ErrNotInterview", err)
	}
}

func TestOnlyTheAssignedInterviewerFilesTheScorecard(t *testing.T) {
	f := newScorecardFixture(t)
	ctx := context.Background()
	if _, err := f.cards.Save(ctx, f.other(), f.input(service.OverallYes, 4, 4)); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("save by an unassigned vetter = %v, want ErrForbidden", err)
	}
	if _, err := f.cards.Form(ctx, f.other(), f.appID, f.stages[domain.StageInterview]); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("form for an unassigned vetter = %v, want ErrForbidden", err)
	}

	// With no vetter on the application, the stage's default interviewer stands in.
	if _, err := f.sys.Exec(ctx, `update application set vetter_id = null where id = $1`, f.appID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sys.Exec(ctx, `update stage set default_vetter_id = $1 where id = $2`, f.otherID, f.stages[domain.StageInterview]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.cards.Save(ctx, f.other(), f.input(service.OverallYes, 4, 4)); err != nil {
		t.Fatalf("save by the stage default: %v", err)
	}
}

func TestAStrongYesIsAnnouncedOnceOnly(t *testing.T) {
	f := newScorecardFixture(t)
	ctx := context.Background()
	card, err := f.cards.Save(ctx, f.vetter(), f.input(service.OverallStrongYes, 5, 5))
	if err != nil {
		t.Fatal(err)
	}
	again := f.input(service.OverallStrongYes, 5, 4)
	again.ID = card.ID
	if _, err := f.cards.Save(ctx, f.vetter(), again); err != nil {
		t.Fatal(err)
	}
	var events int
	if err := f.sys.QueryRow(ctx, `select count(*) from application_event where application_id = $1 and kind = $2`,
		f.appID, service.EventScorecardStrongYes).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("strong-yes events after re-saving = %d, want 1", events)
	}
}

// poolEntries counts the talent-pool entries the fixture's candidate holds.
func (f *scorecardFixture) poolEntries(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.sys.QueryRow(context.Background(), `select count(*) from talent_pool_entry e
		join application a on a.candidate_id = e.candidate_id where a.id = $1`, f.appID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAStrongYesFilesTheCandidateInThePool(t *testing.T) {
	f := newScorecardFixture(t)
	ctx := context.Background()
	if _, err := f.cards.Save(ctx, f.vetter(), f.input(service.OverallYes, 4, 4)); err != nil {
		t.Fatal(err)
	}
	if n := f.poolEntries(t); n != 0 {
		t.Fatalf("pool entries after a plain yes = %d, want 0", n)
	}
	if _, err := f.cards.Save(ctx, f.vetter(), f.input(service.OverallStrongYes, 5, 5)); err != nil {
		t.Fatal(err)
	}
	if n := f.poolEntries(t); n != 1 {
		t.Fatalf("pool entries after a strong yes = %d, want 1", n)
	}
}

func TestScorecardRejectsAScoreOutOfRange(t *testing.T) {
	f := newScorecardFixture(t)
	if _, err := f.cards.Save(context.Background(), f.vetter(), f.input(service.OverallYes, 4, 6)); !errors.Is(err, service.ErrBadScore) {
		t.Fatalf("save with a 6 = %v, want ErrBadScore", err)
	}
	if _, err := f.cards.Save(context.Background(), f.vetter(), f.input("maybe", 4, 4)); !errors.Is(err, service.ErrBadOverall) {
		t.Fatalf("save with an unknown verdict = %v, want ErrBadOverall", err)
	}
}

func TestScorecardIsEditableByItsAuthorOnly(t *testing.T) {
	f := newScorecardFixture(t)
	ctx := context.Background()
	card, err := f.cards.Save(ctx, f.vetter(), f.input(service.OverallYes, 4, 5))
	if err != nil {
		t.Fatal(err)
	}
	// The snapshot travels with the card, so a later rubric edit leaves it alone.
	if len(card.Criteria) != 2 || card.Criteria[0].Name != "Communication" {
		t.Fatalf("card criteria = %+v", card.Criteria)
	}

	edit := f.input(service.OverallNo, 2, 2)
	edit.ID = card.ID
	// Handing the interview to someone else does not hand them the card.
	if _, err := f.sys.Exec(ctx, `update application set vetter_id = $1 where id = $2`, f.otherID, f.appID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.cards.Save(ctx, f.other(), edit); !errors.Is(err, service.ErrNotAuthor) {
		t.Fatalf("edit by another vetter = %v, want ErrNotAuthor", err)
	}
	if _, err := f.sys.Exec(ctx, `update application set vetter_id = $1 where id = $2`, f.userID, f.appID); err != nil {
		t.Fatal(err)
	}
	again, err := f.cards.Save(ctx, f.vetter(), edit)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != card.ID || again.Overall != service.OverallNo {
		t.Fatalf("author edit = %+v", again)
	}
}

func TestScorecardSatisfiesTheInterviewPrerequisite(t *testing.T) {
	f := newScorecardFixture(t)
	ctx := context.Background()
	rec := f.principal(service.RoleRecruiter)

	if _, err := f.move(t, rec, f.stages[domain.StageAssessment], service.MoveRequest{}); !errors.Is(err, domain.ErrPrereqMissing) {
		t.Fatalf("move before the scorecard = %v, want ErrPrereqMissing", err)
	}
	if _, err := f.cards.Save(ctx, f.vetter(), f.input(service.OverallStrongYes, 5, 5)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.move(t, rec, f.stages[domain.StageAssessment], service.MoveRequest{}); err != nil {
		t.Fatalf("move after the scorecard: %v", err)
	}

	// A strong yes is the signal the talent pool listens for.
	var kinds int
	if err := f.sys.QueryRow(ctx, `select count(*) from application_event where application_id = $1 and kind = $2`,
		f.appID, service.EventScorecardStrongYes).Scan(&kinds); err != nil {
		t.Fatal(err)
	}
	if kinds != 1 {
		t.Fatalf("strong-yes events = %d, want 1", kinds)
	}

	list, err := f.cards.List(ctx, rec, f.appID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Overall != service.OverallStrongYes || list[0].VetterName == "" {
		t.Fatalf("summary = %+v", list)
	}
}

func TestVetterAssignmentsShowScorecardStatus(t *testing.T) {
	f := newScorecardFixture(t)
	ctx := context.Background()
	rows, err := f.cards.Assignments(ctx, f.vetter())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ApplicationID != f.appID || rows[0].Submitted {
		t.Fatalf("assignments = %+v", rows)
	}
	if _, err := f.cards.Save(ctx, f.vetter(), f.input(service.OverallYes, 3, 3)); err != nil {
		t.Fatal(err)
	}
	rows, err = f.cards.Assignments(ctx, f.vetter())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || !rows[0].Submitted || rows[0].Overall != service.OverallYes {
		t.Fatalf("assignments after submit = %+v", rows)
	}
}

func TestAnotherOrgsScorecardsAreNotFound(t *testing.T) {
	f := newScorecardFixture(t)
	stranger := service.Principal{Kind: service.PrincipalOrgUser, OrgID: uuid.New(), UserID: uuid.New(), Roles: []string{service.RoleRecruiter}}
	if _, err := f.cards.List(context.Background(), stranger, f.appID); !errors.Is(err, service.ErrNotFound) {
		t.Errorf("list across orgs = %v, want ErrNotFound", err)
	}
	if _, err := f.cards.List(context.Background(), stranger, uuid.New()); !errors.Is(err, service.ErrNotFound) {
		t.Errorf("list of an unknown application = %v, want ErrNotFound", err)
	}
}
