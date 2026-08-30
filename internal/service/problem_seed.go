package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"

	"recruiting/internal/domain"
	seed "recruiting/seed/problems"
)

// SeedProblems parses every embedded seed document into one batch. Files are
// read in name order so the bank loads the same way every time, and the whole
// set is validated together — a title may not repeat across files either.
func SeedProblems() ([]domain.ImportProblem, error) {
	names, err := fs.Glob(seed.FS, "*.json")
	if err != nil {
		return nil, fmt.Errorf("seed problems: %w", err)
	}
	if len(names) == 0 {
		return nil, errors.New("seed problems: no documents are embedded")
	}
	sort.Strings(names)
	var all []json.RawMessage
	for _, name := range names {
		data, err := fs.ReadFile(seed.FS, name)
		if err != nil {
			return nil, fmt.Errorf("seed problems: %s: %w", name, err)
		}
		var one []json.RawMessage
		if err := json.Unmarshal(data, &one); err != nil {
			return nil, fmt.Errorf("seed problems: %s: %w", name, err)
		}
		all = append(all, one...)
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
