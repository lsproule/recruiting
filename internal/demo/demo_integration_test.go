//go:build integration

package demo_test

import (
	"context"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"recruiting/internal/demo"
	"recruiting/internal/service"
	"recruiting/internal/store"
	"recruiting/internal/store/system"
)

// The demo must leave every queue rule with something to show and every
// screen with rows; it is what a first look at the product runs on.
func TestSeedFillsEveryStepOfTheQueue(t *testing.T) {
	ownerURL := os.Getenv("DATABASE_URL")
	if ownerURL == "" {
		t.Fatal("DATABASE_URL is not set; run `make dev-up` and use `make test-integration`")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	lock, err := pgx.Connect(ctx, ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	// The shared schema lock keeps the migration round-trip test out of the
	// database while this org exists; registered first so it is released
	// last, after the org is gone.
	t.Cleanup(func() { _ = lock.Close(context.Background()) })
	if _, err := lock.Exec(ctx, "select pg_advisory_lock_shared($1)", 7371); err != nil {
		t.Fatal(err)
	}
	if err := store.MigrateUp(ctx, ownerURL); err != nil {
		t.Fatal(err)
	}
	sys, err := system.Open(ctx, ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer sys.Close()

	name := "Demo " + time.Now().Format("150405.000")
	var report demo.Report
	err = sys.WithSystemTx(ctx, func(ctx context.Context, tx *store.Tx) error {
		var err error
		report, err = demo.Seed(ctx, tx, demo.Options{OrgName: name, Candidates: 40})
		return err
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	owner, err := pgxpool.New(ctx, ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	t.Cleanup(func() { _, _ = owner.Exec(context.Background(), `delete from org where id = $1`, report.OrgID) })

	for _, want := range []string{"clients", "jobs", "candidates", "applications", "attempts", "interviews", "scorecards", "sprints", "shortlists", "talent profiles", "introductions"} {
		if report.Counts[want] == 0 {
			t.Errorf("the demo wrote no %s: %v", want, report.Counts)
		}
	}
	if len(report.Logins) < 8 {
		t.Errorf("logins = %d, want the admin, the team, and a contact per client", len(report.Logins))
	}

	// The queue, read as the first recruiter, holds every rule.
	u, _ := url.Parse(ownerURL)
	u.User = url.UserPassword("app_rw", "app_rw")
	st, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var recruiterID [16]byte
	if err := owner.QueryRow(ctx, `select u.id from org_user u join org_user_role r on r.org_user_id = u.id where u.org_id = $1 and r.role = 'recruiter' order by u.created_at limit 1`, report.OrgID).Scan(&recruiterID); err != nil {
		t.Fatal(err)
	}
	p := service.Principal{Kind: service.PrincipalOrgUser, OrgID: report.OrgID, UserID: recruiterID, Roles: []string{service.RoleRecruiter}}
	counts, err := service.NewWorkQueueService(st).Counts(ctx, p)
	if err != nil {
		t.Fatalf("queue counts: %v", err)
	}
	for _, kind := range service.QueueKinds {
		if counts[kind] == 0 {
			t.Errorf("the demo queue has no %s rows: %v", kind, counts)
		}
	}
	// And the automation left its mark: an application rejected by the
	// system on its score.
	var auto int
	if err := owner.QueryRow(ctx, `select count(*) from application_event e join application a on a.id = e.application_id where a.org_id = $1 and e.actor_kind = 'system' and e.reason like 'Automatic:%'`, report.OrgID).Scan(&auto); err != nil {
		t.Fatal(err)
	}
	if auto == 0 {
		t.Error("no application was closed by an assessment stage's own rule")
	}
}
