package service

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func validInput() AssessmentInput {
	return AssessmentInput{Name: "Screen", DurationMinutes: 60, InviteWindowDays: 7, ProblemIDs: []uuid.UUID{uuid.New()}}
}

// The integrity block is a promise made to the candidate before they start,
// so a setting that cannot be honoured is refused at the form rather than
// half-applied in the session.
func TestAssessmentInputCleanChecksIntegritySettings(t *testing.T) {
	t.Run("webcam defaults its interval", func(t *testing.T) {
		in := validInput()
		in.Integrity = IntegritySettings{Webcam: true}
		got, err := in.clean()
		if err != nil {
			t.Fatalf("clean: %v", err)
		}
		if got.Integrity.WebcamEvery != DefaultWebcamInterval {
			t.Errorf("interval = %d, want the %d second default", got.Integrity.WebcamEvery, DefaultWebcamInterval)
		}
	})
	t.Run("interval bounds", func(t *testing.T) {
		for _, every := range []int{14, 601} {
			in := validInput()
			in.Integrity = IntegritySettings{Webcam: true, WebcamEvery: every}
			if _, err := in.clean(); !errors.Is(err, ErrAssessmentInvalid) {
				t.Errorf("interval %d: err = %v, want ErrAssessmentInvalid", every, err)
			}
		}
		for _, every := range []int{MinWebcamInterval, MaxWebcamInterval} {
			in := validInput()
			in.Integrity = IntegritySettings{Webcam: true, WebcamEvery: every}
			if _, err := in.clean(); err != nil {
				t.Errorf("interval %d: %v, want it accepted", every, err)
			}
		}
	})
	t.Run("photo id needs the webcam", func(t *testing.T) {
		in := validInput()
		in.Integrity = IntegritySettings{PhotoID: true}
		if _, err := in.clean(); !errors.Is(err, ErrAssessmentInvalid) {
			t.Errorf("err = %v, want ErrAssessmentInvalid", err)
		}
	})
	t.Run("no webcam clears its interval", func(t *testing.T) {
		in := validInput()
		in.Integrity = IntegritySettings{WebcamEvery: 120}
		got, err := in.clean()
		if err != nil {
			t.Fatalf("clean: %v", err)
		}
		if got.Integrity.WebcamEvery != 0 {
			t.Errorf("interval = %d, want 0 without the webcam", got.Integrity.WebcamEvery)
		}
	})
}

func TestAssessmentInputCleanNormalisesAllowedLanguages(t *testing.T) {
	in := validInput()
	in.AllowedLanguages = []string{"Node", " python ", "python"}
	got, err := in.clean()
	if err != nil {
		t.Fatalf("clean: %v", err)
	}
	if len(got.AllowedLanguages) != 2 || got.AllowedLanguages[0] != "python" || got.AllowedLanguages[1] != "javascript" {
		t.Errorf("allowed = %v, want [python javascript] in registry order", got.AllowedLanguages)
	}

	in = validInput()
	in.AllowedLanguages = []string{"go", "any"}
	got, err = in.clean()
	if err != nil {
		t.Fatalf("clean: %v", err)
	}
	if len(got.AllowedLanguages) != 0 {
		t.Errorf("allowed = %v, want any to mean the empty set", got.AllowedLanguages)
	}

	in = validInput()
	in.AllowedLanguages = []string{"cobol"}
	if _, err := in.clean(); !errors.Is(err, ErrAssessmentInvalid) {
		t.Errorf("err = %v, want ErrAssessmentInvalid", err)
	}
}

// A candidate answers in a language both the assessment and the problem
// allow; the assessment's empty set means it defers to the problem.
func TestSessionLanguagesIntersectsTheAssessmentAndTheProblem(t *testing.T) {
	problem := Problem{AllowedLanguages: []string{"python", "go", "rust"}}
	cases := []struct {
		name    string
		allowed []string
		want    []string
	}{
		{"empty defers to the problem", nil, []string{"python", "go", "rust"}},
		{"intersection keeps the problem's order", []string{"rust", "python", "java"}, []string{"python", "rust"}},
		{"no overlap leaves nothing", []string{"java"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sessionLanguages(Assessment{AllowedLanguages: tc.allowed}, problem)
			if len(got) != len(tc.want) {
				t.Fatalf("languages = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("languages = %v, want %v", got, tc.want)
				}
			}
		})
	}
}
