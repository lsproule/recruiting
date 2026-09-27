package queue

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/riverqueue/river"
)

// The worker adapter hands a Snooze to river as its own snooze, which puts
// the job back without charging an attempt; every other error passes
// through untouched so river retries it with backoff.
func TestMapSnoozeBecomesRiverJobSnooze(t *testing.T) {
	err := mapSnooze(fmt.Errorf("handler: %w", Snooze(45*time.Second)))
	var rs *river.JobSnoozeError
	if !errors.As(err, &rs) || rs.Duration != 45*time.Second {
		t.Fatalf("mapSnooze(Snooze(45s)) = %#v, want river.JobSnooze(45s)", err)
	}
	plain := errors.New("boom")
	if got := mapSnooze(plain); got != plain { //nolint:errorlint // identity is the point
		t.Errorf("an ordinary error must pass through unchanged, got %v", got)
	}
	if got := mapSnooze(nil); got != nil {
		t.Errorf("nil must stay nil, got %v", got)
	}
}
