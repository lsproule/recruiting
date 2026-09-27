package workqueue_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"recruiting/internal/service"
	"recruiting/internal/web/layout"
	"recruiting/internal/web/middleware"
	"recruiting/internal/web/workqueue"
)

// fakeQueue is a queue of fixed rows that counts how often it is read.
type fakeQueue struct {
	items      []service.QueueItem
	lists      atomic.Int64
	fromCounts atomic.Int64
	given      int
}

func (q *fakeQueue) List(_ context.Context, _ service.Principal, filter service.QueueKind) ([]service.QueueItem, error) {
	q.lists.Add(1)
	return service.FilterItems(q.items, filter), nil
}

func (q *fakeQueue) NavCountsFrom(_ context.Context, _ service.Principal, items []service.QueueItem) (map[string]int, error) {
	q.fromCounts.Add(1)
	q.given = len(items)
	return map[string]int{"queue": len(items), "clients": 4}, nil
}

func (q *fakeQueue) Decide(context.Context, service.Principal, service.DecideRequest) error {
	return nil
}

func (q *fakeQueue) Snooze(context.Context, service.Principal, service.QueueKind, uuid.UUID, time.Time) error {
	return nil
}

func row(kind service.QueueKind, who string) service.QueueItem {
	due := time.Now().Add(time.Hour)
	return service.QueueItem{
		Kind: kind, Who: who, JobTitle: "Go Engineer", ClientName: "Globex", Detail: "waiting",
		Due: &due, ActionLabel: "Open", ActionURL: "/app/applications/x",
		SubjectID: uuid.New(), ApplicationID: uuid.New(),
	}
}

// serve mounts the queue the way serve.go does, behind the counts
// middleware, with a signed-in recruiter on every request. The middleware's
// own source fails the test if it is ever asked: the screen must supply the
// badges from the list it draws.
func serve(t *testing.T, q *fakeQueue) http.Handler {
	t.Helper()
	p := service.Principal{Kind: service.PrincipalOrgUser, OrgID: uuid.New(), UserID: uuid.New(), Roles: []string{service.RoleRecruiter}}
	src := func(context.Context, service.Principal) (map[string]int, error) {
		t.Error("the counts middleware computed badges the queue screen already had")
		return nil, nil
	}
	mux := chi.NewMux()
	mux.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(middleware.WithPrincipal(r.Context(), p)))
		})
	})
	mux.Use(layout.WithCounts(src, nil))
	workqueue.Mount(mux, workqueue.Deps{Queue: q})
	return mux
}

func get(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	body, _ := io.ReadAll(rec.Result().Body)
	return rec.Code, string(body)
}

func TestQueueScreenReadsTheQueueOnceForRowsChipsAndBadge(t *testing.T) {
	q := &fakeQueue{items: []service.QueueItem{
		row(service.QueueResume, "Ada Lovelace"), row(service.QueueResume, "Bo Second"), row(service.QueueFeedback, "Cy Third"),
	}}
	h := serve(t, q)
	status, body := get(t, h, workqueue.Prefix)
	if status != http.StatusOK {
		t.Fatalf("GET %s = %d\n%s", workqueue.Prefix, status, body)
	}
	if n := q.lists.Load(); n != 1 {
		t.Fatalf("the screen listed the queue %d times, want 1", n)
	}
	if q.fromCounts.Load() != 1 || q.given != 3 {
		t.Fatalf("badges computed %d times from %d rows, want once from the 3 listed", q.fromCounts.Load(), q.given)
	}
	for _, want := range []string{
		"Ada Lovelace", "Bo Second", "Cy Third",
		// the sidebar badge: the queue's total, and the other badge the
		// screen was handed
		`<span class="nav-count">3</span>`, `<span class="nav-count">4</span>`,
		// the chips
		`Résumé review <span class="num muted">2</span>`, `Feedback due <span class="num muted">1</span>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("screen is missing %q", want)
		}
	}
}

// A filtered screen still reads once: the chips count every rule off the
// same list the rows are picked from.
func TestQueueScreenFiltersTheOneListItRead(t *testing.T) {
	q := &fakeQueue{items: []service.QueueItem{
		row(service.QueueResume, "Ada Lovelace"), row(service.QueueFeedback, "Cy Third"),
	}}
	h := serve(t, q)
	status, body := get(t, h, workqueue.Prefix+"?kind="+string(service.QueueFeedback))
	if status != http.StatusOK {
		t.Fatalf("GET = %d", status)
	}
	if n := q.lists.Load(); n != 1 {
		t.Fatalf("the filtered screen listed the queue %d times, want 1", n)
	}
	if strings.Contains(body, "Ada Lovelace") || !strings.Contains(body, "Cy Third") {
		t.Fatal("the filter did not narrow the rows to the feedback due")
	}
	for _, want := range []string{
		`Résumé review <span class="num muted">1</span>`, `Feedback due <span class="num muted">1</span>`,
		`<span class="nav-count">2</span>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("filtered screen is missing %q", want)
		}
	}
}
