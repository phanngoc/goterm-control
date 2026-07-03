package storage

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ngocp/goterm-control/internal/agent"
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
	tables := []string{"meta", "sessions", "messages", "chat_state"}
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

// helper to wrap a single session into ChatState map for Save().
func chatStates(sessions ...*session.Session) map[int64]*session.ChatState {
	chats := make(map[int64]*session.ChatState)
	for _, s := range sessions {
		cs, ok := chats[s.ChatID]
		if !ok {
			cs = &session.ChatState{
				ActiveSessionID: s.ID,
				NextSeq:         1,
				Sessions:        make(map[string]*session.Session),
			}
			chats[s.ChatID] = cs
		}
		cs.Sessions[s.ID] = s
	}
	return chats
}

func TestSessionStoreRoundtrip(t *testing.T) {
	db := testDB(t)
	store := NewSessionStore(db)

	s1 := session.NewFromDB("chat_100", 100, time.Now(), time.Now(), "claude_abc", 5, 1000, 500, "summary text", "First chat", 0, false)
	s2 := session.NewFromDB("chat_200", 200, time.Now(), time.Now(), "", 0, 0, 0, "", "", 0, false)

	if err := store.Save(chatStates(s1, s2)); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(loaded) != 2 {
		t.Fatalf("loaded %d chats, want 2", len(loaded))
	}

	cs := loaded[100]
	s := cs.Sessions["chat_100"]
	if s == nil {
		t.Fatal("session chat_100 not found")
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
	if s.GetLabel() != "First chat" {
		t.Errorf("Label = %q, want 'First chat'", s.GetLabel())
	}
}

func TestSessionStoreUpsert(t *testing.T) {
	db := testDB(t)
	store := NewSessionStore(db)

	s := session.NewFromDB("chat_100", 100, time.Now(), time.Now(), "v1", 1, 100, 50, "", "", 0, false)
	store.Save(chatStates(s))

	// Update
	s.SetSessionID("v2")
	s.IncrementMessages()
	store.Save(chatStates(s))

	loaded, _ := store.Load()
	cs := loaded[100]
	ls := cs.Sessions["chat_100"]
	if ls.GetSessionID() != "v2" {
		t.Errorf("updated ClaudeSessionID = %q, want v2", ls.GetSessionID())
	}
	if ls.GetMessageCount() != 2 {
		t.Errorf("updated MessageCount = %d, want 2", ls.GetMessageCount())
	}
}

func TestSessionStoreMultipleSessionsPerChat(t *testing.T) {
	db := testDB(t)
	store := NewSessionStore(db)

	s1 := session.NewFromDB("chat_100", 100, time.Now(), time.Now(), "claude_1", 5, 100, 50, "", "Session 1", 0, false)
	s2 := session.NewFromDB("chat_100_1", 100, time.Now(), time.Now(), "claude_2", 3, 200, 80, "", "Session 2", 1, false)

	chats := map[int64]*session.ChatState{
		100: {
			ActiveSessionID: "chat_100_1",
			NextSeq:         2,
			Sessions:        map[string]*session.Session{"chat_100": s1, "chat_100_1": s2},
		},
	}

	if err := store.Save(chats); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	cs := loaded[100]
	if cs == nil {
		t.Fatal("chat 100 not found")
	}
	if len(cs.Sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(cs.Sessions))
	}
	if cs.ActiveSessionID != "chat_100_1" {
		t.Errorf("ActiveSessionID = %q, want chat_100_1", cs.ActiveSessionID)
	}
	if cs.NextSeq != 2 {
		t.Errorf("NextSeq = %d, want 2", cs.NextSeq)
	}
}

// --- Message Store Tests ---

func TestMessageStoreRoundtrip(t *testing.T) {
	db := testDB(t)
	sessStore := NewSessionStore(db)
	msgStore := NewMessageStore(db)

	s := session.NewFromDB("chat_100", 100, time.Now(), time.Now(), "", 0, 0, 0, "", "", 0, false)
	sessStore.Save(chatStates(s))

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

	s := session.NewFromDB("chat_100", 100, time.Now(), time.Now(), "", 0, 0, 0, "", "", 0, false)
	sessStore.Save(chatStates(s))

	for i := 0; i < 10; i++ {
		msgStore.Append("chat_100", agent.Message{Role: "user", Content: "msg"})
	}

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

	s := session.NewFromDB("chat_100", 100, time.Now(), time.Now(), "", 0, 0, 0, "", "", 0, false)
	sessStore.Save(chatStates(s))

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

	// Open DB — should auto-import
	db, err := Open(filepath.Join(dir, "goterm.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// Verify sessions imported
	sessStore := NewSessionStore(db)
	chats, err := sessStore.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(chats) != 1 {
		t.Fatalf("imported %d chats, want 1", len(chats))
	}
	cs := chats[100]
	s := cs.Sessions[cs.ActiveSessionID]
	if s.GetSessionID() != "sess_abc" {
		t.Errorf("ClaudeSessionID = %q, want sess_abc", s.GetSessionID())
	}
}

func TestOpenIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")

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

func TestMigrateV2ToV3(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")

	// Build a v2 database by hand (no memory_flushed column, version 2).
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	stmts := []string{
		`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`INSERT INTO meta(key, value) VALUES('schema_version', '2')`,
		`CREATE TABLE sessions (
			id                TEXT PRIMARY KEY,
			chat_id           INTEGER NOT NULL,
			created_at        TEXT NOT NULL,
			updated_at        TEXT NOT NULL,
			claude_session_id TEXT DEFAULT '',
			message_count     INTEGER DEFAULT 0,
			input_tokens      INTEGER DEFAULT 0,
			output_tokens     INTEGER DEFAULT 0,
			compact_summary   TEXT DEFAULT '',
			label             TEXT DEFAULT '',
			seq               INTEGER DEFAULT 0
		)`,
		`CREATE TABLE chat_state (
			chat_id           INTEGER PRIMARY KEY,
			active_session_id TEXT NOT NULL,
			next_seq          INTEGER DEFAULT 1
		)`,
		`CREATE TABLE messages (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id   TEXT NOT NULL,
			role         TEXT NOT NULL,
			content      TEXT NOT NULL,
			tool_calls   TEXT DEFAULT '',
			tool_results TEXT DEFAULT '',
			tokens       INTEGER DEFAULT 0,
			created_at   TEXT NOT NULL
		)`,
		`INSERT INTO sessions (id, chat_id, created_at, updated_at, claude_session_id, message_count)
			VALUES ('chat_100', 100, '2026-07-01T10:00:00Z', '2026-07-01T10:00:00Z', 'claude_v2', 4)`,
		`INSERT INTO chat_state (chat_id, active_session_id, next_seq) VALUES (100, 'chat_100', 1)`,
	}
	for _, s := range stmts {
		if _, err := raw.Exec(s); err != nil {
			t.Fatalf("seed v2 db: %v\n%s", err, s)
		}
	}
	raw.Close()

	// Open runs the migration.
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open (migrate): %v", err)
	}
	defer db.Close()

	ver, _ := db.currentVersion()
	if ver != 3 {
		t.Errorf("version = %d, want 3", ver)
	}

	// Existing row must load with memory_flushed defaulting to false.
	loaded, err := NewSessionStore(db).Load()
	if err != nil {
		t.Fatalf("Load after migrate: %v", err)
	}
	s := loaded[100].Sessions["chat_100"]
	if s == nil {
		t.Fatal("session chat_100 not found after migration")
	}
	if s.GetSessionID() != "claude_v2" || s.GetMessageCount() != 4 {
		t.Errorf("migrated session data lost: id=%q count=%d", s.GetSessionID(), s.GetMessageCount())
	}
	if s.IsMemoryFlushed() {
		t.Error("memory_flushed should default to false after migration")
	}

	// Round-trip the new flag through Save/Load.
	s.MarkMemoryFlushed()
	if err := NewSessionStore(db).Save(loaded); err != nil {
		t.Fatalf("Save after migrate: %v", err)
	}
	reloaded, _ := NewSessionStore(db).Load()
	if !reloaded[100].Sessions["chat_100"].IsMemoryFlushed() {
		t.Error("memory_flushed flag did not persist")
	}
}
