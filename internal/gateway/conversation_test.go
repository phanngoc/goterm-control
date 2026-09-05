package gateway

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/ngocp/goterm-control/internal/execution"
	"github.com/ngocp/goterm-control/internal/models"
	"github.com/ngocp/goterm-control/internal/session"
	"github.com/ngocp/goterm-control/internal/storage"
)

// capturingTurn is a TurnRunner that records which session it was handed.
type capturingTurn struct {
	sessionID string
	chatID    int64
}

func (c *capturingTurn) RunTurn(ctx context.Context, sess *session.Session, chatID int64,
	modelID, userText string, sink TurnSink) (*execution.RunResult, error) {
	c.sessionID = sess.ID
	c.chatID = chatID
	sink.Write("ok")
	return &execution.RunResult{SessionID: sess.ID, Status: execution.RunSuccess}, nil
}

// TestDashboardMessageLandsInTheBoundConversation is S1's acceptance test: a
// dashboard message with no session named must run on the ACTIVE session of the
// conversation the web account is bound to — the Telegram one — not on a
// dashboard-private chat_1.
func TestDashboardMessageLandsInTheBoundConversation(t *testing.T) {
	dir := t.TempDir()
	sdb, err := storage.Open(filepath.Join(dir, "goterm.db"))
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	defer sdb.Close()

	sessions := session.NewManager(storage.NewSessionStore(sdb))
	// The Telegram side has been talking: chat 555, currently on its 2nd session.
	sessions.Get(555)
	telegramActive, err := sessions.NewSession(555)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	conv := storage.NewConversationStore(sdb)
	conv.EnsureTelegramBindings([]int64{555})

	turn := &capturingTurn{}
	deps := Deps{
		Sessions:      sessions,
		Resolver:      models.NewResolver("claude-opus-4-8", nil),
		DataDir:       dir,
		Conversations: conv,
		Turn:          turn,
	}

	params, _ := json.Marshal(SendParams{Message: "hello from the web"})
	var final string
	NewStreamSendHandler(deps)(context.Background(),
		Request{ID: "1", Method: "send", Params: params},
		func(ev StreamEvent) {
			if ev.Type == "response" {
				final = ev.Data
			}
		})

	if turn.sessionID != telegramActive.ID {
		t.Errorf("dashboard message ran on %q, want the Telegram conversation's active session %q",
			turn.sessionID, telegramActive.ID)
	}
	if turn.chatID != 555 {
		t.Errorf("queued on chat lane %d, want 555 (so web and Telegram never run concurrently)", turn.chatID)
	}
	if turn.sessionID == "chat_1" {
		t.Error("message fell back to the dashboard-private chat")
	}

	var resp map[string]any
	if err := json.Unmarshal([]byte(final), &resp); err != nil {
		t.Fatalf("response frame: %v (%q)", err, final)
	}
	if resp["session_id"] != telegramActive.ID {
		t.Errorf("response session_id = %v, want %q so the UI selects the shared session", resp["session_id"], telegramActive.ID)
	}
}

// Without a conversation store the gateway must keep the old behaviour so
// minimal setups and existing tests are unaffected.
func TestDashboardFallsBackToFixedChatWithoutBindings(t *testing.T) {
	dir := t.TempDir()
	sdb, err := storage.Open(filepath.Join(dir, "goterm.db"))
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	defer sdb.Close()

	turn := &capturingTurn{}
	deps := Deps{
		Sessions: session.NewManager(storage.NewSessionStore(sdb)),
		Resolver: models.NewResolver("claude-opus-4-8", nil),
		DataDir:  dir,
		Turn:     turn,
	}
	params, _ := json.Marshal(SendParams{Message: "hi"})
	NewStreamSendHandler(deps)(context.Background(),
		Request{ID: "1", Method: "send", Params: params}, func(StreamEvent) {})

	if turn.chatID != dashboardChatID {
		t.Errorf("chat lane = %d, want the fixed dashboard chat %d", turn.chatID, dashboardChatID)
	}
}
