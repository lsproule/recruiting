package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"recruiting/internal/domain"
	seed "recruiting/seed/problems"
)

// SeedProblems assembles the embedded seed bank into one import batch: each
// problem directory's problem.json with the solution files beside it attached
// as its reference solutions. Directories are read in name order so the bank
// loads the same way every time, and the whole set is validated together — a
// title may not repeat across directories either.
func SeedProblems() ([]domain.ImportProblem, error) {
	dirs, err := fs.ReadDir(seed.FS, "_bank")
	if err != nil {
		return nil, fmt.Errorf("seed problems: %w", err)
	}
	var all []json.RawMessage
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		raw, err := seedProblem("_bank/" + d.Name())
		if err != nil {
			return nil, fmt.Errorf("seed problems: %s: %w", d.Name(), err)
		}
		all = append(all, raw)
	}
	if len(all) == 0 {
		return nil, errors.New("seed problems: no problem directories are embedded")
	}
	doc, err := json.Marshal(all)
	if err != nil {
		return nil, fmt.Errorf("seed problems: %w", err)
	}
	problems, err := domain.ParseProblemImport(doc)
	if err != nil {
		return nil, fmt.Errorf("seed problems: %w", err)
	}
	return problems, nil
}

// seedProblem reads one problem directory into an import document entry.
func seedProblem(dir string) (json.RawMessage, error) {
	data, err := fs.ReadFile(seed.FS, dir+"/problem.json")
	if err != nil {
		return nil, err
	}
	var problem map[string]json.RawMessage
	if err := json.Unmarshal(data, &problem); err != nil {
		return nil, fmt.Errorf("problem.json: %w", err)
	}
	if _, has := problem["reference_solutions"]; has {
		return nil, errors.New("problem.json carries reference_solutions; they belong in solution files beside it")
	}
	entries, err := fs.ReadDir(seed.FS, dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	refs := make([]domain.ImportReference, 0, len(names))
	for _, name := range names {
		if name == "problem.json" || strings.HasPrefix(name, ".") {
			continue
		}
		lang, ok := seed.SolutionLanguages[name]
		if !ok {
			return nil, fmt.Errorf("%s is not a solution file the bank knows (one of %s)", name, strings.Join(seedSolutionNames(), ", "))
		}
		src, err := fs.ReadFile(seed.FS, dir+"/"+name)
		if err != nil {
			return nil, err
		}
		refs = append(refs, domain.ImportReference{Language: lang, Source: string(src)})
	}
	encoded, err := json.Marshal(refs)
	if err != nil {
		return nil, err
	}
	problem["reference_solutions"] = encoded
	return json.Marshal(problem)
}

func seedSolutionNames() []string {
	out := make([]string, 0, len(seed.SolutionLanguages))
	for name := range seed.SolutionLanguages {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
