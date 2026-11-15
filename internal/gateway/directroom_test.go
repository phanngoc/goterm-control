package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/ngocp/goterm-control/internal/coord"
	"github.com/ngocp/goterm-control/internal/session"
)

// A line the owner types at the top of their Direct room is the next turn of
// the conversation they have with this agent on Telegram: it runs on that
// conversation's session, with the words as typed, and the answer lands in the
// line's thread — like any room — and is not copied back a second time.
func TestADirectLineContinuesTheOwnersConversation(t *testing.T) {
	turn := &recordingTurn{reply: "đây là câu trả lời"}
	deps, cdb := mentionTestDeps(t, turn)
	deps.Sessions = session.NewManager(nil)
	conversation := deps.Sessions.Get(webConversation(deps))

	dm, err := cdb.EnsureOwnerDM("bomclaw2")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := cdb.PostMessage(coord.NewChannelMessage{
		ChannelID: dm.ID, AuthorKind: coord.MemberUser, AuthorID: coord.OwnerUserID,
		Body: "tiếp tục việc hôm qua", Notify: []coord.Member{{Kind: coord.MemberAgent, ID: "bomclaw2"}},
	}); err != nil {
		t.Fatal(err)
	}

	NewMentionWatcher(deps).sweep(context.Background())

	if turn.count() != 1 {
		t.Fatalf("turns = %d, want 1", turn.count())
	}
	if turn.sessions[0] != conversation.ID {
		t.Errorf("ran on %s, want the owner's conversation %s", turn.sessions[0], conversation.ID)
	}
	if turn.prompts[0] != "tiếp tục việc hôm qua" {
		t.Errorf("prompt = %q, want the line as typed — this is a conversation, not a summons", turn.prompts[0])
	}
	if !turn.direct[0] {
		t.Error("the turn was not marked as typed in the room, so the engine would copy it back")
	}

	msgs, err := cdb.ChannelMessages(dm.ID, 10, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("top of the room = %+v, want only the owner's line", msgs)
	}
	thread, err := cdb.ThreadMessages(msgs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	var answered bool
	for _, m := range thread {
		if m.AuthorID == "bomclaw2" && m.Body == "đây là câu trả lời" && m.ThreadRoot == msgs[0].ID {
			answered = true
		}
	}
	if !answered {
		t.Fatalf("no answer in the line's thread: %+v", thread)
	}
	if left, _ := cdb.UnreadMentions(coord.MemberAgent, "bomclaw2", 10); len(left) != 0 {
		t.Errorf("%d mention(s) left unread after answering", len(left))
	}
}

// A reply inside a thread of the Direct room is the same conversation too: it
// runs on the owner's session, and is answered in that thread.
func TestAReplyInADirectThreadContinuesTheConversation(t *testing.T) {
	turn := &recordingTurn{reply: "ok"}
	deps, cdb := mentionTestDeps(t, turn)
	deps.Sessions = session.NewManager(nil)
	conversation := deps.Sessions.Get(webConversation(deps))

	dm, err := cdb.EnsureOwnerDM("bomclaw2")
	if err != nil {
		t.Fatal(err)
	}
	root, _, err := cdb.PostMessage(coord.NewChannelMessage{
		ChannelID: dm.ID, AuthorKind: coord.MemberAgent, AuthorID: "bomclaw2", Body: "báo cáo xong",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := cdb.PostMessage(coord.NewChannelMessage{
		ChannelID: dm.ID, ThreadRoot: root.ID, AuthorKind: coord.MemberUser, AuthorID: coord.OwnerUserID,
		Body: "giải thích dòng này", Notify: []coord.Member{{Kind: coord.MemberAgent, ID: "bomclaw2"}},
	}); err != nil {
		t.Fatal(err)
	}

	NewMentionWatcher(deps).sweep(context.Background())

	if turn.count() != 1 {
		t.Fatalf("turns = %d, want 1", turn.count())
	}
	if turn.sessions[0] != conversation.ID {
		t.Errorf("ran on %s, want the owner's conversation %s", turn.sessions[0], conversation.ID)
	}
	thread, _ := cdb.ThreadMessages(root.ID)
	if last := thread[len(thread)-1]; last.AuthorID != "bomclaw2" || last.Body != "ok" {
		t.Fatalf("answer not in the thread: %+v", thread)
	}
}
