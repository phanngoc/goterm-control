package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")
	store := NewStore(path)

	// Create sessions
	sessions := map[int64]*Session{
		100: {ID: "chat_100", ChatID: 100, CreatedAt: time.Now(), UpdatedAt: time.Now(), ClaudeSessionID: "abc123", MessageCount: 5, InputTokens: 1000, OutputTokens: 500},
		200: {ID: "chat_200", ChatID: 200, CreatedAt: time.Now(), UpdatedAt: time.Now()},
	}

	// Save
	if err := store.Save(sessions); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file not created: %v", err)
	}

	// Load
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if len(loaded) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(loaded))
	}

	s := loaded[100]
	if s.ClaudeSessionID != "abc123" {
		t.Errorf("expected session ID abc123, got %s", s.ClaudeSessionID)
	}
	if s.MessageCount != 5 {
		t.Errorf("expected 5 messages, got %d", s.MessageCount)
	}
	if s.InputTokens != 1000 {
		t.Errorf("expected 1000 input tokens, got %d", s.InputTokens)
	}
}

func TestStoreLoadMissing(t *testing.T) {
	store := NewStore("/nonexistent/sessions.json")
	sessions, err := store.Load()
	if err != nil {
		t.Fatalf("expected nil error for missing file, got: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("expected empty map, got %d", len(sessions))
	}
}

func TestManagerIdleReset(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "sessions.json"))

	// 1ms idle timeout for testing
	mgr := NewManager(store, 1*time.Millisecond)

	sess := mgr.Get(100)
	sess.SetSessionID("original-session")

	time.Sleep(5 * time.Millisecond)

	sess2 := mgr.Get(100)
	if sess2.GetSessionID() != "" {
		t.Errorf("expected empty session after idle reset, got %s", sess2.GetSessionID())
	}
}
