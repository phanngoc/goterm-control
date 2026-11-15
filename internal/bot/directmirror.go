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
//
// The owner's line goes at the top of the room; the agent's answer goes in
// that line's thread (threadRoot), the way every room answers a request. It
// returns the row written, so the answer knows where to go.
func (h *Handler) mirrorToDirect(ctx context.Context, chatID int64, fromOwner bool, body, threadRoot string) *coord.ChannelMessage {
	if !h.mirrorsToDirect(ctx, chatID) || strings.TrimSpace(body) == "" {
		return nil
	}
	room, err := h.coord.EnsureOwnerDM(h.agentID)
	if err != nil {
		log.Printf("direct mirror: room with owner: %v", err)
		return nil
	}
	kind, id := coord.MemberAgent, h.agentID
	if fromOwner {
		kind, id = coord.MemberUser, coord.OwnerUserID
	}
	msg, _, err := h.coord.PostMessage(coord.NewChannelMessage{
		ChannelID: room.ID, ThreadRoot: threadRoot, AuthorKind: kind, AuthorID: id, Body: body, Mirror: true,
	})
	if err != nil {
		log.Printf("direct mirror: %v", err)
		return nil
	}
	return msg
}

// RoomTurns opens a room turn — progress card, task, session log, answer — for
// a Telegram line mirrored into the Direct room, so a turn taken on the phone
// reads on the dashboard exactly like one taken there. Set by the gateway
// (gateway.RoomTurnHook); nil keeps the plain mirror.
type RoomTurns func(root coord.ChannelMessage, trace func() string) (chat.TurnSink, func(reply string, err error))

// SetRoomTurns installs the hook.
func (h *Handler) SetRoomTurns(fn RoomTurns) { h.roomTurns = fn }
