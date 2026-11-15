package bot

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ngocp/goterm-control/internal/chat"
	"github.com/ngocp/goterm-control/internal/config"
	"github.com/ngocp/goterm-control/internal/coord"
	"github.com/ngocp/goterm-control/internal/execution"
	"github.com/ngocp/goterm-control/internal/memory"
	"github.com/ngocp/goterm-control/internal/session"
	"github.com/ngocp/goterm-control/internal/transcript"
)

func mirrorHandler(t *testing.T) (*Handler, *coord.DB) {
	t.Helper()
	dir := t.TempDir()
	cdb, err := coord.Open(filepath.Join(dir, "coord.db"))
	if err != nil {
		t.Fatalf("coord: %v", err)
	}
	t.Cleanup(func() { cdb.Close() })
	if err := cdb.RegisterAgent(coord.Agent{ID: "a1", DisplayName: "a1"}); err != nil {
		t.Fatal(err)
	}
	h := &Handler{
		sessions:   session.NewManager(nil),
		llm:        &scriptedClient{},
		cfg:        &config.Config{},
		engine:     execution.NewEngine(execution.Hooks{}, 3),
		transcript: transcript.NewWriter(filepath.Join(dir, "transcripts")),
		memory:     memory.NewManager(memory.Config{}),
		coord:      cdb,
		agentID:    "a1",
	}
	return h, cdb
}

func directLines(t *testing.T, cdb *coord.DB) []coord.ChannelMessage {
	t.Helper()
	msgs, err := cdb.ChannelMessages(coord.DMChannelID(coord.OwnerUserID, "a1"), 50, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	return msgs
}

func runOne(t *testing.T, h *Handler, ctx context.Context, chatID int64, text string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	sess := h.sessions.Get(chatID)
	if _, err := h.RunTurn(ctx, sess, chatID, "m", text, &recordingSink{}); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
}

// What the owner says to the bot on Telegram, and what it answers, is the
// owner's direct conversation with this agent — so it reads in the Direct
// room on the dashboard too, as a record that summons nobody: the owner's
// line at the top of the room, the answer in its thread, like any request.
func TestAnOwnerTurnIsReflectedInTheDirectRoom(t *testing.T) {
	h, cdb := mirrorHandler(t)
	runOne(t, h, context.Background(), 5531591024, "list the files")

	msgs := directLines(t, cdb)
	if len(msgs) != 1 || msgs[0].AuthorID != coord.OwnerUserID || msgs[0].Body != "list the files" {
		t.Fatalf("top of the direct room = %+v, want only the owner's line", msgs)
	}
	if len(msgs[0].Mentions) != 0 {
		t.Errorf("a mirrored line mentioned %v", msgs[0].Mentions)
	}
	thread, err := cdb.ThreadMessages(msgs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	var agent bool
	for _, m := range thread {
		if m.AuthorKind == coord.MemberAgent && m.AuthorID == "a1" && m.Body == "Hello done" && m.ThreadRoot == msgs[0].ID {
			agent = true
		}
	}
	if !agent {
		t.Fatalf("thread = %+v, want the reply in the owner's line's thread", thread)
	}
	if unread, _ := cdb.UnreadMentions(coord.MemberAgent, "a1", 10); len(unread) != 0 {
		t.Errorf("the mirror left %d mention(s) for the agent to answer again", len(unread))
	}
}

// A turn typed in the Direct room is already there, and a channel thread or a
// task run is not a conversation with the owner. None of them is copied.
func TestOnlyOwnerConversationsAreReflected(t *testing.T) {
	h, cdb := mirrorHandler(t)
	runOne(t, h, chat.FromDirectRoom(context.Background()), 1, "from the room")
	runOne(t, h, context.Background(), -2, "named in #room")
	runOne(t, h, context.Background(), -1, "task run")

	if msgs := directLines(t, cdb); len(msgs) != 0 {
		t.Fatalf("direct room has %d line(s), want none: %+v", len(msgs), msgs)
	}
}
