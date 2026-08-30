//go:build integration

package service_test

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"recruiting/internal/domain"
	"recruiting/internal/runner/server"
	"recruiting/internal/service"
	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

// passExecutor answers that every reference solved every case, except for the
// sources named in fail.
type passExecutor struct{ fail map[string]bool }

func (e passExecutor) Execute(_ context.Context, req server.Request) (server.Response, error) {
	res := make([]server.TestResult, len(req.Tests))
	for i, t := range req.Tests {
		res[i] = server.TestResult{TestID: t.ID, Status: server.TestPass}
		if e.fail[req.Source] {
			res[i].Status = server.TestFail
			res[i].StderrTail = "wrong answer"
		}
	}
	return server.Response{ID: req.ID, Status: server.StatusOK, Results: res}, nil
}

// countingExecutor records that it was never asked to run anything.
type countingExecutor struct{ calls int }

func (e *countingExecutor) Execute(_ context.Context, req server.Request) (server.Response, error) {
	e.calls++
	return passExecutor{}.Execute(context.Background(), req)
}

type problemFixture struct {
	st    *store.Store
	owner *pgxpool.Pool
	orgID uuid.UUID
	p     service.Principal
}

func newProblemFixture(t *testing.T) *problemFixture {
	t.Helper()
	ownerURL := os.Getenv("DATABASE_URL")
	if ownerURL == "" {
		t.Fatal("DATABASE_URL is not set; run `make dev-up` and use `make test-integration`")
	}
	lockSchemaShared(t, ownerURL)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	if err := store.MigrateUp(ctx, ownerURL); err != nil {
		t.Fatal(err)
	}
	owner, err := pgxpool.New(ctx, ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owner.Close)
	u, _ := url.Parse(ownerURL)
	u.User = url.UserPassword("app_rw", "app_rw")
	st, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)

	f := &problemFixture{st: st, owner: owner, orgID: uuid.New()}
	if _, err := owner.Exec(ctx, `insert into org (id, name, slug) values ($1, $2, $2)`, f.orgID, f.orgID.String()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = owner.Exec(context.Background(), `delete from org where id = $1`, f.orgID) })
	f.p = service.Principal{Kind: service.PrincipalOrgUser, OrgID: f.orgID, Roles: []string{service.RoleRecruiter}}
	return f
}

// systemTx runs fn on the schema-owner connection, the way the admin CLI does.
func (f *problemFixture) systemTx(t *testing.T, ctx context.Context, fn func(tx *store.Tx) error) error {
	t.Helper()
	tx, err := f.owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := fn(&store.Tx{Tx: tx, Q: db.New(tx)}); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

func codeProblemJSON(title, source string) string {
	return `{"kind":"code","title":"` + title + `","statement":"Add them.","difficulty":"easy",
		"tags":["math"],"allowed_languages":["python"],
		"reference_solutions":[{"language":"python","source":"` + source + `"}],
		"test_cases":[{"input":"1 2","expected":"3","visibility":"public","weight":1},
			{"input":"2 2","expected":"4","visibility":"hidden","weight":2}]}`
}

func TestProblemCRUD(t *testing.T) {
	f := newProblemFixture(t)
	ctx := context.Background()
	svc := service.NewProblemService(f.st, passExecutor{})

	problems, err := domain.ParseProblemImport([]byte("[" + codeProblemJSON("Adder", "print(3)") + "]"))
	if err != nil {
		t.Fatal(err)
	}
	created, err := svc.Create(ctx, f.p, problems[0])
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(created.TestCases) != 2 || len(created.References) != 1 {
		t.Fatalf("created %+v, want 2 test cases and 1 reference", created)
	}
	if created.TestCases[1].Weight != 2 {
		t.Errorf("hidden case weight = %v, want 2", created.TestCases[1].Weight)
	}

	got, err := svc.Get(ctx, f.p, created.ID)
	if err != nil || got.Title != "Adder" {
		t.Fatalf("get = %+v, %v", got, err)
	}

	for _, tc := range []struct {
		name string
		f    service.ProblemFilter
		want bool
	}{
		{"by tag", service.ProblemFilter{Tag: "math"}, true},
		{"by other tag", service.ProblemFilter{Tag: "graphs"}, false},
		{"by difficulty", service.ProblemFilter{Difficulty: "easy"}, true},
		{"by other difficulty", service.ProblemFilter{Difficulty: "hard"}, false},
		{"by kind", service.ProblemFilter{Kind: "code"}, true},
		{"by other kind", service.ProblemFilter{Kind: "sql"}, false},
		{"by title", service.ProblemFilter{Query: "add"}, true},
	} {
		found, err := svc.List(ctx, f.p, tc.f)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		var seen bool
		for _, p := range found {
			seen = seen || p.ID == created.ID
		}
		if seen != tc.want {
			t.Errorf("%s: found = %v, want %v", tc.name, seen, tc.want)
		}
	}

	edit := created.AsImport()
	edit.Difficulty = "hard"
	edit.TestCases = edit.TestCases[:1]
	updated, err := svc.Update(ctx, f.p, created.ID, edit)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Difficulty != "hard" || len(updated.TestCases) != 1 {
		t.Fatalf("updated = %+v, want one hard case", updated)
	}

	if err := svc.Delete(ctx, f.p, created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := svc.Get(ctx, f.p, created.ID); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("get after delete = %v, want ErrNotFound", err)
	}
}

func TestImportRejectsTheWholeBatchWhenOneReferenceFails(t *testing.T) {
	f := newProblemFixture(t)
	ctx := context.Background()
	svc := service.NewProblemService(f.st, passExecutor{fail: map[string]bool{"print(9)": true}})

	doc := "[" + codeProblemJSON("Batch Good", "print(3)") + "," + codeProblemJSON("Batch Bad", "print(9)") + "]"
	_, err := svc.Import(ctx, f.p, []byte(doc))
	var report domain.ProblemImportErrors
	if !errors.As(err, &report) {
		t.Fatalf("import err = %v, want a per-problem report", err)
	}
	if len(report) != 1 || report[0].Title != "Batch Bad" {
		t.Fatalf("report = %+v, want only the failing problem named", report)
	}
	found, err := svc.List(ctx, f.p, service.ProblemFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range found {
		if p.OrgID == f.orgID {
			t.Fatalf("a rejected batch stored %q", p.Title)
		}
	}
}

func TestImportRejectsAnUnknownLanguageWithoutCallingTheRunner(t *testing.T) {
	f := newProblemFixture(t)
	svc := service.NewProblemService(f.st, passExecutor{})
	doc := strings.ReplaceAll("["+codeProblemJSON("Cobol Adder", "print(3)")+"]", `"python"`, `"cobol"`)
	_, err := svc.Import(context.Background(), f.p, []byte(doc))
	var report domain.ProblemImportErrors
	if !errors.As(err, &report) {
		t.Fatalf("import err = %v, want a per-problem report", err)
	}
	if !strings.Contains(err.Error(), "cobol") {
		t.Errorf("error %q does not name the unknown language", err)
	}
}

func TestImportStoresTheWholeBatchAndReimportsByTitle(t *testing.T) {
	f := newProblemFixture(t)
	ctx := context.Background()
	svc := service.NewProblemService(f.st, passExecutor{})

	doc := "[" + codeProblemJSON("Import One", "print(3)") + "," + codeProblemJSON("Import Two", "print(4)") + "]"
	imported, err := svc.Import(ctx, f.p, []byte(doc))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(imported) != 2 {
		t.Fatalf("imported %d problems, want 2", len(imported))
	}

	again, err := svc.Import(ctx, f.p, []byte(doc))
	if err != nil {
		t.Fatalf("re-import: %v", err)
	}
	if again[0].ID != imported[0].ID {
		t.Errorf("re-import created a second %q instead of replacing it", again[0].Title)
	}
	found, err := svc.List(ctx, f.p, service.ProblemFilter{Query: "Import "})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 {
		t.Fatalf("bank holds %d matching problems after two imports, want 2", len(found))
	}
}

func TestPlatformSeedIsReadableByAnOrgAndWritableByNone(t *testing.T) {
	f := newProblemFixture(t)
	ctx := context.Background()
	svc := service.NewProblemService(f.st, passExecutor{})

	seeded, err := f.seedPlatform(t, ctx, svc)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if len(seeded) < 8 {
		t.Fatalf("seeded %d problems, want the whole bank", len(seeded))
	}

	found, err := svc.List(ctx, f.p, service.ProblemFilter{})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[uuid.UUID]service.Problem{}
	for _, p := range found {
		byID[p.ID] = p
	}
	for _, p := range seeded {
		got, ok := byID[p.ID]
		if !ok {
			t.Fatalf("the org cannot see the platform problem %q", p.Title)
		}
		if !got.Platform() {
			t.Errorf("%q is not marked as a platform problem", got.Title)
		}
	}

	// The tenant policy's check constraint keeps a platform row out of reach,
	// and the refusal comes before any reference solution is executed.
	counted := &countingExecutor{}
	guarded := service.NewProblemService(f.st, counted)
	edit := seeded[0].AsImport()
	edit.Statement = "rewritten"
	if _, err := guarded.Update(ctx, f.p, seeded[0].ID, edit); !errors.Is(err, service.ErrPlatformProblem) {
		t.Fatalf("update of a platform problem = %v, want ErrPlatformProblem", err)
	}
	if counted.calls != 0 {
		t.Errorf("a refused platform edit still ran %d reference solutions", counted.calls)
	}
	if err := svc.Delete(ctx, f.p, seeded[0].ID); !errors.Is(err, service.ErrPlatformProblem) {
		t.Fatalf("delete of a platform problem = %v, want ErrPlatformProblem", err)
	}
	// RLS itself, not only the service, refuses the write.
	err = f.st.WithTx(ctx, f.p, func(ctx context.Context, tx *store.Tx) error {
		_, err := tx.Exec(ctx, `update problem set statement = 'rewritten' where id = $1`, seeded[0].ID)
		if err != nil {
			return err
		}
		var statement string
		return tx.QueryRow(ctx, `select statement from problem where id = $1`, seeded[0].ID).Scan(&statement)
	})
	if err != nil {
		t.Fatalf("platform read: %v", err)
	}
	var statement string
	if err := f.owner.QueryRow(ctx, `select statement from problem where id = $1`, seeded[0].ID).Scan(&statement); err != nil {
		t.Fatal(err)
	}
	if statement == "rewritten" {
		t.Error("a tenant rewrote a platform problem")
	}
}

func TestImportPlatformSeedIsIdempotent(t *testing.T) {
	f := newProblemFixture(t)
	ctx := context.Background()
	svc := service.NewProblemService(f.st, passExecutor{})

	first, err := f.seedPlatform(t, ctx, svc)
	if err != nil {
		t.Fatalf("first seed: %v", err)
	}
	second, err := f.seedPlatform(t, ctx, svc)
	if err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if len(first) != len(second) {
		t.Fatalf("seed sizes differ: %d then %d", len(first), len(second))
	}
	for i := range first {
		if first[i].ID != second[i].ID {
			t.Errorf("%q was duplicated rather than refreshed", first[i].Title)
		}
	}
	var n int
	if err := f.owner.QueryRow(ctx, `select count(*) from problem where org_id = $1`, service.PlatformOrgID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != len(first) {
		t.Errorf("platform org holds %d problems after two seeds, want %d", n, len(first))
	}
}

// seedPlatform loads the embedded bank the way the admin CLI would, and drops
// it again when the test ends so the shared database is left as it was.
func (f *problemFixture) seedPlatform(t *testing.T, ctx context.Context, svc *service.ProblemService) ([]service.Problem, error) {
	t.Helper()
	var out []service.Problem
	err := f.systemTx(t, ctx, func(tx *store.Tx) error {
		var err error
		out, err = svc.ImportPlatformSeed(ctx, tx)
		return err
	})
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() {
		_, _ = f.owner.Exec(context.Background(), `delete from problem where org_id = $1`, service.PlatformOrgID)
	})
	return out, nil
}
