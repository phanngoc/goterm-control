package gateway

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ngocp/goterm-control/internal/coord"
	"github.com/ngocp/goterm-control/internal/execution"
	"github.com/ngocp/goterm-control/internal/session"
)

// recordingTurn answers every turn with a fixed reply and remembers what it was
// asked, including the session it ran under — which is how these tests see
// whether the thread's conversation was resumed.
type recordingTurn struct {
	mu       sync.Mutex
	calls    int
	prompts  []string
	sessions []string
	resumed  []string
	reply    string
	newID    string
}

func (r *recordingTurn) RunTurn(ctx context.Context, sess *session.Session, chatID int64,
	modelID, userText string, sink TurnSink) (*execution.RunResult, error) {
	r.mu.Lock()
	r.calls++
	r.prompts = append(r.prompts, userText)
	r.sessions = append(r.sessions, sess.ID)
	r.resumed = append(r.resumed, sess.GetSessionID())
	reply, newID := r.reply, r.newID
	r.mu.Unlock()

	if newID != "" {
		sess.SetSessionID(newID) // the CLI hands back the session it just wrote
	}
	sink.Write(reply)
	return &execution.RunResult{SessionID: sess.ID, Status: execution.RunSuccess}, nil
}

func (r *recordingTurn) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func mentionTestDeps(t *testing.T, turn *recordingTurn) (Deps, *coord.DB) {
	t.Helper()
	cdb, err := coord.Open(filepath.Join(t.TempDir(), "coord.db"))
	if err != nil {
		t.Fatalf("coord: %v", err)
	}
	t.Cleanup(func() { cdb.Close() })
	for _, id := range []string{"bomclaw", "bomclaw2"} {
		if err := cdb.RegisterAgent(coord.Agent{ID: id, DisplayName: id, WSAddr: "ws://127.0.0.1:0/ws"}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
	return Deps{Coord: cdb, Turn: turn, AgentID: "bomclaw2", ProviderName: "claude"}, cdb
}

// TestMentionBecomesAChatTurn is the acceptance test of the whole feature:
// naming an agent must produce an answer in the room, not silence — and not a
// task, which is what the first attempt at this did.
func TestMentionBecomesAChatTurn(t *testing.T) {
	turn := &recordingTurn{reply: "chào, mình đây", newID: "sess-1"}
	deps, cdb := mentionTestDeps(t, turn)

	m, wake, err := cdb.PostMessage(coord.NewChannelMessage{
		ChannelID: coord.GeneralChannelID, AuthorKind: coord.MemberUser, AuthorID: coord.OwnerUserID,
		Body: "@bomclaw2 chào bạn",
	})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if len(wake) != 1 || wake[0] != "bomclaw2" {
		t.Fatalf("expected bomclaw2 to be woken, got %v", wake)
	}

	NewMentionWatcher(deps).sweep(context.Background())

	if turn.count() != 1 {
		t.Fatalf("expected exactly one turn, got %d", turn.count())
	}
	if !strings.Contains(turn.prompts[0], "chào bạn") {
		t.Fatalf("prompt did not carry what was said:\n%s", turn.prompts[0])
	}

	// The reply is in the room, in a thread under the line that named it.
	thread, err := cdb.ThreadMessages(m.ID)
	if err != nil {
		t.Fatalf("thread: %v", err)
	}
	if len(thread) != 2 {
		t.Fatalf("expected the mention and one reply, got %d messages", len(thread))
	}
	if thread[1].Body != "chào, mình đây" || thread[1].AuthorID != "bomclaw2" {
		t.Fatalf("unexpected reply: %+v", thread[1])
	}

	// And the summons is cleared, or the next sweep answers it again forever.
	left, err := cdb.UnreadMentions(coord.MemberAgent, "bomclaw2", 10)
	if err != nil {
		t.Fatalf("unread: %v", err)
	}
	if len(left) != 0 {
		t.Fatalf("mention still unread after being answered: %v", left)
	}
}

// TestSecondTurnInAThreadResumesTheFirst is the difference between a
// conversation and two strangers: turn 2 must run on the session turn 1 left.
func TestSecondTurnInAThreadResumesTheFirst(t *testing.T) {
	turn := &recordingTurn{reply: "ừ", newID: "sess-1"}
	deps, cdb := mentionTestDeps(t, turn)
	w := NewMentionWatcher(deps)

	root, _, err := cdb.PostMessage(coord.NewChannelMessage{
		ChannelID: coord.GeneralChannelID, AuthorKind: coord.MemberUser, AuthorID: coord.OwnerUserID,
		Body: "@bomclaw2 xem hộ log",
	})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	w.sweep(context.Background())

	// A follow-up inside the same thread.
	if _, _, err := cdb.PostMessage(coord.NewChannelMessage{
		ChannelID: coord.GeneralChannelID, ThreadRoot: root.ID,
		AuthorKind: coord.MemberUser, AuthorID: coord.OwnerUserID,
		Body: "@bomclaw2 thế còn dòng cuối?",
	}); err != nil {
		t.Fatalf("follow-up: %v", err)
	}
	w.sweep(context.Background())

	if turn.count() != 2 {
		t.Fatalf("expected two turns, got %d", turn.count())
	}
	if turn.sessions[0] != turn.sessions[1] {
		t.Fatalf("thread ran under two different sessions: %s then %s", turn.sessions[0], turn.sessions[1])
	}
	if turn.resumed[0] != "" {
		t.Fatalf("first turn resumed something: %q", turn.resumed[0])
	}
	if turn.resumed[1] != "sess-1" {
		t.Fatalf("second turn did not resume the first: %q", turn.resumed[1])
	}
}

// TestThreadStopsAtTheTurnCap: A names B, B answers naming A, and nothing in a
// conversation ever says stop. The cap is the stop.
func TestThreadStopsAtTheTurnCap(t *testing.T) {
	turn := &recordingTurn{reply: "vẫn đây", newID: "sess-1"}
	deps, cdb := mentionTestDeps(t, turn)
	w := NewMentionWatcher(deps)

	root, _, err := cdb.PostMessage(coord.NewChannelMessage{
		ChannelID: coord.GeneralChannelID, AuthorKind: coord.MemberUser, AuthorID: coord.OwnerUserID,
		Body: "@bomclaw2 bắt đầu",
	})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	for i := 0; i < MaxThreadTurns+3; i++ {
		if _, _, err := cdb.PostMessage(coord.NewChannelMessage{
			ChannelID: coord.GeneralChannelID, ThreadRoot: root.ID,
			AuthorKind: coord.MemberAgent, AuthorID: "bomclaw",
			Body: "@bomclaw2 còn đó không",
		}); err != nil {
			t.Fatalf("post %d: %v", i, err)
		}
		w.sweep(context.Background())
	}
	if turn.count() != MaxThreadTurns {
		t.Fatalf("expected the thread to stop at %d turns, ran %d", MaxThreadTurns, turn.count())
	}
}

// TestStaleMentionIsClearedNotAnswered: answering yesterday's "are you there"
// is worse than not answering it.
func TestStaleMentionIsClearedNotAnswered(t *testing.T) {
	turn := &recordingTurn{reply: "muộn rồi"}
	deps, cdb := mentionTestDeps(t, turn)

	m, _, err := cdb.PostMessage(coord.NewChannelMessage{
		ChannelID: coord.GeneralChannelID, AuthorKind: coord.MemberUser, AuthorID: coord.OwnerUserID,
		Body: "@bomclaw2 còn thức không",
	})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	old := time.Now().Add(-StaleMention - time.Hour).UTC().Format(time.RFC3339Nano)
	if _, err := cdb.Conn().Exec(`UPDATE channel_messages SET created_at = ? WHERE id = ?`, old, m.ID); err != nil {
		t.Fatalf("age the message: %v", err)
	}

	NewMentionWatcher(deps).sweep(context.Background())

	if turn.count() != 0 {
		t.Fatalf("a day-old mention was answered")
	}
	left, err := cdb.UnreadMentions(coord.MemberAgent, "bomclaw2", 10)
	if err != nil {
		t.Fatalf("unread: %v", err)
	}
	if len(left) != 0 {
		t.Fatal("stale mention was left unread, so every sweep will look at it again")
	}
}

// TestPlainLineWakesNobody guards the shape of the feature: visibility and
// attention stay separate, so three agents in a room do not wake each other on
// every line.
func TestPlainLineWakesNobody(t *testing.T) {
	turn := &recordingTurn{reply: "không nên nói gì"}
	deps, cdb := mentionTestDeps(t, turn)

	if _, _, err := cdb.PostMessage(coord.NewChannelMessage{
		ChannelID: coord.GeneralChannelID, AuthorKind: coord.MemberUser, AuthorID: coord.OwnerUserID,
		Body: "hi bạn",
	}); err != nil {
		t.Fatalf("post: %v", err)
	}
	NewMentionWatcher(deps).sweep(context.Background())
	if turn.count() != 0 {
		t.Fatal("a line with no @ woke an agent")
	}
}
