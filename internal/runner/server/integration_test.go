//go:build integration

package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// The escape tests run under whatever runtime resolveRuntime picks; on a host
// without gVisor they run under runc via RUNNER_ALLOW_INSECURE_RUNTIME.

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func dockerExecutor(t *testing.T) *DockerExecutor {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not on PATH; skipping runner integration tests")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon unreachable; skipping runner integration tests")
	}
	available, err := availableRuntimes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rt, warn, err := resolveRuntime("runsc", true, available)
	if err != nil {
		t.Fatal(err)
	}
	if warn {
		t.Logf("gVisor (runsc) not installed: hardening tests run under %s", rt)
	}
	d := &DockerExecutor{Runtime: rt, ImagePrefix: "recruiting-runner-"}
	for _, lang := range []string{"python", "node", "go", "java"} {
		if err := exec.Command("docker", "image", "inspect", d.ImagePrefix+lang).Run(); err != nil {
			t.Logf("building missing image %s%s", d.ImagePrefix, lang)
			cmd := exec.Command(filepath.Join(repoRoot(t), "runner", "images", "build.sh"), lang)
			cmd.Dir = repoRoot(t)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("build image %s: %v\n%s", lang, err, out)
			}
		}
	}
	return d
}

func TestGVisorRuntimeSelected(t *testing.T) {
	available, err := availableRuntimes(context.Background())
	if err != nil {
		t.Skip("docker unavailable")
	}
	if _, _, err := resolveRuntime("runsc", false, available); err != nil {
		t.Skipf("gVisor (runsc) not installed on this host; runtime assertion skipped: %v", err)
	}
	d := dockerExecutor(t)
	if d.Runtime != "runsc" {
		t.Fatalf("runtime %q, want runsc", d.Runtime)
	}
	args := d.runArgs("x", "python", DefaultLimits)
	if !strings.Contains(strings.Join(args, " "), "--runtime=runsc") {
		t.Fatalf("runsc not passed: %v", args)
	}
}

func run(t *testing.T, d Executor, lang, source string, tests []Test, limits Limits) *Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	return d.Execute(ctx, &Request{ID: uuid.NewString(), Language: lang, Source: source, Tests: tests, Limits: limits})
}

func one(input, expected string) []Test {
	return []Test{{ID: uuid.NewString(), Input: input, Expected: expected, Weight: 1}}
}

var hello = map[string]string{
	"python": "import sys\nprint(sys.stdin.read().strip() + ' world')\n",
	"node":   "const s=require('fs').readFileSync(0,'utf8');console.log(s.trim()+' world');\n",
	"go":     "package main\nimport (\"bufio\";\"fmt\";\"os\")\nfunc main(){s,_:=bufio.NewReader(os.Stdin).ReadString('\\n');fmt.Println(strings(s)+\" world\")}\nfunc strings(s string) string { for len(s)>0 && (s[len(s)-1]=='\\n'||s[len(s)-1]==' ') { s=s[:len(s)-1] }; return s }\n",
	"java":   "import java.util.*;public class Main{public static void main(String[] a){Scanner s=new Scanner(System.in);System.out.println(s.nextLine().trim()+\" world\");}}\n",
}

func TestHelloWorldAndFailingTestPerLanguage(t *testing.T) {
	d := dockerExecutor(t)
	for lang, src := range hello {
		t.Run(lang, func(t *testing.T) {
			tests := []Test{
				{ID: uuid.NewString(), Input: "hello\n", Expected: "hello world", Weight: 1},
				{ID: uuid.NewString(), Input: "bye\n", Expected: "not this", Weight: 1},
			}
			resp := run(t, d, lang, src, tests, DefaultLimits)
			if resp.Status != StatusOK {
				t.Fatalf("status %s: compile=%q results=%+v", resp.Status, resp.CompileOutput, resp.Results)
			}
			if len(resp.Results) != 2 || resp.Results[0].Status != TestPass || resp.Results[1].Status != TestFail {
				t.Fatalf("results %+v", resp.Results)
			}
			if resp.Results[0].TestID != tests[0].ID || resp.Results[0].StdoutHash != sha("hello world") {
				t.Fatalf("result[0] %+v", resp.Results[0])
			}
			if resp.Results[0].TimeMs < 0 || resp.Results[0].MemKB <= 0 {
				t.Fatalf("no accounting: %+v", resp.Results[0])
			}
		})
	}
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func TestCompileErrorReported(t *testing.T) {
	d := dockerExecutor(t)
	resp := run(t, d, "go", "package main\nfunc main() { undefined() }\n", one("", ""), DefaultLimits)
	if resp.Status != StatusCompileError || !strings.Contains(resp.CompileOutput, "undefined") {
		t.Fatalf("status %s output %q", resp.Status, resp.CompileOutput)
	}
}

func TestEscapeNetworkDenied(t *testing.T) {
	d := dockerExecutor(t)
	src := `import socket
try:
    s=socket.create_connection(("1.1.1.1",53),timeout=2); print("connected")
except OSError as e:
    print("denied")
try:
    socket.getaddrinfo("example.com",80); print("resolved")
except OSError:
    print("no-dns")
`
	resp := run(t, d, "python", src, one("", "denied\nno-dns"), DefaultLimits)
	if resp.Status != StatusOK || resp.Results[0].Status != TestPass {
		t.Fatalf("network reachable: %+v %s", resp.Results, resp.Results[0].StderrTail)
	}
}

func TestEscapeWriteOutsideTmpFails(t *testing.T) {
	d := dockerExecutor(t)
	src := `import os
out=[]
for p in ["/etc/pwned","/usr/pwned","/pwned","/home/pwned","/app/pwned"]:
    try:
        open(p,"w").write("x"); out.append("wrote")
    except OSError:
        out.append("ro")
open("/tmp/ok","w").write("x"); out.append("tmp-ok")
print(" ".join(out))
`
	resp := run(t, d, "python", src, one("", "ro ro ro ro ro tmp-ok"), DefaultLimits)
	if resp.Status != StatusOK || resp.Results[0].Status != TestPass {
		t.Fatalf("rootfs writable: %+v", resp.Results)
	}
}

func TestEscapeForkBombCapped(t *testing.T) {
	d := dockerExecutor(t)
	src := `import os, time
n=0
while True:
    try:
        if os.fork()==0:
            while True: time.sleep(60)
        n+=1
    except BlockingIOError:
        print("capped", n < 32); break
print("done")
`
	limits := DefaultLimits
	limits.PIDs = 32
	limits.WallMs = 3000
	resp := run(t, d, "python", src, one("", "capped True\ndone"), limits)
	if resp.Status == StatusError {
		t.Fatalf("runner errored: %+v", resp)
	}
	// Either the fork limit is hit and reported, or the bomb spins until the
	// wall-clock kill; both prove the cap held and the container was reaped.
	st := resp.Results[0].Status
	if st != TestPass && st != TestTimeout && st != TestError {
		t.Fatalf("unexpected %+v", resp.Results[0])
	}
	if out, _ := exec.Command("docker", "ps", "-q", "--filter", "name=runner-").Output(); strings.TrimSpace(string(out)) != "" {
		t.Fatalf("containers left running: %s", out)
	}
}

func TestEscapeMemoryOverrunKilled(t *testing.T) {
	d := dockerExecutor(t)
	limits := DefaultLimits
	limits.MemMB = 64
	resp := run(t, d, "python", "a=bytearray(512*1024*1024)\nprint('survived')\n", one("", "survived"), limits)
	if resp.Status == StatusError {
		t.Fatalf("runner errored: %+v", resp)
	}
	if r := resp.Results[0]; r.Status == TestPass {
		t.Fatalf("memory limit not enforced: %+v", r)
	}
}

func TestEscapeWallTimeKilled(t *testing.T) {
	d := dockerExecutor(t)
	limits := DefaultLimits
	limits.WallMs = 1000
	start := time.Now()
	resp := run(t, d, "python", "while True: pass\n", one("", ""), limits)
	if resp.Status != StatusTimeout && resp.Results[0].Status != TestTimeout {
		t.Fatalf("not timed out: %+v", resp)
	}
	if time.Since(start) > 30*time.Second {
		t.Fatalf("wall kill took %s", time.Since(start))
	}
}

func TestOutputCapped(t *testing.T) {
	d := dockerExecutor(t)
	limits := DefaultLimits
	limits.OutputKB = 1
	src := "import sys\nsys.stdout.write('a'*(2*1024*1024))\n"
	resp := run(t, d, "python", src, one("", strings.Repeat("a", 1024)), limits)
	if resp.Status != StatusOK {
		t.Fatalf("%+v", resp)
	}
	if r := resp.Results[0]; r.Status != TestPass || r.StdoutHash != sha(strings.Repeat("a", 1024)) {
		t.Fatalf("output not capped at 1 KB: %+v", r)
	}
}

func TestSQLExecutionAndTimeout(t *testing.T) {
	dsn := os.Getenv("RUNNER_SQL_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("RUNNER_SQL_URL/DATABASE_URL not set")
	}
	s := &SQLExecutor{AdminURL: dsn}
	schema := "create table t(id int, name text); insert into t values (2,'b'),(1,'a');"
	tests := []Test{
		{ID: uuid.NewString(), Expected: "1\ta\n2\tb", Weight: 1},
		{ID: uuid.NewString(), Expected: "2\tb\n1\ta", Weight: 1},
		{ID: uuid.NewString(), Expected: "2\tb\n1\ta", Weight: 1, Unordered: true},
		{ID: uuid.NewString(), Input: "insert into t values (3,'c');", Expected: "1\ta\n2\tb\n3\tc", Weight: 1},
	}
	resp := s.Execute(context.Background(), &Request{ID: uuid.NewString(), Language: "sql", Source: "select id, name from t order by id", SQLSchema: schema, Tests: tests, Limits: DefaultLimits})
	if resp.Status != StatusOK {
		t.Fatalf("%+v", resp)
	}
	want := []string{TestPass, TestFail, TestPass, TestPass}
	for i, w := range want {
		if resp.Results[i].Status != w {
			t.Fatalf("test %d: %+v want %s", i, resp.Results[i], w)
		}
	}

	limits := DefaultLimits
	limits.WallMs = 500
	resp = s.Execute(context.Background(), &Request{ID: uuid.NewString(), Language: "sql", Source: "select pg_sleep(5)", Tests: one("", ""), Limits: limits})
	if resp.Results[0].Status != TestTimeout {
		t.Fatalf("statement_timeout not enforced: %+v", resp)
	}
	resp = s.Execute(context.Background(), &Request{ID: uuid.NewString(), Language: "sql", Source: "select * from nowhere", Tests: one("", ""), Limits: limits})
	if resp.Status != StatusRuntimeError && resp.Results[0].Status != TestError {
		t.Fatalf("bad query not reported: %+v", resp)
	}
}

func TestServerEndToEndRepeatedID(t *testing.T) {
	d := dockerExecutor(t)
	h := NewHandler("s", d, nil)
	srv := httptest.NewServer(h)
	defer srv.Close()
	id := uuid.NewString()
	payload := fmt.Sprintf(`{"id":%q,"language":"python","source":"print(42)","tests":[{"id":%q,"input":"","expected":"42","weight":1}],"limits":{"cpu_ms":2000,"wall_ms":5000,"mem_mb":256,"output_kb":64,"pids":64}}`, id, uuid.NewString())
	var prev string
	for i := 0; i < 2; i++ {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/execute", strings.NewReader(payload))
		req.Header.Set("Authorization", "Bearer s")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var out Response
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if out.Status != StatusOK || out.Results[0].Status != TestPass {
			t.Fatalf("%+v", out)
		}
		b, _ := json.Marshal(out)
		if i == 1 && string(b) != prev {
			t.Fatalf("repeated id returned different result:\n%s\n%s", prev, b)
		}
		prev = string(b)
	}
}
