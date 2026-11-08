package bot

import (
	"context"
	"log"
	"strings"

	"github.com/ngocp/goterm-control/internal/chat"
	"github.com/ngocp/goterm-control/internal/coord"
)

// The owner talks to an agent in two places: its Telegram bot, and the Direct
// room on the dashboard. They are one conversation, so what is said on one has
// to be readable on the other. The engine is the one place every owner turn
// passes through — Telegram, the dashboard's Chat tab, a photo with a caption —
// which is why the reflection is written here rather than in each channel.
//
// The copies are Mirror rows: a record of a turn already taken, never a
// mention, so the room never asks this agent to answer what it just answered.

// mirrorsToDirect reports whether a turn on chatID belongs in the owner's
// Direct room. Conversations with a person have a non-negative chat id; task
// runs (-1) and channel threads (-2) are the agent talking to the queue and to
// a room, and are already visible where they happen.
func (h *Handler) mirrorsToDirect(ctx context.Context, chatID int64) bool {
	return h.coord != nil && h.agentID != "" && chatID >= 0 && !chat.IsFromDirectRoom(ctx)
}

// mirrorToDirect writes one line of the owner's conversation into their Direct
// room with this agent. Failure is logged and swallowed: the turn itself has
// already happened, and a missing copy must never cost the owner the answer.
func (h *Handler) mirrorToDirect(ctx context.Context, chatID int64, fromOwner bool, body string) {
	if !h.mirrorsToDirect(ctx, chatID) || strings.TrimSpace(body) == "" {
		return
	}
	room, err := h.coord.EnsureOwnerDM(h.agentID)
	if err != nil {
		log.Printf("direct mirror: room with owner: %v", err)
		return
	}
	kind, id := coord.MemberAgent, h.agentID
	if fromOwner {
		kind, id = coord.MemberUser, coord.OwnerUserID
	}
	if _, _, err := h.coord.PostMessage(coord.NewChannelMessage{
		ChannelID: room.ID, AuthorKind: kind, AuthorID: id, Body: body, Mirror: true,
	}); err != nil {
		log.Printf("direct mirror: %v", err)
	}
}
