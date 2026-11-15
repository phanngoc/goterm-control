package gateway

import (
	"context"
	"errors"
	"log"
	"strings"

	"github.com/ngocp/goterm-control/internal/chat"
	"github.com/ngocp/goterm-control/internal/coord"
)

// The owner's Direct room with this agent is the same conversation as the
// agent's Telegram chat — the bot copies every Telegram turn into the room
// (bot/directmirror.go), and this is the other half: a line the owner types in
// the room runs on the conversation's own session, the one Telegram is on,
// rather than opening a thread session of its own. Turn 2 typed on the
// dashboard remembers turn 1 typed on the phone.
//
// It is answered like any room line, though: in the thread of the line it
// answers, with a live progress card, a task on the board once it is work,
// and a session log to watch (roomturn.go). The top of the room reads as a
// list of requests, each with its answer under it — the same as a project
// room — and a reply inside one of those threads continues the same
// conversation.

// isOwnerDirect reports whether m is the owner speaking in their Direct room
// with this agent.
func (w *MentionWatcher) isOwnerDirect(m coord.ChannelMessage) bool {
	return w.deps.Sessions != nil &&
		m.AuthorKind == coord.MemberUser &&
		m.ChannelID == coord.DMChannelID(coord.OwnerUserID, w.deps.AgentID)
}

// answerDirect runs one Direct-room line as a turn of the owner's conversation
// and answers it in the line's thread.
func (w *MentionWatcher) answerDirect(ctx context.Context, m coord.ChannelMessage) {
	clear := func() {
		if _, err := w.deps.Coord.MarkMentionsRead(coord.MemberAgent, w.deps.AgentID, []string{m.ID}); err != nil {
			log.Printf("direct: clear %s: %v", m.ID, err)
		}
	}

	sess := w.deps.Sessions.Get(webConversation(w.deps))
	rt := OpenRoomTurn(w.deps, m, coord.ThreadKey(m), sess.CurrentTrace)

	// Marked so the engine does not copy this turn back into the room it came
	// from: the owner's line is already a row, and the answer is about to be.
	timed, cancelTimeout := context.WithTimeout(chat.FromDirectRoom(ctx), mentionTurnTimeout)
	defer cancelTimeout()
	turnCtx, cancel := context.WithCancelCause(timed)
	defer cancel(nil)
	rt.WatchInterrupts(cancel)

	w.live.Store(m.ID, sess)
	_, err := w.deps.Turn.RunTurn(turnCtx, sess, sess.ChatID, w.model(), strings.TrimSpace(m.Body), rt.Sink())
	w.live.Delete(m.ID)

	if err != nil {
		// Same rule as a room: a turn killed by shutdown is left unread so the
		// next gateway answers it.
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			log.Printf("direct: %s interrupted by shutdown — leaving it unread for the next run", m.ID)
			rt.Abandon()
			return
		}
		if cause := context.Cause(turnCtx); errors.Is(cause, ErrRoomSteered) {
			err = cause
		}
		log.Printf("direct: turn for %s: %v", m.ID, err)
	}
	rt.Finish(rt.Sink().Text(), err)
	clear()
}
