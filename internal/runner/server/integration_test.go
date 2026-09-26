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
	"slices"
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
	// python carries the hardening tests, so build it when it is missing; every
	// other language skips instead (see requireImage).
	if err := exec.Command("docker", "image", "inspect", d.ImagePrefix+"python").Run(); err != nil {
		t.Logf("building missing image %spython", d.ImagePrefix)
		cmd := exec.Command(filepath.Join(repoRoot(t), "runner", "images", "build.sh"), "python")
		cmd.Dir = repoRoot(t)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build image python: %v\n%s", err, out)
		}
	}
	return d
}

// requireImage skips rather than fails when a language image has not been
// built: the full set is large and `make runner-images` is a separate step.
func requireImage(t *testing.T, d *DockerExecutor, lang string) {
	t.Helper()
	if err := exec.Command("docker", "image", "inspect", d.ImagePrefix+lang).Run(); err != nil {
		t.Skipf("image %s%s not built; run `RUNNER_LANGUAGES=%s make runner-images`", d.ImagePrefix, lang, lang)
	}
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

// helloWorlds echoes one stdin line back with " world" appended, once per code
// language in the registry (internal/domain/languages.go), in registry order.
var helloWorlds = []struct{ lang, source string }{
	{"python", "import sys\nprint(sys.stdin.readline().strip() + ' world')\n"},
	{"javascript", "const s=require('fs').readFileSync(0,'utf8');console.log(s.split('\\n')[0].trim()+' world');\n"},
	{"ruby", "puts \"#{$stdin.gets.strip} world\"\n"},
	{"php", "<?php\n$s = trim(fgets(STDIN));\necho $s . \" world\\n\";\n"},
	{"go", "package main\n\nimport (\n\t\"bufio\"\n\t\"fmt\"\n\t\"os\"\n\t\"strings\"\n)\n\nfunc main() {\n\tline, _ := bufio.NewReader(os.Stdin).ReadString('\\n')\n\tfmt.Println(strings.TrimSpace(line) + \" world\")\n}\n"},
	{"java", "import java.util.*;\npublic class Main{public static void main(String[] a){Scanner s=new Scanner(System.in);System.out.println(s.nextLine().trim()+\" world\");}}\n"},
	{"csharp", "class Program { static void Main() { System.Console.WriteLine(System.Console.ReadLine().Trim() + \" world\"); } }\n"},
	{"cpp", "#include <iostream>\n#include <string>\nint main(){std::string s;std::getline(std::cin,s);while(!s.empty()&&(s.back()=='\\r'||s.back()==' '))s.pop_back();std::cout<<s<<\" world\"<<std::endl;}\n"},
	{"c", "#include <stdio.h>\n#include <string.h>\nint main(void){char b[256];if(!fgets(b,sizeof b,stdin))return 1;b[strcspn(b,\"\\r\\n\")]=0;printf(\"%s world\\n\",b);return 0;}\n"},
	{"rust", "use std::io::BufRead;\nfn main(){let mut s=String::new();std::io::stdin().lock().read_line(&mut s).unwrap();println!(\"{} world\", s.trim());}\n"},
}

// The hello-world table, the images on disk, and build.sh's default set must
// name the same languages, or a language silently stops being exercised.
func TestEveryLanguageHasAnImageDefinition(t *testing.T) {
	root := repoRoot(t)
	var want []string
	for _, hw := range helloWorlds {
		want = append(want, hw.lang)
		if _, err := os.Stat(filepath.Join(root, "runner", "images", hw.lang, "Dockerfile")); err != nil {
			t.Errorf("no image for %s: %v", hw.lang, err)
		}
	}
	script, err := os.ReadFile(filepath.Join(root, "runner", "images", "build.sh"))
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(script), "\nall=(")
	if !ok {
		t.Fatal("build.sh has no all=() default set")
	}
	list, _, _ := strings.Cut(rest, ")")
	if got := strings.Fields(list); !slices.Equal(got, want) {
		t.Errorf("build.sh builds %v, hello-world table covers %v", got, want)
	}
}

func TestHelloWorldAndFailingTestPerLanguage(t *testing.T) {
	d := dockerExecutor(t)
	for _, hw := range helloWorlds {
		t.Run(hw.lang, func(t *testing.T) {
			requireImage(t, d, hw.lang)
			tests := []Test{
				{ID: uuid.NewString(), Input: "hello\n", Expected: "hello world", Weight: 1},
				{ID: uuid.NewString(), Input: "bye\n", Expected: "not this", Weight: 1},
			}
			resp := run(t, d, hw.lang, hw.source, tests, DefaultLimits)
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
	// want is a fragment of the toolchain's own diagnostic, so the candidate
	// sees the compiler's message and not a generic failure.
	cases := []struct{ lang, source, want string }{
		{"go", "package main\nfunc main() { undefined() }\n", "undefined"},
		{"c", "int main(void){ return nope(); }\n", "nope"},
		{"rust", "fn main(){ nope(); }\n", "nope"},
		{"java", "public class Main{public static void main(String[] a){nope();}}\n", "nope"},
	}
	for _, c := range cases {
		t.Run(c.lang, func(t *testing.T) {
			requireImage(t, d, c.lang)
			resp := run(t, d, c.lang, c.source, one("", ""), DefaultLimits)
			if resp.Status != StatusCompileError || !strings.Contains(resp.CompileOutput, c.want) {
				t.Fatalf("status %s output %q", resp.Status, resp.CompileOutput)
			}
		})
	}
}

// busyLoops spin forever without allocating, so only the wall clock can end
// them. The compiled languages are the ones where a stuck child could outlive
// the harness, so they are the ones asserted.
var busyLoops = []struct{ lang, source string }{
	{"c", "int main(void){ for(;;){} }\n"},
	{"rust", "fn main(){ loop { std::hint::spin_loop(); } }\n"},
}

func TestCompiledLanguageBusyLoopKilled(t *testing.T) {
	d := dockerExecutor(t)
	for _, bl := range busyLoops {
		t.Run(bl.lang, func(t *testing.T) {
			requireImage(t, d, bl.lang)
			limits := DefaultLimits
			limits.WallMs = 1000
			start := time.Now()
			resp := run(t, d, bl.lang, bl.source, one("", ""), limits)
			if resp.Status != StatusTimeout && resp.Results[0].Status != TestTimeout {
				t.Fatalf("not timed out: %+v", resp)
			}
			if time.Since(start) > 3*time.Minute {
				t.Fatalf("wall kill took %s", time.Since(start))
			}
		})
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
	// The request id is chosen here so the reaping check names this run's own
	// container: any other runner container on the host belongs to someone else.
	id := uuid.NewString()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	resp := d.Execute(ctx, &Request{
		ID:       id,
		Language: "python",
		Source:   src,
		Tests:    one("", "capped True\ndone"),
		Limits:   limits,
	})
	if resp.Status == StatusError {
		t.Fatalf("runner errored: %+v", resp)
	}
	// Either the fork limit is hit and reported, or the bomb spins until the
	// wall-clock kill; both prove the cap held and the container was reaped.
	st := resp.Results[0].Status
	if st != TestPass && st != TestTimeout && st != TestError {
		t.Fatalf("unexpected %+v", resp.Results[0])
	}
	name := containerName(id)
	if out, _ := exec.Command("docker", "ps", "-q", "--filter", "name="+name).Output(); strings.TrimSpace(string(out)) != "" {
		t.Fatalf("container %s left running: %s", name, out)
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
