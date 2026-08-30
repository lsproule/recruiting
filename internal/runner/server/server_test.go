package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
		" --security-opt=no-new-privileges ", " --pids-limit=16 ", " --memory=64m ", " --memory-swap=64m ",
		" --cpus=", " --user=65534:65534 ", " --name=c1 ", " --rm ", " recruiting-runner-python ",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %q", want, joined)
		}
	}
}

func TestResultCacheBoundedAndExpiring(t *testing.T) {
	now := time.Unix(0, 0)
	c := newResultCache(2, time.Hour, time.Minute)
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

func TestExecuteReturns503WhenSaturated(t *testing.T) {
	exec := &blockingExecutor{release: make(chan struct{})}
	h := NewHandler("s3cret", exec, nil)
	h.sem = make(chan struct{}, 1)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	first := strings.Replace(body, "11111111-1111-1111-1111-111111111111", "first", 1)
	done := make(chan *http.Response, 1)
	go func() { done <- post(t, srv, "s3cret", first) }()
	time.Sleep(100 * time.Millisecond)
	resp := post(t, srv, "s3cret", body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("status %d retry-after %q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	close(exec.release)
	(<-done).Body.Close()
	// A repeated id for a finished run is served from cache even when saturated.
	h.sem <- struct{}{}
	resp = post(t, srv, "s3cret", first)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cached result blocked by saturation: %d", resp.StatusCode)
	}
}

func TestDisconnectedBeforeStartIsDropped(t *testing.T) {
	exec := &fakeExecutor{}
	h := NewHandler("s3cret", exec, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := &Request{ID: "gone", Language: "python", Source: "x", Tests: []Test{{ID: "t"}}}
	if resp, code := h.run(ctx, req); code != 0 || resp.Status != StatusError {
		t.Fatalf("resp %+v code %d", resp, code)
	}
	if exec.calls.Load() != 0 {
		t.Fatal("executed for a disconnected client")
	}
	if _, pending := h.cache.get("gone"); pending {
		t.Fatal("dropped request left a cache entry")
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
