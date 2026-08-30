//go:build integration

package main

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"recruiting/internal/config"
	"recruiting/internal/service"
	"recruiting/internal/store"
	"recruiting/internal/store/system"
)

const testBaseURL = "https://hire.example.test"

// TestCreateOrgBootstrapsOrg runs the CLI against the Compose Postgres and
// checks the org, its default pipeline template, its settings, and the
// one-time link the first admin uses to choose a password.
func TestCreateOrgBootstrapsOrg(t *testing.T) {
	ownerURL := os.Getenv("DATABASE_URL")
	if ownerURL == "" {
		t.Fatal("DATABASE_URL is not set; run `make dev-up` and use `make test-integration`")
	}
	lockSchema(t, ownerURL)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	if err := store.MigrateUp(ctx, ownerURL); err != nil {
		t.Fatal(err)
	}
	sys, err := pgxpool.New(ctx, ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sys.Close)

	name := "Acme " + uuid.NewString()
	slug := service.Slugify(name)
	t.Cleanup(func() { _, _ = sys.Exec(context.Background(), `delete from org where slug = $1`, slug) })
	email := slug + "@example.com"

	cfg := &config.Config{DatabaseURL: ownerURL, BaseURL: testBaseURL}
	var out bytes.Buffer
	if err := runAdminCmd(&out, discardLogger(), cfg, []string{"create-org", "--name", name, "--admin-email", email}); err != nil {
		t.Fatalf("create-org: %v", err)
	}

	link := regexp.MustCompile(regexp.QuoteMeta(testBaseURL) + `/app/reset/[A-Za-z0-9_-]+`).FindString(out.String())
	if link == "" {
		t.Fatalf("no password-set link printed:\n%s", out.String())
	}

	var orgID uuid.UUID
	if err := sys.QueryRow(ctx, `select id from org where slug = $1`, slug).Scan(&orgID); err != nil {
		t.Fatalf("org not created: %v", err)
	}

	// The first admin exists, holds the admin role, and has no password yet.
	var roles []string
	err = sys.QueryRow(ctx, `
		select array_agg(r.role order by r.role)
		from org_user u join org_user_role r on r.org_user_id = u.id
		where u.org_id = $1 and lower(u.email) = lower($2)`, orgID, email).Scan(&roles)
	if err != nil || len(roles) != 1 || roles[0] != service.RoleAdmin {
		t.Fatalf("admin roles = %v (%v)", roles, err)
	}
	var creds int
	if err := sys.QueryRow(ctx, `select count(*) from org_user_credential where org_id = $1`, orgID).Scan(&creds); err != nil || creds != 0 {
		t.Fatalf("credential count = %d (%v), want 0 before the link is used", creds, err)
	}

	// The default pipeline template is seeded in the same transaction.
	rows, err := sys.Query(ctx, `
		select s.name, s.kind, s.unblind
		from pipeline_template t join pipeline_template_stage s on s.template_id = t.id
		where t.org_id = $1 and t.is_default order by s.position`, orgID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []service.StageSpec
	for rows.Next() {
		var s service.StageSpec
		if err := rows.Scan(&s.Name, &s.Kind, &s.Unblind); err != nil {
			t.Fatal(err)
		}
		got = append(got, s)
	}
	if len(got) != len(service.DefaultPipelineStages) {
		t.Fatalf("seeded %d stages, want %d", len(got), len(service.DefaultPipelineStages))
	}
	for i, want := range service.DefaultPipelineStages {
		if got[i] != want {
			t.Errorf("stage %d = %+v, want %+v", i, got[i], want)
		}
	}

	// Default settings are written.
	settings := map[string]string{}
	srows, err := sys.Query(ctx, `select key, value::text from org_setting where org_id = $1`, orgID)
	if err != nil {
		t.Fatal(err)
	}
	defer srows.Close()
	for srows.Next() {
		var k, v string
		if err := srows.Scan(&k, &v); err != nil {
			t.Fatal(err)
		}
		settings[k] = v
	}
	if settings[service.SettingPoolScoreThreshold] != "80" {
		t.Errorf("%s = %q, want 80", service.SettingPoolScoreThreshold, settings[service.SettingPoolScoreThreshold])
	}
	if settings[service.SettingAssessmentInviteDays] != "7" {
		t.Errorf("%s = %q, want 7", service.SettingAssessmentInviteDays, settings[service.SettingAssessmentInviteDays])
	}
	for _, signal := range service.IntegritySignalNames {
		if !strings.Contains(settings[service.SettingIntegrityWeights], signal) {
			t.Errorf("%s has no weight for %s: %s", service.SettingIntegrityWeights, signal, settings[service.SettingIntegrityWeights])
		}
	}

	// The printed token is a live password-set token for that admin.
	token := strings.TrimPrefix(link, testBaseURL+"/app/reset/")
	u, _ := url.Parse(ownerURL)
	u.User = url.UserPassword("app_rw", "app_rw")
	st, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	auth := service.NewAuthService(st)
	if err := auth.CheckPasswordReset(ctx, token); err != nil {
		t.Fatalf("printed token is not usable: %v", err)
	}
	if err := auth.ResetPassword(ctx, token, "first-password-1"); err != nil {
		t.Fatalf("set first password: %v", err)
	}
	res, err := auth.Login(ctx, service.SurfaceApp, email, "first-password-1", "")
	if err != nil {
		t.Fatalf("admin login: %v", err)
	}
	if !res.Principal.HasRole(service.RoleAdmin) || res.Principal.OrgID != orgID {
		t.Errorf("principal = %+v", res.Principal)
	}

	// A second org with the same name collides on the slug rather than
	// producing two orgs that cannot be told apart.
	if err := runAdminCmd(&out, discardLogger(), cfg, []string{"create-org", "--name", name, "--admin-email", email}); err == nil {
		t.Error("duplicate slug was accepted")
	}
}

// TestBootstrapOrgRollsBackOnFailure proves the whole bootstrap is one
// transaction: a failure after the org row is inserted must leave nothing
// behind, not a half-built org with no admin or no pipeline template.
func TestBootstrapOrgRollsBackOnFailure(t *testing.T) {
	ownerURL := os.Getenv("DATABASE_URL")
	if ownerURL == "" {
		t.Fatal("DATABASE_URL is not set; run `make dev-up` and use `make test-integration`")
	}
	lockSchema(t, ownerURL)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	if err := store.MigrateUp(ctx, ownerURL); err != nil {
		t.Fatal(err)
	}
	sys, err := pgxpool.New(ctx, ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sys.Close)
	sysStore, err := system.Open(ctx, ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sysStore.Close)

	name := "Rollback " + uuid.NewString()
	slug := service.Slugify(name)
	email := slug + "@example.com"
	t.Cleanup(func() { _, _ = sys.Exec(context.Background(), `delete from org where slug = $1`, slug) })

	// The first bootstrap succeeds; the second fails on the unique slug, so
	// the transaction unwinds everything the first one wrote.
	var first service.BootstrapResult
	err = sysStore.WithSystemTx(ctx, func(ctx context.Context, tx *store.Tx) error {
		first, err = service.BootstrapOrg(ctx, tx, service.NewOrg{Name: name, AdminEmail: email})
		if err != nil {
			return err
		}
		_, err := service.BootstrapOrg(ctx, tx, service.NewOrg{Name: name, AdminEmail: "second-" + email})
		return err
	})
	if err == nil {
		t.Fatal("duplicate slug was accepted")
	}
	if first.OrgID == uuid.Nil || first.AdminUserID == uuid.Nil {
		t.Fatalf("the first bootstrap wrote nothing, so rollback proves nothing: %+v", first)
	}

	for _, q := range []struct {
		what string
		sql  string
		args []any
	}{
		{"org", `select count(*) from org where slug = $1`, []any{slug}},
		{"org_user", `select count(*) from org_user where lower(email) = lower($1)`, []any{email}},
		{"org_user_role", `select count(*) from org_user_role where org_id = $1`, []any{first.OrgID}},
		{"org_setting", `select count(*) from org_setting where org_id = $1`, []any{first.OrgID}},
		{"pipeline_template", `select count(*) from pipeline_template where org_id = $1`, []any{first.OrgID}},
		{"pipeline_template_stage", `select count(*) from pipeline_template_stage where org_id = $1`, []any{first.OrgID}},
		{"password_reset", `select count(*) from password_reset where org_id = $1`, []any{first.OrgID}},
	} {
		var n int
		if err := sys.QueryRow(ctx, q.sql, q.args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q.what, err)
		}
		if n != 0 {
			t.Errorf("%s: %d rows survived the rollback, want 0", q.what, n)
		}
	}
}

func lockSchema(t *testing.T, ownerURL string) {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), ownerURL)
	if err != nil {
		t.Fatalf("schema lock connect: %v", err)
	}
	if _, err := conn.Exec(context.Background(), "select pg_advisory_lock_shared($1)", 7371); err != nil {
		t.Fatalf("schema lock: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
}
