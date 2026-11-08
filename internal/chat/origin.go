package chat

import "context"

// directRoomKey marks a turn that was typed in the owner's Direct room on the
// dashboard rather than on Telegram.
type directRoomKey struct{}

// FromDirectRoom marks ctx as a turn the owner started in their Direct room.
//
// The turn engine reflects every owner conversation into that room, so the
// dashboard shows what was said on Telegram. A turn that STARTED in the room
// is already there — the owner's line is a row, and the reply lands in place
// of the progress line — so mirroring it as well would print both twice.
// Lives here because both bot (which mirrors) and gateway (which starts such
// turns) import chat, and neither may import the other.
func FromDirectRoom(ctx context.Context) context.Context {
	return context.WithValue(ctx, directRoomKey{}, true)
}

// IsFromDirectRoom reports whether ctx was marked by FromDirectRoom.
func IsFromDirectRoom(ctx context.Context) bool {
	v, _ := ctx.Value(directRoomKey{}).(bool)
	return v
}
