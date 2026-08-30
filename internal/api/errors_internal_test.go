package api

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"recruiting/internal/domain"
	"recruiting/internal/service"
)

func TestProblemDetailMapsDomainErrors(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{service.ErrForbidden, http.StatusForbidden},
		{service.ErrPlatformProblem, http.StatusForbidden},
		{service.ErrNotAuthor, http.StatusForbidden},
		{service.ErrBlind, http.StatusForbidden},
		{domain.ErrForbiddenMove, http.StatusForbidden},

		{service.ErrNotFound, http.StatusNotFound},
		{service.ErrLinkInvalid, http.StatusNotFound},

		{service.ErrStale, http.StatusConflict},
		{service.ErrSlotTaken, http.StatusConflict},
		{service.ErrReviewFiled, http.StatusConflict},
		{service.ErrEmailTaken, http.StatusConflict},
		{service.ErrSlugTaken, http.StatusConflict},
		{service.ErrAlreadyApplied, http.StatusConflict},
		{service.ErrPoolAlreadyOnJob, http.StatusConflict},
		{service.ErrAssessmentInUse, http.StatusConflict},
		{service.ErrStageOccupied, http.StatusConflict},

		{service.ErrTitleRequired, http.StatusUnprocessableEntity},
		{service.ErrInvalidJob, http.StatusUnprocessableEntity},
		{service.ErrBadVerdict, http.StatusUnprocessableEntity},
		{service.ErrBadScore, http.StatusUnprocessableEntity},
		{service.ErrBadTimezone, http.StatusUnprocessableEntity},
		{service.ErrNotActive, http.StatusUnprocessableEntity},
		{domain.ErrPrereqMissing, http.StatusUnprocessableEntity},
		{domain.ErrReasonRequired, http.StatusUnprocessableEntity},

		{errors.New("boom"), http.StatusInternalServerError},
	}
	for _, c := range cases {
		got := problemDetail(c.err)
		var se huma.StatusError
		if !errors.As(got, &se) {
			t.Fatalf("%v: mapped to %T, want a huma status error", c.err, got)
		}
		if se.GetStatus() != c.want {
			t.Errorf("%v: status = %d, want %d", c.err, se.GetStatus(), c.want)
		}
	}
}

// TestProblemDetailUnwrapsWrappedErrors keeps the mapping working when a
// service returns its sentinel wrapped in context.
func TestProblemDetailUnwrapsWrappedErrors(t *testing.T) {
	err := fmt.Errorf("move application: %w", service.ErrStale)
	var se huma.StatusError
	if !errors.As(problemDetail(err), &se) || se.GetStatus() != http.StatusConflict {
		t.Fatalf("wrapped ErrStale did not map to 409")
	}
}

// TestProblemDetailHidesInternalDetail keeps a database or programming fault
// from reaching the caller as text.
func TestProblemDetailHidesInternalDetail(t *testing.T) {
	err := errors.New("pq: relation \"secret\" does not exist")
	got := problemDetail(err)
	if got.Error() == err.Error() {
		t.Fatalf("internal error text leaked: %v", got)
	}
}
