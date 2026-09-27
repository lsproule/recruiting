package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"recruiting/internal/domain"
	seed "recruiting/seed/problems"
)

// SeedProblemDirs lists the bank's problem directories in load order: name
// order, so the bank loads the same way every time.
func SeedProblemDirs() ([]string, error) {
	dirs, err := fs.ReadDir(seed.FS, "_bank")
	if err != nil {
		return nil, fmt.Errorf("seed problems: %w", err)
	}
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if d.IsDir() {
			out = append(out, "_bank/"+d.Name())
		}
	}
	if len(out) == 0 {
		return nil, errors.New("seed problems: no problem directories are embedded")
	}
	return out, nil
}

// SeedProblems assembles the whole embedded seed bank into one import batch:
// each problem directory's problem.json with the solution files beside it
// attached as its reference solutions, validated the way an import is — a
// title may not repeat across directories either. The bank's perf cases make
// this many megabytes at once, so the seed load itself walks
// SeedProblemDirs and SeedProblem one directory at a time; this is for the
// tests and tools that want the bank whole.
func SeedProblems() ([]domain.ImportProblem, error) {
	dirs, err := SeedProblemDirs()
	if err != nil {
		return nil, err
	}
	out := make([]domain.ImportProblem, 0, len(dirs))
	var report domain.ProblemImportErrors
	seen := map[string]int{}
	for i, dir := range dirs {
		p, err := SeedProblem(dir)
		var faults domain.ProblemImportErrors
		switch {
		case errors.As(err, &faults):
			report = append(report, seedFault(faults, i))
			continue
		case err != nil:
			return nil, fmt.Errorf("seed problems: %s: %w", path.Base(dir), err)
		}
		if msg := seedDuplicate(seen, p.Title, i); msg != "" {
			report = append(report, domain.ProblemImportError{Index: i, Title: p.Title, Errors: []string{msg}})
			continue
		}
		out = append(out, p)
	}
	if len(report) > 0 {
		return nil, report
	}
	return out, nil
}

// SeedProblem reads one problem directory into a normalized, validated
// import entry. What is wrong with the problem itself comes back as a
// domain.ProblemImportErrors with index 0; a directory the bank cannot read
// at all is a plain error.
func SeedProblem(dir string) (domain.ImportProblem, error) {
	p, err := seedProblemJSON(dir + "/problem.json")
	if err != nil {
		return domain.ImportProblem{}, err
	}
	p.References, err = seedSolutions(dir)
	if err != nil {
		return domain.ImportProblem{}, err
	}
	p.Normalize()
	if errs := p.Validate(); len(errs) > 0 {
		return domain.ImportProblem{}, domain.ProblemImportErrors{{Index: 0, Title: p.Title, Errors: errs}}
	}
	return p, nil
}

// seedProblemJSON decodes a problem.json straight from the embedded file,
// with the import document's strictness: no unknown fields, nothing after
// the object, and no reference solutions inside it.
func seedProblemJSON(name string) (domain.ImportProblem, error) {
	f, err := seed.FS.Open(name)
	if err != nil {
		return domain.ImportProblem{}, err
	}
	defer f.Close()
	// The outer field shadows the embedded one, so the decoder lands any
	// reference_solutions here rather than on the problem, where the check
	// below sees it.
	var doc struct {
		domain.ImportProblem
		References json.RawMessage `json:"reference_solutions"`
	}
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return domain.ImportProblem{}, fmt.Errorf("problem.json: %w", err)
	}
	if dec.More() {
		return domain.ImportProblem{}, errors.New("problem.json carries more than one JSON value")
	}
	if len(doc.References) > 0 {
		return domain.ImportProblem{}, errors.New("problem.json carries reference_solutions; they belong in solution files beside it")
	}
	return doc.ImportProblem, nil
}

// seedSolutions reads the solution files beside a problem.json, in name
// order, as the problem's reference solutions.
func seedSolutions(dir string) ([]domain.ImportReference, error) {
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
	return refs, nil
}

// seedFault re-indexes one problem's faults to its place in the bank.
func seedFault(faults domain.ProblemImportErrors, index int) domain.ProblemImportError {
	out := domain.ProblemImportError{Index: index}
	for _, f := range faults {
		out.Title = f.Title
		out.Errors = append(out.Errors, f.Errors...)
	}
	return out
}

// seedDuplicate records a title and names the earlier problem when it was
// already taken.
func seedDuplicate(seen map[string]int, title string, index int) string {
	key := strings.ToLower(title)
	if first, dup := seen[key]; dup {
		return fmt.Sprintf("duplicate title, already used by problem %d", first+1)
	}
	seen[key] = index
	return ""
}

func seedSolutionNames() []string {
	out := make([]string, 0, len(seed.SolutionLanguages))
	for name := range seed.SolutionLanguages {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
