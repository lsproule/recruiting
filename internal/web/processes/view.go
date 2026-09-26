package processes

import (
	"strconv"

	"recruiting/internal/domain"
	"recruiting/internal/service"
)

// listView is the library screen: the org's processes, the built-in ones,
// and what a refused create form carried.
type listView struct {
	Processes []service.Process
	Library   []domain.ProcessSpec
	CanEdit   bool
	Draft     service.ProcessInput
}

func itoa(n int) string { return strconv.Itoa(n) }

func jobsLabel(n int) string {
	switch n {
	case 0:
		return "no jobs built from it yet"
	case 1:
		return "1 job built from it"
	}
	return itoa(n) + " jobs built from it"
}

func libraryName(key string) string {
	if spec, ok := domain.ProcessByKey(key); ok {
		return spec.Name
	}
	return key
}
