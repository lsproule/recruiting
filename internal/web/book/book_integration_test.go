//go:build integration

package book_test

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
	"recruiting/internal/web/book"
	"recruiting/internal/web/middleware"
)

type fixture struct {
	srv   *httptest.Server
	token string
	sched *service.ScheduleService
	appID uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
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

	orgID, jobID, appID, vetterID, stageID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := sys.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`insert into org (id, name, slug) values ($1, $2, $2)`, orgID, orgID.String())
	t.Cleanup(func() { _, _ = sys.Exec(context.Background(), `delete from org where id = $1`, orgID) })
	exec(`insert into org_user (id, org_id, email, name, timezone) values ($1, $2, $3, 'Vera Vetter', 'Europe/Berlin')`, vetterID, orgID, "vet-"+orgID.String()+"@example.com")
	exec(`insert into org_user_role (org_user_id, org_id, role) values ($1, $2, 'vetter')`, vetterID, orgID)
	companyID := uuid.New()
	exec(`insert into client_company (id, org_id, name) values ($1, $2, 'Globex')`, companyID, orgID)
	exec(`insert into job (id, org_id, client_company_id, title, slug, status) values ($1, $2, $3, 'Go Engineer', $4, 'open')`, jobID, orgID, companyID, "go-"+orgID.String())
	exec(`insert into stage (id, org_id, job_id, position, name, kind, default_vetter_id) values ($1, $2, $3, 1, 'Phone screen', 'interview', $4)`, stageID, orgID, jobID, vetterID)
	candID := uuid.New()
	exec(`insert into candidate (id, org_id, email, name) values ($1, $2, $3, 'Ada Lovelace')`, candID, orgID, "ada-"+orgID.String()+"@example.com")
	exec(`insert into application (id, org_id, job_id, candidate_id, client_company_id, stage_id) values ($1, $2, $3, $4, $5, $6)`, appID, orgID, jobID, candID, companyID, stageID)
	for d := 0; d < 7; d++ {
		exec(`insert into availability_rule (org_id, vetter_id, weekday, start_time, end_time, timezone, slot_minutes) values ($1, $2, $3, '09:00', '17:00', 'Europe/Berlin', 30)`, orgID, vetterID, d)
	}

	links := service.NewMagicLinkService(st)
	token, _, err := links.Issue(ctx, service.Principal{Kind: service.PrincipalSystem, OrgID: orgID}, service.LinkBook, appID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{token: token, appID: appID, sched: service.NewScheduleService(st, nil, "https://example.test")}
	mux := chi.NewMux()
	if _, err := auth.Mount(mux, auth.Deps{Auth: service.NewAuthService(st), Links: links, CookieSecret: []byte("test-secret")}); err != nil {
		t.Fatal(err)
	}
	book.Mount(mux, book.Deps{Schedule: f.sched, Links: links})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

type visitor struct {
	t   *testing.T
	f   *fixture
	cli *http.Client
}

func (f *fixture) visitor(t *testing.T) *visitor {
	jar, _ := cookiejar.New(nil)
	return &visitor{t: t, f: f, cli: &http.Client{Jar: jar}}
}

func (v *visitor) get(path string) (*http.Response, string) {
	v.t.Helper()
	res, err := v.cli.Get(v.f.srv.URL + path)
	if err != nil {
		v.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

func (v *visitor) post(path string, form url.Values) (*http.Response, string) {
	v.t.Helper()
	u, _ := url.Parse(v.f.srv.URL)
	for _, c := range v.cli.Jar.Cookies(u) {
		if c.Name == middleware.CSRFCookie {
			form.Set(middleware.CSRFField, c.Value)
		}
	}
	res, err := v.cli.PostForm(v.f.srv.URL+path, form)
	if err != nil {
		v.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

// nextSlot is a bookable start a day out, at 10:00 Berlin time.
func nextSlot() time.Time {
	loc, _ := time.LoadLocation("Europe/Berlin")
	d := time.Now().In(loc).AddDate(0, 0, 2)
	return time.Date(d.Year(), d.Month(), d.Day(), 10, 0, 0, 0, loc).UTC()
}

func TestCandidateBooksReschedulesAndCancels(t *testing.T) {
	f := newFixture(t)
	v := f.visitor(t)
	path := "/book/" + f.token
	res, body := v.get(path)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "picker(") || !strings.Contains(body, "Ada Lovelace") {
		t.Fatalf("GET %s = %d\n%s", path, res.StatusCode, body)
	}
	start := nextSlot()
	res, body = v.post(path, url.Values{"starts_at": {start.Format(time.RFC3339)}, "timezone": {"Asia/Ho_Chi_Minh"}})
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "Booked for") || !strings.Contains(body, "Asia/Ho_Chi_Minh") {
		t.Fatalf("book = %d\n%s", res.StatusCode, body)
	}
	// A time that is not in the open list — taken, blocked, or made up — is a
	// conflict that comes back with the list refreshed.
	res, body = v.post(path, url.Values{"starts_at": {start.Add(7 * time.Minute).Format(time.RFC3339)}, "timezone": {"Asia/Ho_Chi_Minh"}})
	if res.StatusCode != http.StatusConflict || !strings.Contains(body, "refreshed") {
		t.Fatalf("rebook same slot = %d\n%s", res.StatusCode, body)
	}
	res, body = v.post(path, url.Values{"starts_at": {start.Add(time.Hour).Format(time.RFC3339)}, "timezone": {"Asia/Ho_Chi_Minh"}})
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "Booked for") {
		t.Fatalf("reschedule = %d\n%s", res.StatusCode, body)
	}
	res, body = v.post(path+"/cancel", url.Values{})
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "cancelled") {
		t.Fatalf("cancel = %d\n%s", res.StatusCode, body)
	}
	res, body = v.post(path+"/cancel", url.Values{})
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("second cancel = %d\n%s", res.StatusCode, body)
	}
}

func TestBadLinkIsRefused(t *testing.T) {
	f := newFixture(t)
	v := f.visitor(t)
	res, _ := v.get("/book/not-a-token")
	if res.StatusCode != http.StatusGone {
		t.Fatalf("bad token = %d", res.StatusCode)
	}
}
