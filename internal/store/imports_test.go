package store_test

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

const systemPkg = "recruiting/internal/store/system"

// Only the binary's admin and migrate modes may open an RLS-bypassing
// connection; every library package must stay tenant-scoped.
func TestSystemPackageImportedOnlyByCommand(t *testing.T) {
	cmd := exec.Command("go", "list", "-json=ImportPath,Imports,TestImports,XTestImports", "./...")
	cmd.Dir = repoRoot(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	dec := json.NewDecoder(strings.NewReader(string(out)))
	for dec.More() {
		var pkg struct {
			ImportPath   string
			Imports      []string
			TestImports  []string
			XTestImports []string
		}
		if err := dec.Decode(&pkg); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if pkg.ImportPath == systemPkg || strings.HasPrefix(pkg.ImportPath, "recruiting/cmd/recruiting") {
			continue
		}
		for _, imp := range append(append(pkg.Imports, pkg.TestImports...), pkg.XTestImports...) {
			if imp == systemPkg {
				t.Errorf("%s imports %s; only cmd/recruiting may", pkg.ImportPath, systemPkg)
			}
		}
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}").Output()
	if err != nil {
		t.Fatalf("go list -m: %v", err)
	}
	return strings.TrimSpace(string(out))
}
