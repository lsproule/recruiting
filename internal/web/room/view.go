package room

import (
	"time"

	"recruiting/internal/service"
)

// pageView is the room page: the room, where its API lives, the island's
// boot JSON, and where Leave goes.
type pageView struct {
	Room   service.Room
	Base   string
	Config string
	Back   string
	Now    time.Time
	// Embed draws the room without its header, for the console's frame.
	Embed bool
}
