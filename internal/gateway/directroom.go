package gateway

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/ngocp/goterm-control/internal/chat"
	"github.com/ngocp/goterm-control/internal/coord"
)

// The owner's Direct room with this agent is the same conversation as the
// agent's Telegram chat — the bot copies every Telegram turn into the room
// (bot/directmirror.go), and this is the other half: a line the owner types at
// the top of the room runs on the conversation's own session, the one Telegram
// and the Chat tab are on, rather than opening a thread session of its own.
// Turn 2 typed on the dashboard remembers turn 1 typed on the phone.
//
// A reply inside a thread of that room is still a thread: a side conversation
// about one line, answered the way any room answers it.

// isOwnerDirect reports whether m is the owner speaking at the top of their
// Direct room with this agent.
func (w *MentionWatcher) isOwnerDirect(m coord.ChannelMessage) bool {
	return w.deps.Sessions != nil &&
		m.ThreadRoot == "" &&
		m.AuthorKind == coord.MemberUser &&
		m.ChannelID == coord.DMChannelID(coord.OwnerUserID, w.deps.AgentID)
}

// answerDirect runs one Direct-room line as a turn of the owner's conversation
// and lands the answer in the room, in place of its progress line.
func (w *MentionWatcher) answerDirect(ctx context.Context, m coord.ChannelMessage) {
	clear := func() {
		if _, err := w.deps.Coord.MarkMentionsRead(coord.MemberAgent, w.deps.AgentID, []string{m.ID}); err != nil {
			log.Printf("direct: clear %s: %v", m.ID, err)
		}
	}

	sess := w.deps.Sessions.Get(webConversation(w.deps))

	progress, _, err := w.deps.Coord.PostMessage(coord.NewChannelMessage{
		ChannelID: m.ChannelID, AuthorKind: coord.MemberAgent, AuthorID: w.deps.AgentID,
		Body: workingLine(progressView{}),
	})
	if err != nil {
		log.Printf("direct: could not open a progress line: %v", err)
		progress = nil
	}
	sink := &replySink{preview: chat.DefaultPreview()}
	if progress != nil {
		sink.onProgress = func(p progressView) {
			if err := w.deps.Coord.UpdateMessageBody(progress.ID, workingLine(p)); err != nil {
				log.Printf("direct: progress update: %v", err)
			}
		}
	}

	// Marked so the engine does not copy this turn back into the room it came
	// from: the owner's line is already a row, and the answer is about to be.
	turnCtx, cancel := context.WithTimeout(chat.FromDirectRoom(ctx), mentionTurnTimeout)
	defer cancel()

	w.live.Store(m.ID, sess)
	_, err = w.deps.Turn.RunTurn(turnCtx, sess, sess.ChatID, w.model(), strings.TrimSpace(m.Body), sink)
	sink.stop()
	w.live.Delete(m.ID)

	if err != nil {
		// Same rule as a room: a turn killed by shutdown is left unread so the
		// next gateway answers it.
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			log.Printf("direct: %s interrupted by shutdown — leaving it unread for the next run", m.ID)
			w.finishProgress(progress, "")
			return
		}
		log.Printf("direct: turn for %s: %v", m.ID, err)
		note := "⚠️ lượt này hỏng giữa chừng: " + truncateLine(err.Error(), 200)
		if errors.Is(err, context.DeadlineExceeded) {
			note = fmt.Sprintf("⌛ hết %s cho một lượt trả lời — nhờ mở task cho việc dài.", mentionTurnTimeout)
		}
		w.finishProgress(progress, note)
		clear()
		return
	}

	reply := strings.TrimSpace(sink.Text())
	if progress != nil {
		w.finishProgress(progress, reply)
	} else if reply != "" {
		if _, _, err := w.deps.Coord.PostMessage(coord.NewChannelMessage{
			ChannelID: m.ChannelID, AuthorKind: coord.MemberAgent, AuthorID: w.deps.AgentID, Body: reply,
		}); err != nil {
			log.Printf("direct: post reply: %v", err)
		}
	}
	clear()
}
