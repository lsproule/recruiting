package service

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// The editor records the keymap the candidate picked, the fullscreen and
// snapshot integrity beats, and the consent answer. Each carries a fixed
// shape; anything else is a client the server does not recognise.
func TestValidateEventChecksTheIntegrityAndKeymapShapes(t *testing.T) {
	problemID := uuid.New()
	now := time.Now()
	a := Assessment{Problems: []Problem{{ID: problemID, AllowedLanguages: []string{"python"}}}}
	ev := func(kind, data string) AttemptEvent {
		return AttemptEvent{Seq: 1, T: now.UnixMilli(), ProblemID: problemID, Type: kind, Data: json.RawMessage(data)}
	}

	good := map[string]AttemptEvent{
		"keymap default":   ev("keymap", `{"keymap":"default"}`),
		"keymap vim":       ev("keymap", `{"keymap":"vim"}`),
		"keymap emacs":     ev("keymap", `{"keymap":"emacs"}`),
		"fullscreen enter": ev("fullscreen_enter", `{}`),
		"fullscreen exit":  ev("fullscreen_exit", `{}`),
		"snapshot ok":      ev("snapshot", `{"seq":3,"ok":true}`),
		"snapshot gap":     ev("snapshot", `{"seq":4,"ok":false}`),
		"consent":          ev("consent", `{"webcam":true,"photo_id":false}`),
	}
	for name, e := range good {
		if err := validateEvent(e, a, now); err != nil {
			t.Errorf("%s: err = %v, want accepted", name, err)
		}
	}

	bad := map[string]AttemptEvent{
		"unknown keymap":     ev("keymap", `{"keymap":"nano"}`),
		"missing keymap":     ev("keymap", `{}`),
		"keymap not string":  ev("keymap", `{"keymap":3}`),
		"keymap not object":  ev("keymap", `"vim"`),
		"snapshot no seq":    ev("snapshot", `{"ok":true}`),
		"snapshot bad seq":   ev("snapshot", `{"seq":-1,"ok":true}`),
		"snapshot no ok":     ev("snapshot", `{"seq":1}`),
		"consent no webcam":  ev("consent", `{"photo_id":true}`),
		"consent no photoid": ev("consent", `{"webcam":true}`),
	}
	for name, e := range bad {
		if err := validateEvent(e, a, now); !errors.Is(err, ErrEventInvalid) {
			t.Errorf("%s: err = %v, want ErrEventInvalid", name, err)
		}
	}
}
