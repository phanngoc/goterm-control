package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ngocp/goterm-control/internal/agent"
	"github.com/ngocp/goterm-control/internal/memory"
	"github.com/ngocp/goterm-control/internal/session"
)

func testDB(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOpenAndClose(t *testing.T) {
	db := testDB(t)
	if db.Conn() == nil {
		t.Fatal("expected non-nil connection")
	}
}

func TestSchemaCreation(t *testing.T) {
	db := testDB(t)

	// Verify tables exist
	tables := []string{"meta", "sessions", "messages", "memory"}
	for _, table := range tables {
		var name string
		err := db.conn.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %s not found: %v", table, err)
		}
	}

	// Verify schema version
	ver, err := db.currentVersion()
	if err != nil {
		t.Fatalf("currentVersion: %v", err)
	}
	if ver != schemaVersion {
		t.Errorf("version = %d, want %d", ver, schemaVersion)
	}
}

// --- Session Store Tests ---

func TestSessionStoreRoundtrip(t *testing.T) {
	db := testDB(t)
	store := NewSessionStore(db)

	// Save sessions
	sessions := map[int64]*session.Session{
		100: session.NewFromDB("chat_100", 100, time.Now(), time.Now(), "claude_abc", 5, 1000, 500, "summary text"),
		200: session.NewFromDB("chat_200", 200, time.Now(), time.Now(), "", 0, 0, 0, ""),
	}

	if err := store.Save(sessions); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Load sessions
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(loaded) != 2 {
		t.Fatalf("loaded %d sessions, want 2", len(loaded))
	}

	s := loaded[100]
	if s.ID != "chat_100" {
		t.Errorf("ID = %q, want chat_100", s.ID)
	}
	if s.GetSessionID() != "claude_abc" {
		t.Errorf("ClaudeSessionID = %q, want claude_abc", s.GetSessionID())
	}
	if s.GetMessageCount() != 5 {
		t.Errorf("MessageCount = %d, want 5", s.GetMessageCount())
	}
	if s.GetCompactSummary() != "summary text" {
		t.Errorf("CompactSummary = %q, want 'summary text'", s.GetCompactSummary())
	}
}

func TestSessionStoreUpsert(t *testing.T) {
	db := testDB(t)
	store := NewSessionStore(db)

	sessions := map[int64]*session.Session{
		100: session.NewFromDB("chat_100", 100, time.Now(), time.Now(), "v1", 1, 100, 50, ""),
	}
	store.Save(sessions)

	// Update
	sessions[100].SetSessionID("v2")
	sessions[100].IncrementMessages()
	store.Save(sessions)

	loaded, _ := store.Load()
	s := loaded[100]
	if s.GetSessionID() != "v2" {
		t.Errorf("updated ClaudeSessionID = %q, want v2", s.GetSessionID())
	}
	if s.GetMessageCount() != 2 {
		t.Errorf("updated MessageCount = %d, want 2", s.GetMessageCount())
	}
}

// --- Memory Store Tests ---

func TestMemoryStoreAppend(t *testing.T) {
	db := testDB(t)
	store := NewMemoryStore(db)

	entry := memory.Entry{
		SessionID: "chat_100",
		ChatID:    100,
		Facts:     []string{"path: /tmp/test.go", "url: https://example.com"},
		Keywords:  []string{"golang", "testing", "sqlite"},
		Summary:   "Testing SQLite memory store integration.",
	}

	if err := store.Append(entry); err != nil {
		t.Fatalf("Append: %v", err)
	}

	all, err := store.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("ReadAll returned %d entries, want 1", len(all))
	}

	got := all[0]
	if got.SessionID != "chat_100" {
		t.Errorf("SessionID = %q, want chat_100", got.SessionID)
	}
	if len(got.Facts) != 2 {
		t.Errorf("Facts len = %d, want 2", len(got.Facts))
	}
	if len(got.Keywords) != 3 {
		t.Errorf("Keywords len = %d, want 3", len(got.Keywords))
	}
}

func TestMemoryStoreSearch(t *testing.T) {
	db := testDB(t)
	store := NewMemoryStore(db)

	// Insert entries with different keywords
	entries := []memory.Entry{
		{SessionID: "s1", Facts: []string{}, Keywords: []string{"golang", "http", "server"}, Summary: "Building an HTTP server in Go."},
		{SessionID: "s2", Facts: []string{}, Keywords: []string{"python", "django", "web"}, Summary: "Django web framework tutorial."},
		{SessionID: "s3", Facts: []string{}, Keywords: []string{"golang", "sqlite", "database"}, Summary: "Using SQLite with Go."},
	}
	for _, e := range entries {
		store.Append(e)
	}

	// Search for golang
	results, err := store.Search("golang database", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(results) == 0 {
		t.Fatal("Search returned 0 results, want at least 1")
	}

	// Should find golang-related entries
	found := false
	for _, r := range results {
		for _, k := range r.Keywords {
			if k == "golang" {
				found = true
			}
		}
	}
	if !found {
		t.Error("Search did not return golang-related entries")
	}
}

func TestMemoryStoreSearchFallback(t *testing.T) {
	db := testDB(t)
	store := NewMemoryStore(db)

	store.Append(memory.Entry{
		SessionID: "s1",
		Keywords:  []string{"test"},
		Facts:     []string{"path: /home/user/project"},
		Summary:   "Working on a project",
	})

	// Short query that might not have FTS tokens
	results, err := store.Search("ab", 5)
	if err != nil {
		t.Fatalf("Search short query: %v", err)
	}
	// Should return empty (no 3+ char tokens) — not an error
	_ = results
}

// --- Message Store Tests ---

func TestMessageStoreRoundtrip(t *testing.T) {
	db := testDB(t)
	sessStore := NewSessionStore(db)
	msgStore := NewMessageStore(db)

	// Need a session first (foreign key)
	sessions := map[int64]*session.Session{
		100: session.NewFromDB("chat_100", 100, time.Now(), time.Now(), "", 0, 0, 0, ""),
	}
	sessStore.Save(sessions)

	// Append messages
	msgs := []agent.Message{
		{Role: "user", Content: "Hello, how are you?"},
		{Role: "assistant", Content: "I'm doing well, thanks!"},
		{Role: "user", Content: "Can you help me with Go?", ToolCalls: []agent.ToolCall{
			{ID: "tc1", Name: "read_file", Input: json.RawMessage(`{"path":"/tmp/test.go"}`)},
		}},
	}

	if err := msgStore.AppendBatch("chat_100", msgs); err != nil {
		t.Fatalf("AppendBatch: %v", err)
	}

	// Load all
	loaded, err := msgStore.LoadHistory("chat_100", 0)
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}
	if len(loaded) != 3 {
		t.Fatalf("loaded %d messages, want 3", len(loaded))
	}

	if loaded[0].Role != "user" || loaded[0].Content != "Hello, how are you?" {
		t.Errorf("msg[0] = %+v", loaded[0])
	}
	if loaded[2].Role != "user" || len(loaded[2].ToolCalls) != 1 {
		t.Errorf("msg[2] tool calls = %+v", loaded[2].ToolCalls)
	}
}

func TestMessageStoreLimit(t *testing.T) {
	db := testDB(t)
	sessStore := NewSessionStore(db)
	msgStore := NewMessageStore(db)

	sessions := map[int64]*session.Session{
		100: session.NewFromDB("chat_100", 100, time.Now(), time.Now(), "", 0, 0, 0, ""),
	}
	sessStore.Save(sessions)

	// Insert 10 messages
	for i := 0; i < 10; i++ {
		msgStore.Append("chat_100", agent.Message{Role: "user", Content: "msg"})
	}

	// Load last 3
	loaded, err := msgStore.LoadHistory("chat_100", 3)
	if err != nil {
		t.Fatalf("LoadHistory limit: %v", err)
	}
	if len(loaded) != 3 {
		t.Errorf("loaded %d, want 3", len(loaded))
	}
}

func TestMessageStoreDeleteBySession(t *testing.T) {
	db := testDB(t)
	sessStore := NewSessionStore(db)
	msgStore := NewMessageStore(db)

	sessions := map[int64]*session.Session{
		100: session.NewFromDB("chat_100", 100, time.Now(), time.Now(), "", 0, 0, 0, ""),
	}
	sessStore.Save(sessions)

	msgStore.Append("chat_100", agent.Message{Role: "user", Content: "hello"})
	msgStore.DeleteBySession("chat_100")

	count, _ := msgStore.Count("chat_100")
	if count != 0 {
		t.Errorf("count after delete = %d, want 0", count)
	}
}

// --- Migration Tests ---

func TestMigrateFromJSON(t *testing.T) {
	dir := t.TempDir()

	// Create legacy sessions.json
	sessData := map[string]*legacySession{
		"chat_100": {
			ID: "chat_100", ChatID: 100,
			CreatedAt: time.Now(), UpdatedAt: time.Now(),
			ClaudeSessionID: "sess_abc", MessageCount: 3,
			InputTokens: 500, OutputTokens: 200,
		},
	}
	data, _ := json.MarshalIndent(sessData, "", "  ")
	os.WriteFile(filepath.Join(dir, "sessions.json"), data, 0644)

	// Create legacy memory.jsonl
	memDir := filepath.Join(dir, "memory")
	os.MkdirAll(memDir, 0755)
	memEntry := legacyMemoryEntry{
		ID: "mem_1", CreatedAt: time.Now(),
		SessionID: "chat_100", ChatID: 100,
		Facts: []string{"path: /tmp"}, Keywords: []string{"test"},
		Summary: "test summary",
	}
	memData, _ := json.Marshal(memEntry)
	os.WriteFile(filepath.Join(memDir, "memory.jsonl"), append(memData, '\n'), 0644)

	// Open DB — should auto-import
	db, err := Open(filepath.Join(dir, "goterm.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// Verify sessions imported
	sessStore := NewSessionStore(db)
	sessions, err := sessStore.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("imported %d sessions, want 1", len(sessions))
	}
	if sessions[100].GetSessionID() != "sess_abc" {
		t.Errorf("ClaudeSessionID = %q, want sess_abc", sessions[100].GetSessionID())
	}

	// Verify memory imported
	memStore := NewMemoryStore(db)
	entries, err := memStore.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("imported %d memory entries, want 1", len(entries))
	}
	if entries[0].Summary != "test summary" {
		t.Errorf("Summary = %q, want 'test summary'", entries[0].Summary)
	}
}

func TestOpenIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")

	// Open twice — second open should not fail or re-create
	db1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	db1.Close()

	db2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer db2.Close()

	ver, _ := db2.currentVersion()
	if ver != schemaVersion {
		t.Errorf("version after reopen = %d, want %d", ver, schemaVersion)
	}
}
