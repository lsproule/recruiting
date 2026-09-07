package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"recruiting/internal/domain"
	"recruiting/internal/service"
)

// problemDetail is the single mapping from a domain or service error to the
// RFC 9457 problem detail the API answers with. Huma renders the status and
// message it carries as application/problem+json.
//
// Anything unrecognised is a fault of ours, not the caller's: it becomes a
// 500 with a fixed message so a database or programming error never reaches
// the caller as text.
func problemDetail(err error) error {
	if err == nil {
		return nil
	}
	msg := strings.TrimPrefix(err.Error(), "service: ")
	if status, ok := errorStatus(err); ok {
		return huma.NewError(status, msg)
	}
	var importErrs domain.ProblemImportErrors
	if errors.As(err, &importErrs) {
		return huma.Error422UnprocessableEntity(importErrs.Error())
	}
	return huma.Error500InternalServerError("something went wrong")
}

// errorStatus is the status one sentinel answers with, and whether it is one
// the API recognises at all.
func errorStatus(err error) (int, bool) {
	for status, sentinels := range statusErrors {
		for _, s := range sentinels {
			if errors.Is(err, s) {
				return status, true
			}
		}
	}
	return 0, false
}

// statusErrors groups the sentinels by the answer they earn. A sentinel
// belongs to exactly one group.
var statusErrors = map[int][]error{
	http.StatusForbidden: {
		service.ErrForbidden,
		service.ErrPlatformProblem,
		service.ErrNotAuthor,
		service.ErrBlind,
		domain.ErrForbiddenMove,
	},
	http.StatusNotFound: {
		service.ErrNotFound,
		service.ErrLinkInvalid,
		service.ErrLinkPurpose,
		service.ErrProblemNotInSet,
		service.ErrNoBooking,
		service.ErrNoResume,
	},
	http.StatusConflict: {
		service.ErrStale,
		service.ErrSlotTaken,
		service.ErrTooLate,
		service.ErrReviewFiled,
		service.ErrEmailTaken,
		service.ErrSlugTaken,
		service.ErrProblemTitleTaken,
		service.ErrAlreadyApplied,
		service.ErrPoolAlreadyOnJob,
		service.ErrAssessmentInUse,
		service.ErrStageOccupied,
		service.ErrEventSeq,
		service.ErrLinkUsed,
		service.ErrIdentityRecorded,
		service.ErrPacketSent,
	},
	http.StatusGone: {
		service.ErrLinkExpired,
		service.ErrLinkRevoked,
		service.ErrInviteExpired,
		service.ErrAttemptExpired,
	},
	http.StatusTooManyRequests: {
		service.ErrSubmissionPending,
	},
	// The upload is well formed but larger than the record takes.
	http.StatusRequestEntityTooLarge: {
		domain.ErrResumeTooLarge,
		service.ErrSnapshotTooLarge,
	},
	// The bytes are not one of the resume formats, whatever the filename
	// claimed; the sniffer decides, so this is the media type, not the body.
	http.StatusUnsupportedMediaType: {
		domain.ErrResumeType,
		service.ErrSnapshotType,
	},
	// The deployment is missing a dependency the operation needs; the caller
	// can do nothing about it, and a retry may succeed.
	http.StatusServiceUnavailable: {
		service.ErrNoBlobStore,
		service.ErrNoExecutor,
	},
	http.StatusUnprocessableEntity: {
		service.ErrTooManyPicks,
		service.ErrDuplicatePick,
		service.ErrNoPicks,
		service.ErrPickRejected,
		service.ErrProblemQuality,
		service.ErrNoLanguage,
		service.ErrProblemInvalid,
		service.ErrTitleRequired,
		service.ErrInvalidJob,
		service.ErrNoStages,
		service.ErrNoTemplate,
		service.ErrInvalidRole,
		service.ErrNameRequired,
		service.ErrEmailRequired,
		service.ErrCompanyRequired,
		service.ErrInvalidSettings,
		service.ErrAssessmentInvalid,
		service.ErrStageNotAssessment,
		service.ErrIntakeInvalid,
		service.ErrTemplateNoAssessment,
		service.ErrBadVerdict,
		service.ErrNotScored,
		service.ErrBadScore,
		service.ErrBadOverall,
		service.ErrNotInterview,
		service.ErrBadTimezone,
		service.ErrBadRule,
		service.ErrBadOutcome,
		service.ErrBadException,
		service.ErrNoVetter,
		service.ErrNotVetter,
		service.ErrNotActive,
		service.ErrTooLong,
		service.ErrMessageRequired,
		service.ErrPasswordTooShort,
		service.ErrTokenName,
		service.ErrTokenExpired,
		service.ErrJobNotOpen,
		service.ErrAttemptNotStarted,
		service.ErrConsentRequired,
		service.ErrAttemptClosed,
		service.ErrLanguageNotAllowed,
		service.ErrEventKind,
		service.ErrEventInvalid,
		service.ErrSourceTooLarge,
		domain.ErrPrereqMissing,
		domain.ErrReasonRequired,
		domain.ErrTerminal,
		domain.ErrBadTimezone,
		domain.ErrInvalidPipeline,
		domain.ErrResumeEmpty,
	},
}
