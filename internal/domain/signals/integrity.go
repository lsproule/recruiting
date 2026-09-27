package signals

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/google/uuid"
)

// Thresholds for the session signals. Like the typing ones they are
// calibration constants, kept together so a session recording can move them.
const (
	// FullscreenExitsFull is the number of exits from fullscreen at which
	// the count alone saturates the signal.
	FullscreenExitsFull = 5
	// FullscreenOutsideFull is the total time outside fullscreen at which
	// the duration alone saturates the signal.
	FullscreenOutsideFull = 5 * time.Minute
)

// fullscreenExits is how far the sitting departed from the fullscreen it was
// asked to run in: the number of exits and the time spent outside, each
// normalised against the point where it saturates, whichever is higher. A
// session that never enforced fullscreen records neither and stays at 0.
func (c computation) fullscreenExits() Signal {
	s := Signal{Name: FullscreenExits}
	if c.stream.Count() == 0 {
		return s
	}
	var outside time.Duration
	// The stream closed an exit never returned from at its last event: the
	// candidate was outside for the rest of the recording.
	for _, exit := range c.stream.exits {
		outside += exit.Away
		s.Evidence = append(s.Evidence, Evidence{
			ProblemID: exit.ProblemID, At: at(exit.At), Seq: exit.Seq,
			Note:   fmt.Sprintf("left fullscreen for %s", exit.Away.Round(time.Second)),
			Values: map[string]float64{"seconds": exit.Away.Seconds()},
		})
	}
	exits := len(c.stream.exits)
	if exits == 0 {
		return s
	}
	s.Value = math.Max(
		float64(exits)/FullscreenExitsFull,
		outside.Seconds()/FullscreenOutsideFull.Seconds(),
	)
	return s
}

// snapshotGaps is the share of the webcam beats the session should have
// produced that no frame arrived for. Intervals are counted from the sitting
// the recording covers, so a candidate who blocks the camera for half the
// session is not hidden by having sent fewer beats than were due.
func (c computation) snapshotGaps() Signal {
	s := Signal{Name: SnapshotGaps}
	if c.in.WebcamEvery <= 0 {
		// The webcam was off: nothing was expected, so nothing is missing.
		return s
	}
	if c.stream.Count() == 0 {
		s.Confidence = ConfidenceLow
		return s
	}
	taken, reported := c.stream.taken, c.stream.reported
	start := c.in.StartedAt
	if start.IsZero() || c.stream.first.Before(start) {
		start = c.stream.first
	}
	covered := c.stream.last.Sub(start)
	expected := max(reported, int(covered/c.in.WebcamEvery))
	if expected == 0 {
		s.Confidence = ConfidenceLow
		return s
	}
	missed := min(max(expected-taken, 0), expected)
	if missed == 0 {
		return s
	}
	s.Value = float64(missed) / float64(expected)
	s.Evidence = append(s.Evidence, Evidence{
		Note: fmt.Sprintf("%d of %d webcam frames never arrived, one due every %s",
			missed, expected, c.in.WebcamEvery),
		Values: map[string]float64{"missed": float64(missed), "expected": float64(expected)},
	})
	return s
}

// bySeq is the stream in seq order, which is the order it was recorded in
// regardless of the order it was handed over in.
func bySeq(events []Event) []Event {
	out := make([]Event, len(events))
	copy(out, events)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// problemOf is the problem an event names, or the nil UUID for the
// session-wide events that name none.
func problemOf(ev Event) uuid.UUID {
	if ev.ProblemID == nil {
		return uuid.Nil
	}
	return *ev.ProblemID
}
