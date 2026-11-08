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
// room itself — not in a thread, and not copied back a second time.
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
	var answered bool
	for _, m := range msgs {
		if m.AuthorID == "bomclaw2" && m.Body == "đây là câu trả lời" && m.ThreadRoot == "" {
			answered = true
		}
	}
	if !answered {
		t.Fatalf("no top-level answer in the room: %+v", msgs)
	}
	if left, _ := cdb.UnreadMentions(coord.MemberAgent, "bomclaw2", 10); len(left) != 0 {
		t.Errorf("%d mention(s) left unread after answering", len(left))
	}
}

// A reply inside a thread of the Direct room is a side conversation about one
// line, and keeps the thread's own session like any room.
func TestAThreadInTheDirectRoomIsStillAThread(t *testing.T) {
	turn := &recordingTurn{reply: "ok"}
	deps, cdb := mentionTestDeps(t, turn)
	deps.Sessions = session.NewManager(nil)

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
	if turn.sessions[0] != "ch_"+root.ID {
		t.Errorf("ran on %s, want the thread's session ch_%s", turn.sessions[0], root.ID)
	}
	if turn.direct[0] {
		t.Error("a thread turn was marked as a Direct-room conversation turn")
	}
}
