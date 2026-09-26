//go:build integration

package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"recruiting/internal/queue"
	"recruiting/internal/service"
)

// fakePoster stands in for the browser tool.
type fakePoster struct {
	url  string
	err  error
	seen []service.PostRequest
}

func (f *fakePoster) Post(_ context.Context, req service.PostRequest) (service.PostResult, error) {
	f.seen = append(f.seen, req)
	if f.err != nil {
		return service.PostResult{}, f.err
	}
	return service.PostResult{URL: f.url, ID: "42"}, nil
}

func (f *pipelineFixture) postings(t *testing.T) *service.JobPostingService {
	t.Helper()
	q, err := queue.New(f.st.Pool(), queue.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return service.NewJobPostingService(f.st, q, "https://example.test/")
}

func (f *pipelineFixture) publish(t *testing.T, poster service.Poster, postingID uuid.UUID) error {
	t.Helper()
	payload, _ := json.Marshal(service.JobPostPublishPayload{PostingID: postingID, OrgID: f.orgID})
	return service.JobPostPublishHandler(f.st, poster, nil)(context.Background(), queue.Job{Kind: queue.KindJobPostPublish, Payload: payload})
}

func TestPostingWritesTheAdFromTheJobAndQueuesTheBrowser(t *testing.T) {
	f := newPipelineFixture(t)
	svc := f.postings(t)
	rec := f.principal(service.RoleRecruiter)
	ctx := context.Background()

	previews, err := svc.Preview(ctx, rec, f.jobID)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(previews) < 3 {
		t.Fatalf("previews = %d, want one per board", len(previews))
	}
	if !strings.Contains(previews[0].Posting.Body, "Globex is hiring a go engineer") {
		t.Errorf("copy opens with %q", strings.SplitN(previews[0].Posting.Body, "\n", 2)[0])
	}

	posting, err := svc.Post(ctx, rec, f.jobID, "demo")
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if posting.Status != service.PostingQueued || posting.Board != "demo" || !strings.HasPrefix(posting.ApplyURL, "https://example.test/apply/") {
		t.Fatalf("posting = %+v", posting)
	}
	if n := f.jobs(t, queue.KindJobPostPublish); n != 1 {
		t.Fatalf("publish jobs = %d, want 1", n)
	}
	if _, err := svc.Post(ctx, rec, f.jobID, "craigslist"); !errors.Is(err, service.ErrBadBoard) {
		t.Fatalf("unknown board = %v, want ErrBadBoard", err)
	}
	if _, err := svc.Post(ctx, f.principal(service.RoleVetter), f.jobID, "demo"); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("vetter posting = %v, want ErrForbidden", err)
	}

	// The worker's browser lands it; the row carries where.
	poster := &fakePoster{url: "http://board.example/jobs/42"}
	if err := f.publish(t, poster, posting.ID); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if len(poster.seen) != 1 || poster.seen[0].Board != "demo" || poster.seen[0].Title != posting.Title || poster.seen[0].ApplyURL != posting.ApplyURL {
		t.Fatalf("the poster was asked for %+v", poster.seen)
	}
	list, err := svc.List(ctx, rec, f.jobID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Status != service.PostingPosted || list[0].ExternalURL != "http://board.example/jobs/42" || list[0].ExternalID != "42" || list[0].PostedAt.IsZero() {
		t.Fatalf("after publish = %+v", list)
	}
	// A redelivery posts nothing twice.
	if err := f.publish(t, poster, posting.ID); err != nil || len(poster.seen) != 1 {
		t.Fatalf("redelivery: err %v, posts %d", err, len(poster.seen))
	}
}

func TestPostingRecordsAFailureAndCanBeRetried(t *testing.T) {
	f := newPipelineFixture(t)
	svc := f.postings(t)
	rec := f.principal(service.RoleRecruiter)
	ctx := context.Background()
	posting, err := svc.Post(ctx, rec, f.jobID, "linkedin")
	if err != nil {
		t.Fatal(err)
	}
	// The board said no: the row says why and the queue retries.
	err = f.publish(t, &fakePoster{err: errors.New("LinkedIn asked for a verification code")}, posting.ID)
	if err == nil || !strings.Contains(err.Error(), "verification code") {
		t.Fatalf("a board failure returned %v, want the error for the queue to retry", err)
	}
	list, _ := svc.List(ctx, rec, f.jobID)
	if list[0].Status != service.PostingFailed || !strings.Contains(list[0].Error, "verification code") || list[0].Attempts != 1 {
		t.Fatalf("after failure = %+v", list[0])
	}
	if err := svc.Retry(ctx, rec, posting.ID); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if n := f.jobs(t, queue.KindJobPostPublish); n != 2 {
		t.Fatalf("publish jobs after retry = %d, want 2", n)
	}

	// No tool configured: recorded, not retried into the void.
	if err := f.publish(t, nil, posting.ID); err != nil {
		t.Fatalf("publish without a poster = %v, want the failure recorded and the job done", err)
	}
	list, _ = svc.List(ctx, rec, f.jobID)
	if list[0].Status != service.PostingFailed || !strings.Contains(list[0].Error, "JOBPOST_CMD") {
		t.Fatalf("after publishing without a poster = %+v", list[0])
	}
	if err := svc.Remove(ctx, rec, posting.ID); err != nil {
		t.Fatalf("remove: %v", err)
	}
	list, _ = svc.List(ctx, rec, f.jobID)
	if list[0].Status != service.PostingRemoved {
		t.Fatalf("after remove = %+v", list[0])
	}
}

func TestPostingRefusesAClosedJob(t *testing.T) {
	f := newPipelineFixture(t)
	if _, err := f.sys.Exec(context.Background(), `update job set status = 'closed' where id = $1`, f.jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.postings(t).Post(context.Background(), f.principal(service.RoleRecruiter), f.jobID, "demo"); !errors.Is(err, service.ErrPostingClosedJob) {
		t.Fatalf("posting a closed job = %v", err)
	}
}
