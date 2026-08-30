//go:build integration

package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"recruiting/internal/domain"
	"recruiting/internal/service"
	"recruiting/internal/store"
)

type poolFixture struct {
	*pipelineFixture
	pool      *service.PoolService
	candID    uuid.UUID
	companyID uuid.UUID
	otherJob  uuid.UUID
}

// newPoolFixture gives the fixture's job the skills a match is scored on and
// adds a second job at the same client company for suggestions to rank.
func newPoolFixture(t *testing.T) *poolFixture {
	t.Helper()
	pf := newPipelineFixture(t)
	ctx := context.Background()
	f := &poolFixture{pipelineFixture: pf, pool: service.NewPoolService(pf.st), otherJob: uuid.New()}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pf.sys.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := pf.sys.QueryRow(ctx, `select candidate_id, client_company_id from application where id = $1`, pf.appID).
		Scan(&f.candID, &f.companyID); err != nil {
		t.Fatal(err)
	}
	exec(`update job set skills = $2, seniority = 'senior', location = 'Berlin', remote_policy = 'remote' where id = $1`,
		pf.jobID, []string{"go", "postgres"})
	exec(`insert into job (id, org_id, client_company_id, title, slug, status, skills, seniority, location, remote_policy)
		values ($1, $2, $3, 'Platform Engineer', $4, 'open', $5, 'senior', 'Berlin', 'onsite')`,
		f.otherJob, pf.orgID, f.companyID, "plat-"+pf.orgID.String(), []string{"go", "postgres"})
	exec(`insert into stage (org_id, job_id, position, name, kind) values ($1, $2, 1, 'Applied', 'generic')`, pf.orgID, f.otherJob)
	return f
}

func (f *poolFixture) entry(t *testing.T) service.PoolEntry {
	t.Helper()
	list, err := f.pool.List(context.Background(), f.principal(service.RoleRecruiter), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("pool holds %d entries, want 1", len(list))
	}
	return list[0]
}

func TestFlaggingAnApplicationFilesAPoolEntry(t *testing.T) {
	f := newPoolFixture(t)
	ctx := context.Background()
	rec := f.principal(service.RoleRecruiter)

	entry, err := f.pool.Flag(ctx, rec, f.appID)
	if err != nil {
		t.Fatal(err)
	}
	if entry.CandidateID != f.candID || entry.CandidateName != "Ada Lovelace" {
		t.Fatalf("entry = %+v", entry)
	}
	if len(entry.Skills) != 2 || entry.Seniority != "senior" || entry.Location != "Berlin" || !entry.RemoteOK {
		t.Fatalf("entry did not take the job's profile: %+v", entry)
	}
	if len(entry.SourceJobIDs) != 1 || entry.SourceJobIDs[0] != f.jobID {
		t.Fatalf("source jobs = %v", entry.SourceJobIDs)
	}
	if entry.Source != service.PoolSourceFlag {
		t.Fatalf("source = %q", entry.Source)
	}
	var flagged bool
	if err := f.sys.QueryRow(ctx, `select high_quality from application where id = $1`, f.appID).Scan(&flagged); err != nil {
		t.Fatal(err)
	}
	if !flagged {
		t.Error("the application was not marked high quality")
	}
	if _, err := f.pool.Flag(ctx, f.principal(service.RoleVetter), f.appID); !errors.Is(err, service.ErrForbidden) {
		t.Errorf("flag by a vetter = %v, want ErrForbidden", err)
	}
}

func TestPoolEntriesAreUpsertedOncePerCandidate(t *testing.T) {
	f := newPoolFixture(t)
	ctx := context.Background()
	rec := f.principal(service.RoleRecruiter)

	first, err := f.pool.Flag(ctx, rec, f.appID)
	if err != nil {
		t.Fatal(err)
	}
	edited, err := f.pool.Update(ctx, rec, first.ID, service.PoolEdit{Skills: []string{"go", "kubernetes"}, Notes: "great call"})
	if err != nil {
		t.Fatal(err)
	}
	if len(edited.Skills) != 2 || edited.Notes != "great call" {
		t.Fatalf("edited = %+v", edited)
	}

	// A second pool-worthy event on the same candidate updates the one entry
	// rather than filing another, and leaves the recruiter's notes alone.
	err = f.st.WithTx(ctx, rec, func(ctx context.Context, tx *store.Tx) error {
		return f.pool.OnStrongYes(ctx, tx, f.orgID, f.appID)
	})
	if err != nil {
		t.Fatal(err)
	}
	again := f.entry(t)
	if again.ID != first.ID {
		t.Fatalf("entry id changed from %v to %v", first.ID, again.ID)
	}
	if again.Notes != "great call" {
		t.Errorf("notes = %q, want the recruiter's", again.Notes)
	}
	if again.Source != service.PoolSourceScorecard {
		t.Errorf("source = %q, want %q", again.Source, service.PoolSourceScorecard)
	}
	// Merged, not replaced: the job's skills and the recruiter's both stand.
	if len(again.Skills) != 3 {
		t.Errorf("skills = %v, want go, postgres and kubernetes", again.Skills)
	}
}

func TestAReviewBelowTheThresholdFilesNothing(t *testing.T) {
	f := newPoolFixture(t)
	ctx := context.Background()
	rec := f.principal(service.RoleRecruiter)
	err := f.st.WithTx(ctx, rec, func(ctx context.Context, tx *store.Tx) error {
		return f.pool.OnReviewPass(ctx, tx, f.orgID, f.appID, 60, 80)
	})
	if err != nil {
		t.Fatal(err)
	}
	list, err := f.pool.List(ctx, rec, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("pool holds %d entries, want none", len(list))
	}
	err = f.st.WithTx(ctx, rec, func(ctx context.Context, tx *store.Tx) error {
		return f.pool.OnReviewPass(ctx, tx, f.orgID, f.appID, 80, 80)
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.entry(t); got.Source != service.PoolSourceReview {
		t.Errorf("source = %q, want %q", got.Source, service.PoolSourceReview)
	}
}

func TestPoolSearchAndRemoval(t *testing.T) {
	f := newPoolFixture(t)
	ctx := context.Background()
	rec := f.principal(service.RoleRecruiter)
	entry, err := f.pool.Flag(ctx, rec, f.appID)
	if err != nil {
		t.Fatal(err)
	}
	found, err := f.pool.List(ctx, rec, "lovelace")
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 {
		t.Fatalf("search by name found %d entries, want 1", len(found))
	}
	missed, err := f.pool.List(ctx, rec, "haskell")
	if err != nil {
		t.Fatal(err)
	}
	if len(missed) != 0 {
		t.Fatalf("search for an unheld skill found %d entries", len(missed))
	}
	if err := f.pool.Remove(ctx, rec, entry.ID); err != nil {
		t.Fatal(err)
	}
	left, err := f.pool.List(ctx, rec, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("pool holds %d entries after removal", len(left))
	}
	if _, err := f.pool.Entry(ctx, rec, entry.ID); !errors.Is(err, service.ErrNotFound) {
		t.Errorf("reading a removed entry = %v, want ErrNotFound", err)
	}
}

func TestSuggestionsRankTheJobAndAddCreatesTheApplication(t *testing.T) {
	f := newPoolFixture(t)
	ctx := context.Background()
	rec := f.principal(service.RoleRecruiter)
	entry, err := f.pool.Flag(ctx, rec, f.appID)
	if err != nil {
		t.Fatal(err)
	}

	got, err := f.pool.Suggestions(ctx, rec, f.otherJob)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d suggestions, want 1", len(got))
	}
	// Identical skills, the same rung, the same city, no assessment yet.
	if got[0].Breakdown.Skills != 1 || got[0].Breakdown.Seniority != 1 || got[0].Breakdown.Location != 1 {
		t.Fatalf("breakdown = %+v", got[0].Breakdown)
	}
	if got[0].Score < 0.89 || got[0].Score > 0.91 {
		t.Errorf("score = %v, want 0.9", got[0].Score)
	}

	// The job the entry came from already holds the candidate.
	same, err := f.pool.Suggestions(ctx, rec, f.jobID)
	if err != nil {
		t.Fatal(err)
	}
	if len(same) != 0 {
		t.Fatalf("the source job suggested %d entries, want none", len(same))
	}

	appID, err := f.pool.AddToJob(ctx, rec, f.otherJob, entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	var stageName, eventKind string
	err = f.sys.QueryRow(ctx, `select s.name, e.kind from application a
		join stage s on s.id = a.stage_id
		join application_event e on e.application_id = a.id
		where a.id = $1`, appID).Scan(&stageName, &eventKind)
	if err != nil {
		t.Fatal(err)
	}
	if stageName != "Applied" || eventKind != "applied" {
		t.Fatalf("added into stage %q with event %q", stageName, eventKind)
	}
	if _, err := f.pool.AddToJob(ctx, rec, f.otherJob, entry.ID); !errors.Is(err, service.ErrPoolAlreadyOnJob) {
		t.Errorf("adding twice = %v, want ErrPoolAlreadyOnJob", err)
	}
	after, err := f.pool.Suggestions(ctx, rec, f.otherJob)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Fatalf("the candidate is still suggested for a job they are on: %d", len(after))
	}
}

func TestARecentClientRejectionIsNotSuggestedBack(t *testing.T) {
	f := newPoolFixture(t)
	ctx := context.Background()
	rec := f.principal(service.RoleRecruiter)
	if _, err := f.pool.Flag(ctx, rec, f.appID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.apps.Move(ctx, rec, service.MoveRequest{ApplicationID: f.appID, ToStageID: f.reject, Reason: "not a fit"}); err != nil {
		t.Fatal(err)
	}
	got, err := f.pool.Suggestions(ctx, rec, f.otherJob)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("a candidate this client rejected today was suggested back: %d", len(got))
	}

	// A later edit of the application must not stand in for the rejection:
	// only the move that closed it decides when the window opened.
	if _, err := f.sys.Exec(ctx, `update application set updated_at = now() where id = $1`, f.appID); err != nil {
		t.Fatal(err)
	}
	old := time.Now().AddDate(0, -domain.PoolRejectionWindowMonths, -1)
	if _, err := f.sys.Exec(ctx, `update application_event set created_at = $2 where application_id = $1`, f.appID, old); err != nil {
		t.Fatal(err)
	}
	got, err = f.pool.Suggestions(ctx, rec, f.otherJob)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d suggestions after the rejection aged out, want 1", len(got))
	}
}

func TestAddingToAJobThatIsNotOpenIsRefused(t *testing.T) {
	f := newPoolFixture(t)
	ctx := context.Background()
	rec := f.principal(service.RoleRecruiter)
	entry, err := f.pool.Flag(ctx, rec, f.appID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.sys.Exec(ctx, `update job set status = 'closed' where id = $1`, f.otherJob); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.AddToJob(ctx, rec, f.otherJob, entry.ID); !errors.Is(err, service.ErrJobNotOpen) {
		t.Fatalf("add to a closed job = %v, want ErrJobNotOpen", err)
	}
}

func TestWhereTheCandidateWorksIsEditableAndFollowsTheLatestJob(t *testing.T) {
	f := newPoolFixture(t)
	ctx := context.Background()
	rec := f.principal(service.RoleRecruiter)

	// A hybrid role is not a remote one, so it leaves the entry on its city.
	if _, err := f.sys.Exec(ctx, `update job set remote_policy = 'hybrid' where id = $1`, f.jobID); err != nil {
		t.Fatal(err)
	}
	entry, err := f.pool.Flag(ctx, rec, f.appID)
	if err != nil {
		t.Fatal(err)
	}
	if entry.RemoteOK || entry.Location != "Berlin" {
		t.Fatalf("a hybrid job made the entry %+v", entry)
	}

	edited, err := f.pool.Update(ctx, rec, entry.ID, service.PoolEdit{
		Skills: entry.Skills, Location: "Lisbon", RemoteOK: true, Notes: entry.Notes,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !edited.RemoteOK || edited.Location != "Lisbon" {
		t.Fatalf("the recruiter's correction did not stick: %+v", edited)
	}

	// The next pool-worthy event re-reads the job it came from rather than
	// keeping whatever the entry had accumulated.
	if _, err := f.sys.Exec(ctx, `update job set remote_policy = 'onsite', location = 'Munich' where id = $1`, f.jobID); err != nil {
		t.Fatal(err)
	}
	err = f.st.WithTx(ctx, rec, func(ctx context.Context, tx *store.Tx) error {
		return f.pool.OnStrongYes(ctx, tx, f.orgID, f.appID)
	})
	if err != nil {
		t.Fatal(err)
	}
	again := f.entry(t)
	if again.RemoteOK || again.Location != "Munich" {
		t.Fatalf("the entry did not follow the latest source job: %+v", again)
	}
}

func TestARemovedEntryStaysRemovedUntilARecruiterFlagsItAgain(t *testing.T) {
	f := newPoolFixture(t)
	ctx := context.Background()
	rec := f.principal(service.RoleRecruiter)
	entry, err := f.pool.Flag(ctx, rec, f.appID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.pool.Remove(ctx, rec, entry.ID); err != nil {
		t.Fatal(err)
	}
	err = f.st.WithTx(ctx, rec, func(ctx context.Context, tx *store.Tx) error {
		return f.pool.OnStrongYes(ctx, tx, f.orgID, f.appID)
	})
	if err != nil {
		t.Fatal(err)
	}
	list, err := f.pool.List(ctx, rec, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("an automatic source revived a removed entry: %+v", list)
	}
	if got, err := f.pool.Suggestions(ctx, rec, f.otherJob); err != nil || len(got) != 0 {
		t.Fatalf("a removed entry is still suggested: %v %d", err, len(got))
	}
	if _, err := f.pool.Flag(ctx, rec, f.appID); err != nil {
		t.Fatal(err)
	}
	if back := f.entry(t); back.ID != entry.ID {
		t.Fatalf("flagging filed a second entry: %v then %v", entry.ID, back.ID)
	}
}
