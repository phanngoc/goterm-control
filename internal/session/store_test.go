package session

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStoreRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")
	store := NewStore(path)

	// Create sessions as ChatState map
	chats := map[int64]*ChatState{
		100: {
			ActiveSessionID: "chat_100",
			NextSeq:         1,
			Sessions: map[string]*Session{
				"chat_100": {ID: "chat_100", ChatID: 100, CreatedAt: time.Now(), UpdatedAt: time.Now(), ClaudeSessionID: "abc123", MessageCount: 5, InputTokens: 1000, OutputTokens: 500},
			},
		},
		200: {
			ActiveSessionID: "chat_200",
			NextSeq:         1,
			Sessions: map[string]*Session{
				"chat_200": {ID: "chat_200", ChatID: 200, CreatedAt: time.Now(), UpdatedAt: time.Now()},
			},
		},
	}

	if err := store.Save(chats); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if len(loaded) != 2 {
		t.Fatalf("expected 2 chats, got %d", len(loaded))
	}

	cs := loaded[100]
	s := cs.Sessions["chat_100"]
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
	chats, err := store.Load()
	if err != nil {
		t.Fatalf("expected nil error for missing file, got: %v", err)
	}
	if len(chats) != 0 {
		t.Fatalf("expected empty map, got %d", len(chats))
	}
}

func TestManagerSessionPersistsUntilReset(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "sessions.json"))

	mgr := NewManager(store)

	sess := mgr.Get(100)
	sess.SetSessionID("original-session")

	time.Sleep(5 * time.Millisecond)

	sess2 := mgr.Get(100)
	if sess2.GetSessionID() != "original-session" {
		t.Errorf("expected session to persist, got %q", sess2.GetSessionID())
	}

	mgr.Reset(100)
	sess3 := mgr.Get(100)
	if sess3.GetSessionID() != "" {
		t.Errorf("expected empty session after explicit reset, got %s", sess3.GetSessionID())
	}
}

func TestManagerMultiSession(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "sessions.json"))
	mgr := NewManager(store)

	// First session auto-created on Get
	s1 := mgr.Get(100)
	s1.SetSessionID("claude-1")
	s1.SetLabel("First chat")

	// Create second session
	s2, err := mgr.NewSession(100)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	s2.SetSessionID("claude-2")
	s2.SetLabel("Second chat")

	// Active session should be s2 now
	active := mgr.Get(100)
	if active.GetSessionID() != "claude-2" {
		t.Errorf("active = %q, want claude-2", active.GetSessionID())
	}

	// List should return both
	list := mgr.ListForChat(100)
	if len(list) != 2 {
		t.Fatalf("ListForChat = %d, want 2", len(list))
	}

	// Switch back to s1
	if err := mgr.SwitchActive(100, s1.ID); err != nil {
		t.Fatalf("SwitchActive: %v", err)
	}
	active = mgr.Get(100)
	if active.GetSessionID() != "claude-1" {
		t.Errorf("after switch, active = %q, want claude-1", active.GetSessionID())
	}

	// Reset only resets active session
	mgr.Reset(100)
	if mgr.Get(100).GetSessionID() != "" {
		t.Error("expected active session reset")
	}
	// s2 should still be intact
	s2check := mgr.GetByID(s2.ID)
	if s2check.GetSessionID() != "claude-2" {
		t.Errorf("s2 should be intact, got %q", s2check.GetSessionID())
	}
}

func TestManagerSessionLimit(t *testing.T) {
	mgr := NewManager(nil) // in-memory only

	mgr.Get(100) // creates first session
	for i := 1; i < MaxSessionsPerChat; i++ {
		if _, err := mgr.NewSession(100); err != nil {
			t.Fatalf("NewSession %d: %v", i, err)
		}
	}

	// Should fail at the limit
	_, err := mgr.NewSession(100)
	if err == nil {
		t.Error("expected error at session limit")
	}
}
