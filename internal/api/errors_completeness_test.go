package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// unmappedSentinels are the exported error values the API never answers with,
// each with the reason it cannot reach problemDetail. Anything else declared
// in internal/domain or internal/service must appear in statusErrors, so a
// new sentinel cannot quietly become a 500.
var unmappedSentinels = map[string]string{
	"service.ErrInvalidCredentials": "password login is an HTML surface; the API has no login operation",
	"service.ErrNoSession":          "an unresolvable credential is the middleware's 401, not an operation's answer",
	"service.ErrResetInvalid":       "password reset is an HTML surface",
	"service.ErrOrgIncomplete":      "org bootstrap is the admin CLI, which has no API operation",
	"domain.ErrExtractTooBig":       "resume text extraction runs in the worker, never in a request",
}

// declaredSentinels is every exported Err* declared under dir, qualified by
// the package name the API refers to it as.
func declaredSentinels(t *testing.T, dir, pkg string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	err := filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.VAR {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, name := range vs.Names {
					if strings.HasPrefix(name.Name, "Err") && ast.IsExported(name.Name) {
						out[pkg+"."+name.Name] = true
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	if len(out) == 0 {
		t.Fatalf("%s declares no exported error sentinels; the walk is looking in the wrong place", dir)
	}
	return out
}

// mappedSentinels is every qualified sentinel statusErrors names.
func mappedSentinels(t *testing.T) map[string]bool {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "errors.go", nil, 0)
	if err != nil {
		t.Fatalf("parse errors.go: %v", err)
	}
	out := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || !strings.HasPrefix(sel.Sel.Name, "Err") {
			return true
		}
		out[pkg.Name+"."+sel.Sel.Name] = true
		return true
	})
	return out
}

func TestEveryDomainAndServiceSentinelIsMapped(t *testing.T) {
	declared := declaredSentinels(t, "../domain", "domain")
	for name := range declaredSentinels(t, "../service", "service") {
		declared[name] = true
	}
	mapped := mappedSentinels(t)

	for name := range declared {
		if mapped[name] {
			continue
		}
		if _, exempt := unmappedSentinels[name]; exempt {
			continue
		}
		t.Errorf("%s is not in statusErrors and is not exempt; it would answer 500", name)
	}
	for name := range unmappedSentinels {
		if !declared[name] {
			t.Errorf("%s is exempt but no longer declared; drop the exemption", name)
		}
		if mapped[name] {
			t.Errorf("%s is both mapped and exempt; drop the exemption", name)
		}
	}
}

// TestNoSentinelIsMappedTwice keeps the status lookup deterministic: it walks
// the groups in map order, so a sentinel in two of them would flap.
func TestNoSentinelIsMappedTwice(t *testing.T) {
	seen := map[error]int{}
	for status, sentinels := range statusErrors {
		for _, s := range sentinels {
			if prev, ok := seen[s]; ok {
				t.Errorf("%v is mapped to both %d and %d", s, prev, status)
			}
			seen[s] = status
		}
	}
}
