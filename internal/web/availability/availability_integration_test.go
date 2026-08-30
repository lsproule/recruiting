//go:build integration

package availability_test

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"recruiting/internal/service"
	"recruiting/internal/store"
	"recruiting/internal/web/auth"
	"recruiting/internal/web/availability"
	"recruiting/internal/web/middleware"
)

const testPassword = "hunter2-long-enough"

func TestVetterSavesGridAndExceptions(t *testing.T) {
	ownerURL := os.Getenv("DATABASE_URL")
	if ownerURL == "" {
		t.Fatal("DATABASE_URL is not set; run `make dev-up` and use `make test-integration`")
	}
	conn, err := pgx.Connect(context.Background(), ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(context.Background(), "select pg_advisory_lock_shared($1)", 7371); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
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
	u, _ := url.Parse(ownerURL)
	u.User = url.UserPassword("app_rw", "app_rw")
	st, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)

	orgID, vetterID := uuid.New(), uuid.New()
	email := "vet-" + orgID.String() + "@example.com"
	hash, _ := service.HashPassword(testPassword)
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`insert into org (id, name, slug) values ($1, $2, $2)`, []any{orgID, orgID.String()}},
		{`insert into org_user (id, org_id, email, name, timezone) values ($1, $2, $3, $3, 'Europe/Berlin')`, []any{vetterID, orgID, email}},
		{`insert into org_user_role (org_user_id, org_id, role) values ($1, $2, 'vetter')`, []any{vetterID, orgID}},
		{`insert into org_user_credential (org_user_id, org_id, password_hash) values ($1, $2, $3)`, []any{vetterID, orgID, hash}},
	} {
		if _, err := sys.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _, _ = sys.Exec(context.Background(), `delete from org where id = $1`, orgID) })

	mux := chi.NewMux()
	if _, err := auth.Mount(mux, auth.Deps{Auth: service.NewAuthService(st), CookieSecret: []byte("test-secret")}); err != nil {
		t.Fatal(err)
	}
	availability.Mount(mux, availability.Deps{Schedule: service.NewScheduleService(st, nil, "https://example.test"), Org: service.NewOrgService(st)})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	jar, _ := cookiejar.New(nil)
	cli := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	csrf := func() string {
		u, _ := url.Parse(srv.URL)
		for _, c := range jar.Cookies(u) {
			if c.Name == middleware.CSRFCookie {
				return c.Value
			}
		}
		return ""
	}
	post := func(path string, form url.Values) (*http.Response, string) {
		t.Helper()
		form.Set(middleware.CSRFField, csrf())
		res, err := cli.PostForm(srv.URL+path, form)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res, string(b)
	}
	get := func(path string) (*http.Response, string) {
		t.Helper()
		res, err := cli.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res, string(b)
	}

	get("/app/login")
	if res, _ := post("/app/login", url.Values{"email": {email}, "password": {testPassword}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("login = %d", res.StatusCode)
	}
	res, body := get("/app/availability")
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "weekGrid(") || !strings.Contains(body, "Europe/Berlin") {
		t.Fatalf("GET availability = %d\n%s", res.StatusCode, body)
	}
	res, body = post("/app/availability/rules", url.Values{
		"timezone": {"Europe/Berlin"}, "slot_minutes": {"30"}, "buffer_minutes": {"10"},
		"rules": {`[{"weekday":1,"start":"09:00","end":"12:00"},{"weekday":3,"start":"13:00","end":"17:00"}]`},
	})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("save rules = %d\n%s", res.StatusCode, body)
	}
	res, body = post("/app/availability/rules", url.Values{
		"timezone": {"Europe/Berlin"}, "slot_minutes": {"30"}, "buffer_minutes": {"10"},
		"rules": {`[{"weekday":1,"start":"12:00","end":"09:00"}]`},
	})
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "ends before it starts") {
		t.Fatalf("bad rule = %d\n%s", res.StatusCode, body)
	}
	day := time.Now().AddDate(0, 0, 3).Format("2006-01-02")
	res, body = post("/app/availability/exceptions", url.Values{
		"timezone": {"Europe/Berlin"}, "start": {day + "T09:00"}, "end": {day + "T18:00"}, "reason": {"Holiday"},
	})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("add exception = %d\n%s", res.StatusCode, body)
	}
	res, body = get("/app/availability")
	if !strings.Contains(body, "Holiday") || !strings.Contains(body, `value="10"`) {
		t.Fatalf("availability after save:\n%s", body)
	}
	var rules int
	if err := sys.QueryRow(ctx, `select count(*) from availability_rule where vetter_id = $1`, vetterID).Scan(&rules); err != nil {
		t.Fatal(err)
	}
	if rules != 2 {
		t.Fatalf("stored rules = %d, want 2", rules)
	}
}
