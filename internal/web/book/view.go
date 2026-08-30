package book

import (
	"encoding/json"
	"time"

	"recruiting/internal/domain"
)

// slotsJSON hands the picker the open slots as UTC instants; the browser lays
// them out in whichever zone the candidate chooses.
func slotsJSON(slots []domain.Slot) string {
	out := make([]string, 0, len(slots))
	for _, s := range slots {
		out = append(out, s.Start.UTC().Format(time.RFC3339))
	}
	b, _ := json.Marshal(out)
	return string(b)
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
