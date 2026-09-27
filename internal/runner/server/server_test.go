package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakeExecutor struct{ calls atomic.Int32 }

func (f *fakeExecutor) Execute(_ context.Context, req *Request) *Response {
	f.calls.Add(1)
	return &Response{ID: req.ID, Status: StatusOK, Results: []TestResult{{TestID: req.Tests[0].ID, Status: TestPass}}}
}

func newTestServer(t *testing.T) (*httptest.Server, *fakeExecutor) {
	t.Helper()
	exec := &fakeExecutor{}
	h := NewHandler("s3cret", exec, nil)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, exec
}

const body = `{"id":"11111111-1111-1111-1111-111111111111","language":"python","source":"print(1)",
 "tests":[{"id":"22222222-2222-2222-2222-222222222222","input":"","expected":"1","weight":1}],
 "limits":{"cpu_ms":2000,"wall_ms":5000,"mem_mb":256,"output_kb":64,"pids":64}}`

func post(t *testing.T, srv *httptest.Server, secret, payload string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/execute", strings.NewReader(payload))
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestExecuteRejectsBadSecret(t *testing.T) {
	srv, exec := newTestServer(t)
	for _, s := range []string{"", "wrong"} {
		resp := post(t, srv, s, body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("secret %q: status %d, want 401", s, resp.StatusCode)
		}
	}
	if exec.calls.Load() != 0 {
		t.Fatal("executor ran without auth")
	}
}

func TestExecuteIsIdempotentByID(t *testing.T) {
	srv, exec := newTestServer(t)
	var first, second Response
	for i, dst := range []*Response{&first, &second} {
		resp := post(t, srv, "s3cret", body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("call %d: status %d", i, resp.StatusCode)
		}
		if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if exec.calls.Load() != 1 {
		t.Fatalf("executor ran %d times, want 1", exec.calls.Load())
	}
	if first.ID != "11111111-1111-1111-1111-111111111111" || first.Status != StatusOK || second.Status != StatusOK {
		t.Fatalf("unexpected responses %+v %+v", first, second)
	}
}

func TestExecuteValidatesRequest(t *testing.T) {
	srv, _ := newTestServer(t)
	for _, payload := range []string{`{}`, `{"id":"x","language":"cobol","source":"","tests":[]}`, `not json`} {
		resp := post(t, srv, "s3cret", payload)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("payload %s: status %d, want 400", payload, resp.StatusCode)
		}
	}
}

func TestResolveRuntimePolicy(t *testing.T) {
	cases := []struct {
		name          string
		want, allow   string
		available     []string
		wantRuntime   string
		wantErr, warn bool
	}{
		{"gvisor present", "runsc", "", []string{"runc", "runsc"}, "runsc", false, false},
		{"gvisor absent refuses", "runsc", "", []string{"runc"}, "", true, false},
		{"gvisor absent with override", "runsc", "1", []string{"runc"}, "runc", false, true},
		{"unknown runtime never falls back silently", "kata", "", []string{"runc"}, "", true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rt, warn, err := resolveRuntime(c.want, c.allow == "1", c.available)
			if (err != nil) != c.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, c.wantErr)
			}
			if rt != c.wantRuntime || warn != c.warn {
				t.Fatalf("got runtime=%q warn=%v, want %q %v", rt, warn, c.wantRuntime, c.warn)
			}
		})
	}
}

func TestDockerRunArgsHardenContainer(t *testing.T) {
	d := &DockerExecutor{Runtime: "runc", ImagePrefix: "recruiting-runner-"}
	args := d.runArgs("c1", "python", Limits{CPUMs: 1000, WallMs: 2000, MemMB: 64, OutputKB: 8, PIDs: 16})
	joined := " " + strings.Join(args, " ") + " "
	for _, want := range []string{
		" --runtime=runc ", " --network=none ", " --read-only ", " --tmpfs=/tmp:rw,exec,", " --cap-drop=ALL ",
		" --security-opt=no-new-privileges ", " --pids-limit=16 ", " --memory=192m ", " --memory-swap=192m ",
		" --cpus=", " --user=65534:65534 ", " --name=c1 ", " --rm ", " recruiting-runner-python ",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %q", want, joined)
		}
	}
}

func TestResultCacheBoundedAndExpiring(t *testing.T) {
	now := time.Unix(0, 0)
	c := newResultCache(2, 0, time.Hour, time.Minute)
	c.now = func() time.Time { return now }
	c.put("a", &Response{ID: "a", Status: StatusOK})
	c.put("err", &Response{ID: "err", Status: StatusError})
	c.put("b", &Response{ID: "b", Status: StatusOK}) // evicts oldest: a
	if _, ok := c.get("a"); ok {
		t.Fatal("cache not bounded: oldest entry survived")
	}
	if _, ok := c.get("err"); !ok {
		t.Fatal("error result should be cached briefly")
	}
	now = now.Add(2 * time.Minute)
	if _, ok := c.get("err"); ok {
		t.Fatal("error result cached beyond its short TTL")
	}
	if _, ok := c.get("b"); !ok {
		t.Fatal("ok result expired too early")
	}
	now = now.Add(time.Hour)
	if _, ok := c.get("b"); ok {
		t.Fatal("ok result never expired")
	}
	c.sweep()
	if n := c.len(); n != 0 {
		t.Fatalf("sweep left %d entries", n)
	}
}

func TestExecuteRejectsTooManyTests(t *testing.T) {
	srv, exec := newTestServer(t)
	tests := make([]string, 0, MaxTests+1)
	for i := 0; i <= MaxTests; i++ {
		tests = append(tests, fmt.Sprintf(`{"id":"t%d","input":"","expected":"","weight":1}`, i))
	}
	payload := `{"id":"many","language":"python","source":"x","tests":[` + strings.Join(tests, ",") + `]}`
	resp := post(t, srv, "s3cret", payload)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || exec.calls.Load() != 0 {
		t.Fatalf("status %d calls %d", resp.StatusCode, exec.calls.Load())
	}
}

func TestExecutionBudgetCapped(t *testing.T) {
	if b := executionBudget(MaxTests, MaxLimits); b > MaxBudget {
		t.Fatalf("budget %s exceeds cap %s", b, MaxBudget)
	}
	if b := executionBudget(1, DefaultLimits); b <= time.Duration(DefaultLimits.WallMs)*time.Millisecond {
		t.Fatalf("budget %s leaves no room for compile", b)
	}
}

type blockingExecutor struct{ release chan struct{} }

func (b *blockingExecutor) Execute(_ context.Context, req *Request) *Response {
	<-b.release
	return &Response{ID: req.ID, Status: StatusOK}
}

func withID(id string) string {
	return strings.Replace(body, "11111111-1111-1111-1111-111111111111", id, 1)
}

// newQueuedServer serves a handler with capacity slots, queueCap waiters and
// a wait bound, backed by an executor that blocks until released.
func newQueuedServer(t *testing.T, capacity, queueCap int, wait time.Duration) (*httptest.Server, *Handler, *blockingExecutor) {
	t.Helper()
	exec := &blockingExecutor{release: make(chan struct{})}
	h := NewHandler("s3cret", exec, nil)
	h.admit = newAdmission(capacity, queueCap, wait)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, h, exec
}

// waitFor polls until cond holds or the test's patience runs out.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// One slot, one queue place: the first request runs, the second waits, the
// third is turned away at once with a Retry-After computed from the queue,
// and the waiter is served when the slot frees.
func TestQueueAdmitsWaitsAndRejects(t *testing.T) {
	srv, h, exec := newQueuedServer(t, 1, 1, 10*time.Second)
	results := make(chan *http.Response, 2)
	go func() { results <- post(t, srv, "s3cret", withID("first")) }()
	waitFor(t, "the first request to be in flight", func() bool { return h.admit.status().InFlight == 1 })
	go func() { results <- post(t, srv, "s3cret", withID("second")) }()
	waitFor(t, "the second request to be queued", func() bool { return h.admit.status().Queued == 1 })

	resp := post(t, srv, "s3cret", withID("third"))
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("third request: status %d, want 503", resp.StatusCode)
	}
	ra, err := strconv.Atoi(resp.Header.Get("Retry-After"))
	if err != nil || ra < int(minRetryAfter.Seconds()) || ra > int(maxRetryAfter.Seconds()) {
		t.Fatalf("Retry-After %q, want seconds within [%s, %s]", resp.Header.Get("Retry-After"), minRetryAfter, maxRetryAfter)
	}
	if st := h.admit.status(); st.InFlight != 1 || st.Queued != 1 || st.Capacity != 1 || st.QueueCapacity != 1 {
		t.Fatalf("status after rejection = %+v", st)
	}

	close(exec.release)
	for i := 0; i < 2; i++ {
		r := <-results
		var out Response
		if err := json.NewDecoder(r.Body).Decode(&out); err != nil || r.StatusCode != http.StatusOK || out.Status != StatusOK {
			t.Fatalf("queued request %d: status %d, body %+v, err %v", i, r.StatusCode, out, err)
		}
		r.Body.Close()
	}
	waitFor(t, "the slots to be released", func() bool {
		st := h.admit.status()
		return st.InFlight == 0 && st.Queued == 0
	})
	// A repeated id for a finished run is served from cache even when saturated.
	h.admit.slots <- struct{}{}
	resp = post(t, srv, "s3cret", withID("first"))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cached result blocked by saturation: %d", resp.StatusCode)
	}
}

// A waiter whose bound expires is turned away like one that never got in,
// and leaves the queue so the place is free for the next arrival.
func TestQueueWaitExpiresInto503(t *testing.T) {
	srv, h, exec := newQueuedServer(t, 1, 2, 50*time.Millisecond)
	done := make(chan *http.Response, 1)
	go func() { done <- post(t, srv, "s3cret", withID("first")) }()
	waitFor(t, "the first request to be in flight", func() bool { return h.admit.status().InFlight == 1 })

	start := time.Now()
	resp := post(t, srv, "s3cret", withID("waiter"))
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("expired waiter: status %d retry-after %q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	if waited := time.Since(start); waited < 50*time.Millisecond || waited > 3*time.Second {
		t.Fatalf("waiter came back after %s, want about the 50ms bound", waited)
	}
	if st := h.admit.status(); st.Queued != 0 {
		t.Fatalf("expired waiter still counted: %+v", st)
	}
	if _, pending := h.inflight["waiter"]; pending {
		t.Fatal("expired waiter left an in-flight entry")
	}
	close(exec.release)
	(<-done).Body.Close()
}

// A retry of an id that is still waiting joins that wait rather than taking
// a second place in the queue.
func TestQueueSharesAWaitingID(t *testing.T) {
	srv, h, exec := newQueuedServer(t, 1, 1, 10*time.Second)
	results := make(chan *http.Response, 3)
	go func() { results <- post(t, srv, "s3cret", withID("first")) }()
	waitFor(t, "the first request to be in flight", func() bool { return h.admit.status().InFlight == 1 })
	for i := 0; i < 2; i++ {
		go func() { results <- post(t, srv, "s3cret", withID("same")) }()
	}
	waitFor(t, "the shared id to be queued", func() bool { return h.admit.status().Queued == 1 })
	time.Sleep(50 * time.Millisecond)
	if st := h.admit.status(); st.Queued != 1 {
		t.Fatalf("the same id took %d queue places, want 1", st.Queued)
	}
	close(exec.release)
	for i := 0; i < 3; i++ {
		r := <-results
		r.Body.Close()
		if r.StatusCode != http.StatusOK {
			t.Fatalf("request %d: status %d", i, r.StatusCode)
		}
	}
}

// Retry-After grows with the queue and the measured execution time, within
// its bounds, so a caller waits about as long as its turn takes to come.
func TestRetryAfterFollowsQueueDepthAndDuration(t *testing.T) {
	a := newAdmission(2, 8, time.Second)
	a.avg = 3 * time.Second
	a.queued = 1
	if got := a.retryAfterLocked(); got != minRetryAfter {
		t.Errorf("one waiter, 3s average, 2 slots: %s, want the %s floor", got, minRetryAfter)
	}
	a.queued = 8
	if got := a.retryAfterLocked(); got != 12*time.Second {
		t.Errorf("eight waiters, 3s average, 2 slots: %s, want 12s", got)
	}
	a.avg = 5 * time.Minute
	if got := a.retryAfterLocked(); got != maxRetryAfter {
		t.Errorf("slow executions: %s, want the %s ceiling", got, maxRetryAfter)
	}

	// A release updates the average and the activity clock.
	now := time.Unix(1000, 0)
	a.now = func() time.Time { return now }
	a.started = now
	a.queued = 0
	a.avg = 4 * time.Second
	release, err := a.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(8 * time.Second)
	release(StatusOK)
	if a.avg != 5*time.Second {
		t.Errorf("average after an 8s run on a 4s average = %s, want 5s", a.avg)
	}
	if a.idle() != 0 {
		t.Errorf("idle right after a run = %s, want 0", a.idle())
	}
	now = now.Add(90 * time.Second)
	if a.idle() != 90*time.Second {
		t.Errorf("idle 90s after a run = %s", a.idle())
	}
}

func TestIdleIsZeroWhileBusyAndCountsFromStart(t *testing.T) {
	now := time.Unix(1000, 0)
	a := newAdmission(1, 1, time.Second)
	a.now = func() time.Time { return now }
	a.started = now
	now = now.Add(time.Minute)
	if a.idle() != time.Minute {
		t.Errorf("idle with no execution ever = %s, want a minute since start", a.idle())
	}
	release, err := a.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if a.idle() != 0 {
		t.Errorf("idle while an execution runs = %s, want 0", a.idle())
	}
	release(StatusOK)
	if a.idle() != 0 {
		t.Errorf("idle right after the execution = %s, want 0", a.idle())
	}
}

// A caller that disconnects while waiting leaves the queue without running.
func TestQueueDropsADisconnectedWaiter(t *testing.T) {
	a := newAdmission(1, 4, 10*time.Second)
	release, err := a.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := a.acquire(ctx)
		errc <- err
	}()
	waitFor(t, "the waiter to queue", func() bool { return a.status().Queued == 1 })
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the caller's cancellation", err)
	}
	if st := a.status(); st.Queued != 0 {
		t.Fatalf("cancelled waiter still queued: %+v", st)
	}
	release(StatusOK)
}

func TestStatusEndpoint(t *testing.T) {
	srv, h, exec := newQueuedServer(t, 3, 5, time.Second)
	get := func(secret string) (*http.Response, snapshot) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/status", nil)
		if secret != "" {
			req.Header.Set("Authorization", "Bearer "+secret)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var st snapshot
		_ = json.NewDecoder(resp.Body).Decode(&st)
		return resp, st
	}
	if resp, _ := get(""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated /status: %d", resp.StatusCode)
	}
	resp, st := get("s3cret")
	if resp.StatusCode != http.StatusOK || st.InFlight != 0 || st.Queued != 0 || st.Capacity != 3 || st.QueueCapacity != 5 || st.IdleSeconds < 0 {
		t.Fatalf("idle status = %d %+v", resp.StatusCode, st)
	}
	done := make(chan *http.Response, 1)
	go func() { done <- post(t, srv, "s3cret", withID("busy")) }()
	waitFor(t, "the execution to start", func() bool { return h.admit.status().InFlight == 1 })
	if _, st := get("s3cret"); st.InFlight != 1 || st.IdleSeconds != 0 {
		t.Fatalf("busy status = %+v", st)
	}
	close(exec.release)
	(<-done).Body.Close()
}

func TestDisconnectedBeforeStartIsDropped(t *testing.T) {
	exec := &fakeExecutor{}
	h := NewHandler("s3cret", exec, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := &Request{ID: "gone", Language: "python", Source: "x", Tests: []Test{{ID: "t"}}}
	if resp, err := h.run(ctx, req); !errors.Is(err, context.Canceled) || resp != nil {
		t.Fatalf("resp %+v err %v", resp, err)
	}
	if exec.calls.Load() != 0 {
		t.Fatal("executed for a disconnected client")
	}
	if _, pending := h.cache.get("gone"); pending {
		t.Fatal("dropped request left a cache entry")
	}
	if _, pending := h.inflight["gone"]; pending {
		t.Fatal("dropped request left an in-flight entry")
	}
}

// The cache is bounded by bytes as well as entries: big results push the
// oldest out, and an evicted entry is gone for good.
func TestResultCacheBoundedByBytes(t *testing.T) {
	big := func(id string, n int) *Response {
		return &Response{ID: id, Status: StatusOK, Results: []TestResult{{TestID: "t", Status: TestPass, StdoutTail: strings.Repeat("x", n)}}}
	}
	unit := responseSize(big("a", 1000))
	c := newResultCache(0, 3*unit, time.Hour, time.Minute)
	for _, id := range []string{"a", "b", "c"} {
		c.put(id, big(id, 1000))
	}
	if c.len() != 3 || c.bytes() != 3*unit {
		t.Fatalf("three entries within budget: len %d bytes %d, want 3 and %d", c.len(), c.bytes(), 3*unit)
	}
	c.put("d", big("d", 1000))
	if _, ok := c.get("a"); ok {
		t.Fatal("oldest entry survived a byte-bound eviction")
	}
	for _, id := range []string{"b", "c", "d"} {
		if _, ok := c.get(id); !ok {
			t.Fatalf("entry %s evicted although it fits", id)
		}
	}
	if c.bytes() > 3*unit {
		t.Fatalf("accounted bytes %d exceed the %d budget", c.bytes(), 3*unit)
	}
	// One response larger than the budget still stays: a retry must find it.
	c.put("huge", big("huge", 100000))
	if _, ok := c.get("huge"); !ok || c.len() != 1 {
		t.Fatalf("oversized entry: present=%v len=%d, want present alone", ok, c.len())
	}
	// A replaced id is charged once.
	c.put("huge", big("huge", 10))
	if c.bytes() != responseSize(big("huge", 10)) {
		t.Fatalf("replacing an entry left %d bytes accounted", c.bytes())
	}
}

// A sweep that empties the cache releases the map too, so an idle runner
// holds nothing on behalf of results nobody will ask for again.
func TestResultCacheSweepReleasesEverything(t *testing.T) {
	now := time.Unix(0, 0)
	c := newResultCache(100, 1<<20, time.Hour, time.Minute)
	c.now = func() time.Time { return now }
	for i := 0; i < 50; i++ {
		c.put(fmt.Sprint(i), &Response{ID: fmt.Sprint(i), Status: StatusOK})
	}
	before := c.entries
	now = now.Add(2 * time.Hour)
	c.sweep()
	if c.len() != 0 || c.bytes() != 0 {
		t.Fatalf("after sweep: len %d bytes %d", c.len(), c.bytes())
	}
	if reflect.ValueOf(c.entries).Pointer() == reflect.ValueOf(before).Pointer() {
		t.Fatal("sweep kept the old map; its buckets are never released")
	}
	if len(before) != 0 {
		t.Fatalf("old map still holds %d entries", len(before))
	}
}

func TestResponseSizeCountsEveryString(t *testing.T) {
	small := responseSize(&Response{ID: "a", Status: StatusOK})
	withText := responseSize(&Response{ID: "a", Status: StatusOK, CompileOutput: strings.Repeat("c", 500), Results: []TestResult{{StdoutTail: strings.Repeat("o", 300), StderrTail: strings.Repeat("e", 200)}}})
	if withText-small < 1000 {
		t.Fatalf("size grew by %d for 1000 bytes of text", withText-small)
	}
}

// Docker's streams are bounded: a flood on stdout is a runner error rather
// than a candidate verdict, and stderr keeps only a tail.
func TestBoundedBufferKeepsHeadOrTail(t *testing.T) {
	head := &boundedBuffer{limit: 8}
	_, _ = head.Write([]byte("0123456789"))
	_, _ = head.Write([]byte("abc"))
	if head.String() != "01234567" || !head.dropped {
		t.Errorf("head = %q dropped=%v", head.String(), head.dropped)
	}
	tail := &boundedBuffer{limit: 4, keepTail: true}
	_, _ = tail.Write([]byte("ab"))
	_, _ = tail.Write([]byte("cdef"))
	if tail.String() != "cdef" || !tail.dropped {
		t.Errorf("tail = %q dropped=%v", tail.String(), tail.dropped)
	}
	fits := &boundedBuffer{limit: 4}
	_, _ = fits.Write([]byte("abcd"))
	if fits.dropped {
		t.Error("an exact fit drops nothing")
	}
}

func TestDockerExecutorFloodOnStdoutIsARunnerError(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "docker")
	// Prints a plausible result after more than the cap of noise, so the
	// verdict must come from the cap, not from the JSON.
	script := "#!/bin/sh\nhead -c " + strconv.Itoa(stdoutCap+1) + " /dev/zero | tr '\\0' 'x'\necho '{\"status\":\"ok\",\"results\":[]}'\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	d := &DockerExecutor{Runtime: "runc", ImagePrefix: "x-", Docker: fake}
	resp := d.Execute(context.Background(), &Request{ID: "flood", Language: "python", Source: "x", Tests: []Test{{ID: "t"}}})
	if resp.Status != StatusError || !strings.Contains(resp.CompileOutput, "exceeded") {
		t.Fatalf("flooded stdout: %+v", resp)
	}
	if len(resp.Results) != 1 || resp.Results[0].Status != TestError || resp.Results[0].StderrTail != "runner error" {
		t.Fatalf("flooded stdout blamed the candidate: %+v", resp.Results)
	}
}

func TestDockerExecutorStderrIsATail(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "docker")
	script := "#!/bin/sh\ni=0; while [ $i -lt 2000 ]; do echo \"noise $i\" >&2; i=$((i+1)); done; echo END >&2; exit 1\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	d := &DockerExecutor{Runtime: "runc", ImagePrefix: "x-", Docker: fake}
	resp := d.Execute(context.Background(), &Request{ID: "noisy", Language: "python", Source: "x", Tests: []Test{{ID: "t"}}})
	if resp.Status != StatusError || !strings.HasSuffix(strings.TrimSpace(resp.CompileOutput), "END") {
		t.Fatalf("noisy stderr: status %q tail %q", resp.Status, resp.CompileOutput[max(0, len(resp.CompileOutput)-60):])
	}
	if len(resp.CompileOutput) > stderrTailCap+256 {
		t.Fatalf("compile output carries %d bytes of docker stderr, want a %d tail", len(resp.CompileOutput), stderrTailCap)
	}
}

func TestContainerMemoryCarriesHeadroom(t *testing.T) {
	if got := containerMemoryMB(256); got != 256+memoryHeadroomMB {
		t.Fatalf("containerMemoryMB(256) = %d", got)
	}
}

// A SQL result set is judged whole through its hash but carried clipped,
// like a program's stdout.
func TestJudgeRowsClipsTheTailButHashesEverything(t *testing.T) {
	lines := make([]string, 0, 2000)
	for i := 0; i < 2000; i++ {
		lines = append(lines, fmt.Sprintf("%d\trow number %d", i, i))
	}
	res := judgeRows(TestResult{TestID: "t"}, lines, Test{ID: "t", Expected: strings.Join(lines, "\n")})
	if res.Status != TestPass {
		t.Fatalf("status %q, want pass", res.Status)
	}
	if len(res.StdoutTail) > outputTailBytes+32 || !strings.HasSuffix(res.StdoutTail, "… truncated") {
		t.Fatalf("stdout tail is %d bytes and ends %q", len(res.StdoutTail), res.StdoutTail[len(res.StdoutTail)-20:])
	}
	full := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	if res.StdoutHash != hex.EncodeToString(full[:]) {
		t.Fatal("hash must cover the whole result set")
	}
	short := judgeRows(TestResult{TestID: "t"}, []string{"a", "b"}, Test{ID: "t", Expected: "b\na", Unordered: true})
	if short.Status != TestPass || short.StdoutTail != "a\nb" {
		t.Fatalf("short result = %+v", short)
	}
}

func TestConfigFromEnvQueueDefaultsAndOverrides(t *testing.T) {
	for _, k := range []string{"RUNNER_MAX_CONCURRENT", "RUNNER_MAX_QUEUE", "RUNNER_QUEUE_WAIT", "RUNNER_IDLE_EXIT", "RUNNER_CACHE_MB"} {
		t.Setenv(k, "")
	}
	cfg := ConfigFromEnv("s")
	if cfg.MaxConcurrent != DefaultMaxConcurrent || cfg.MaxQueue != DefaultMaxConcurrent*DefaultQueueFactor || cfg.QueueWait != DefaultQueueWait || cfg.IdleExit != 0 || cfg.CacheMB != DefaultCacheMB {
		t.Fatalf("defaults = %+v", cfg)
	}
	t.Setenv("RUNNER_MAX_CONCURRENT", "3")
	t.Setenv("RUNNER_MAX_QUEUE", "7")
	t.Setenv("RUNNER_QUEUE_WAIT", "15s")
	t.Setenv("RUNNER_IDLE_EXIT", "10m")
	t.Setenv("RUNNER_CACHE_MB", "8")
	cfg = ConfigFromEnv("s")
	if cfg.MaxConcurrent != 3 || cfg.MaxQueue != 7 || cfg.QueueWait != 15*time.Second || cfg.IdleExit != 10*time.Minute || cfg.CacheMB != 8 {
		t.Fatalf("overrides = %+v", cfg)
	}
	t.Setenv("RUNNER_MAX_QUEUE", "")
	if cfg = ConfigFromEnv("s"); cfg.MaxQueue != 3*DefaultQueueFactor {
		t.Fatalf("queue default follows concurrency: %+v", cfg)
	}
	if a := newAdmission(1, 1, time.Hour); a.wait != MaxQueueWait {
		t.Fatalf("an hour's wait was not clamped to %s: %s", MaxQueueWait, a.wait)
	}
}

func TestContainerNameIsCollisionFree(t *testing.T) {
	a, b := containerName("abc/DEF"), containerName("abc-def")
	if a == b || !strings.HasPrefix(a, "runner-") || len(a) != len("runner-")+32 {
		t.Fatalf("names %q %q", a, b)
	}
	for _, r := range a[len("runner-"):] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			t.Fatalf("non-hex name %q", a)
		}
	}
}
