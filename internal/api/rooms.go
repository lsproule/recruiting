package api

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"recruiting/internal/service"
)

// RoomCode is the shared editor of an interview room as last saved.
type RoomCode struct {
	Kind      string    `json:"kind" enum:"slot,pairing"`
	SubjectID uuid.UUID `json:"subject_id"`
	Language  string    `json:"language"`
	Source    string    `json:"source"`
	UpdatedAt time.Time `json:"updated_at"`
}

type roomHandlers struct{ d Deps }

// mountRooms registers the read of a room's editor. The live parts of a room
// (the stream, signalling, updates) are the HTML surface's: they are bound
// to a browser session and a page, not to an API token.
func (m *mounter) mountRooms() {
	h := roomHandlers{d: m.d}
	register(m, accessOrg, huma.Operation{
		OperationID: "get-room-code", Method: http.MethodGet, Path: "/rooms/{kind}/{subject_id}/code",
		Summary: "What was written in an interview room", Tags: []string{"rooms"},
		Description: "The shared editor of a booked interview (kind slot, the slot id) or a sprint conversation (kind pairing, the pairing id), as last saved.",
	}, h.code)
}

type roomInput struct {
	Kind      string    `path:"kind" enum:"slot,pairing"`
	SubjectID uuid.UUID `path:"subject_id"`
}

type roomCodeOutput struct{ Body RoomCode }

func (h roomHandlers) code(ctx context.Context, in *roomInput) (*roomCodeOutput, error) {
	key, err := service.ParseRoomKey(in.Kind, in.SubjectID.String())
	if err != nil {
		return nil, problemDetail(err)
	}
	code, err := h.d.Rooms.Code(ctx, principal(ctx), key)
	if err != nil {
		return nil, problemDetail(err)
	}
	return &roomCodeOutput{Body: RoomCode{
		Kind: key.Kind, SubjectID: key.ID, Language: code.Language, Source: code.Source, UpdatedAt: code.UpdatedAt,
	}}, nil
}
