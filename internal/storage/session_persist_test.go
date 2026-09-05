package storage

import (
	"path/filepath"
	"testing"

	"github.com/ngocp/goterm-control/internal/agent"
	"github.com/ngocp/goterm-control/internal/session"
)

// TestNewSessionIsPersistedBeforeUse covers a real production loss: the
// manager used to persist a freshly created session on a one-second debounce,
// but the first message of a new chat arrives well inside that second. The
// messages table references sessions by foreign key, so that message was
// rejected with "FOREIGN KEY constraint failed" and silently dropped (seen in
// the agent 2 log, 2026-09-04 16:03). Telegram hit it only on a brand-new chat;
// the dashboard, once on the shared engine, would hit it on the first message
// of every new session.
func TestNewSessionIsPersistedBeforeUse(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "goterm.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	mgr := session.NewManager(NewSessionStore(db))
	msgs := NewMessageStore(db)

	// Brand-new chat via Get — the dashboard's path.
	got := mgr.Get(42)
	if err := msgs.Append(got.ID, agent.Message{Role: "user", Content: "first ever message"}); err != nil {
		t.Fatalf("first message of a new chat was rejected: %v", err)
	}

	// Explicit /new — the Telegram path — must be just as safe.
	fresh, err := mgr.NewSession(42)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	if fresh.ID == got.ID {
		t.Fatalf("NewSession returned the existing session %s", fresh.ID)
	}
	if err := msgs.Append(fresh.ID, agent.Message{Role: "user", Content: "first message after /new"}); err != nil {
		t.Fatalf("first message after /new was rejected: %v", err)
	}

	// And the rows are really there, not just accepted by a lenient store.
	var n int
	if err := db.Conn().QueryRow(`SELECT count(*) FROM sessions WHERE chat_id = 42`).Scan(&n); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if n != 2 {
		t.Errorf("sessions persisted = %d, want 2 (both created sessions, synchronously)", n)
	}
}
