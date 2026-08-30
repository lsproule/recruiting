package admin

import (
	"strconv"

	"recruiting/internal/service"
)

func hasRole(roles []string, role string) bool {
	for _, r := range roles {
		if r == role {
			return true
		}
	}
	return false
}

// weight renders a signal weight without a trailing ".0" so the form shows
// what an admin typed.
func weight(s service.Settings, name string) string {
	return strconv.FormatFloat(s.IntegrityWeights[name], 'f', -1, 64)
}
