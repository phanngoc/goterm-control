package storage

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// seedV5 builds a v5 database by hand: a dashboard chat (1) with its single
// session, plus the given Telegram chats, each with two sessions of which the
// second is active. Returns the path.
func seedV5(t *testing.T, telegramChats ...int64) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "v5.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	defer raw.Close()

	stmts := []string{
		`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`INSERT INTO meta(key, value) VALUES('schema_version', '5')`,
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
			seq               INTEGER DEFAULT 0,
			memory_flushed    INTEGER DEFAULT 0,
			provider          TEXT DEFAULT 'claude'
		)`,
		`CREATE TABLE chat_state (
			chat_id           INTEGER PRIMARY KEY,
			active_session_id TEXT NOT NULL,
			next_seq          INTEGER DEFAULT 1
		)`,
		`CREATE TABLE messages (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id   TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
			role         TEXT NOT NULL,
			content      TEXT NOT NULL,
			tool_calls   TEXT DEFAULT '',
			tool_results TEXT DEFAULT '',
			tokens       INTEGER DEFAULT 0,
			created_at   TEXT NOT NULL
		)`,
		`CREATE TABLE users (id INTEGER PRIMARY KEY AUTOINCREMENT, username TEXT UNIQUE NOT NULL,
			password_hash TEXT NOT NULL, role TEXT DEFAULT 'admin', created_at TEXT NOT NULL)`,
		`CREATE TABLE web_sessions (token_hash TEXT PRIMARY KEY, user_id INTEGER NOT NULL,
			expires_at TEXT NOT NULL, created_at TEXT NOT NULL)`,
		// the dashboard's lone session, with one message
		`INSERT INTO sessions (id, chat_id, created_at, updated_at, message_count, label)
			VALUES ('chat_1', 1, '2026-09-01T10:00:00Z', '2026-09-01T10:00:00Z', 3, 'web chat')`,
		`INSERT INTO chat_state VALUES (1, 'chat_1', 1)`,
		`INSERT INTO messages (session_id, role, content, created_at)
			VALUES ('chat_1', 'user', 'hello from the web', '2026-09-01T10:00:00Z')`,
	}
	for _, s := range stmts {
		if _, err := raw.Exec(s); err != nil {
			t.Fatalf("seed v5: %v\n%s", err, s)
		}
	}
	for _, chat := range telegramChats {
		for _, s := range []string{
			`INSERT INTO sessions (id, chat_id, created_at, updated_at, message_count, seq)
				VALUES ('chat_%d_1', %d, '2026-09-02T10:00:00Z', '2026-09-02T10:00:00Z', 5, 1)`,
			`INSERT INTO sessions (id, chat_id, created_at, updated_at, message_count, seq)
				VALUES ('chat_%d_2', %d, '2026-09-03T10:00:00Z', '2026-09-03T10:00:00Z', 2, 2)`,
			`INSERT INTO chat_state VALUES (%d, 'chat_%d_2', 3)`,
		} {
			if _, err := raw.Exec(sprintf2(s, chat)); err != nil {
				t.Fatalf("seed telegram %d: %v", chat, err)
			}
		}
	}
	return path
}

func sprintf2(format string, n int64) string {
	// every placeholder in the seed statements is the same chat id
	out := format
	for i := 0; i < 4; i++ {
		out = replaceFirst(out, "%d", itoa(n))
	}
	return out
}

func replaceFirst(s, old, new string) string {
	for i := 0; i+len(old) <= len(s); i++ {
		if s[i:i+len(old)] == old {
			return s[:i] + new + s[i+len(old):]
		}
	}
	return s
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func count(t *testing.T, db *DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.Conn().QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// TestMigrateV5ToV6MergesDashboardIntoTheOnlyTelegramChat covers the
// single-user case this whole migration exists for: one person, one Telegram
// chat, one dashboard — two unrelated histories until now.
func TestMigrateV5ToV6MergesDashboardIntoTheOnlyTelegramChat(t *testing.T) {
	db, err := Open(seedV5(t, 555))
	if err != nil {
		t.Fatalf("Open (migrate): %v", err)
	}
	defer db.Close()

	conv := NewConversationStore(db)

	// Bindings: telegram to itself, the web account into the Telegram chat.
	if id, ok := conv.Resolve(ChannelTelegram, "555"); !ok || id != 555 {
		t.Errorf("telegram binding = %d,%v; want 555", id, ok)
	}
	if id, ok := conv.Resolve(ChannelWeb, WebAccountID); !ok || id != 555 {
		t.Errorf("web binding = %d,%v; want 555 (merged)", id, ok)
	}

	// The dashboard's session moved under the Telegram chat; nothing was lost.
	if n := count(t, db, `SELECT count(*) FROM sessions WHERE chat_id = 555`); n != 3 {
		t.Errorf("sessions under 555 = %d, want 3 (two telegram + the moved web one)", n)
	}
	if n := count(t, db, `SELECT count(*) FROM sessions WHERE chat_id = 1`); n != 0 {
		t.Errorf("sessions left under the dashboard chat = %d, want 0", n)
	}
	if n := count(t, db, `SELECT count(*) FROM messages WHERE session_id = 'chat_1'`); n != 1 {
		t.Errorf("the web session's messages = %d, want 1 (moving a session must keep its rows)", n)
	}

	// The Telegram side stays active — a merge must not yank a live
	// conversation onto the dashboard's last session.
	var active string
	if err := db.Conn().QueryRow(`SELECT active_session_id FROM chat_state WHERE chat_id = 555`).Scan(&active); err != nil {
		t.Fatalf("chat_state 555: %v", err)
	}
	if active != "chat_555_2" {
		t.Errorf("active session = %q, want chat_555_2", active)
	}
	if n := count(t, db, `SELECT count(*) FROM chat_state WHERE chat_id = 1`); n != 0 {
		t.Errorf("dashboard chat_state survived the merge")
	}
	if n := count(t, db, `SELECT count(*) FROM conversations`); n != 1 {
		t.Errorf("conversations = %d, want just the merged one", n)
	}
}

// With several Telegram chats there is no safe default; the dashboard keeps its
// own conversation and nothing moves. Choosing is a person's job.
func TestMigrateV5ToV6KeepsDashboardApartWhenSeveralTelegramChats(t *testing.T) {
	db, err := Open(seedV5(t, 555, 777))
	if err != nil {
		t.Fatalf("Open (migrate): %v", err)
	}
	defer db.Close()

	conv := NewConversationStore(db)
	if id, ok := conv.Resolve(ChannelWeb, WebAccountID); !ok || id != DashboardConversationID {
		t.Errorf("web binding = %d,%v; want its own conversation %d", id, ok, DashboardConversationID)
	}
	if n := count(t, db, `SELECT count(*) FROM sessions WHERE chat_id = 1`); n != 1 {
		t.Errorf("dashboard sessions = %d, want 1 (untouched)", n)
	}
	if n := count(t, db, `SELECT count(*) FROM conversations`); n != 3 {
		t.Errorf("conversations = %d, want 3", n)
	}
}

// A fresh install has no Telegram chat yet. The web account gets its own
// conversation, and when the first Telegram chat appears later it is NOT merged
// retroactively by ResolveWeb — the binding already exists and is honoured.
func TestResolveWebBindsOnceAndHonoursIt(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	conv := NewConversationStore(db)
	if got := conv.ResolveWeb(); got != DashboardConversationID {
		t.Fatalf("fresh install: web conversation = %d, want %d", got, DashboardConversationID)
	}

	// A Telegram chat shows up afterwards.
	conv.EnsureTelegramBindings([]int64{4242})
	if got := conv.ResolveWeb(); got != DashboardConversationID {
		t.Errorf("an existing web binding was silently re-pointed to %d", got)
	}

	// But an explicit rebind is honoured.
	if err := conv.Bind(ChannelWeb, WebAccountID, 4242); err != nil {
		t.Fatalf("rebind: %v", err)
	}
	if got := conv.ResolveWeb(); got != 4242 {
		t.Errorf("after explicit bind, web conversation = %d, want 4242", got)
	}
}

// The runtime rule mirrors the migration: with exactly one Telegram
// conversation known, an UNBOUND web account joins it.
func TestResolveWebJoinsTheOnlyTelegramConversation(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "one.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	conv := NewConversationStore(db)
	conv.EnsureTelegramBindings([]int64{4242})
	if got := conv.ResolveWeb(); got != 4242 {
		t.Errorf("web conversation = %d, want the sole Telegram conversation 4242", got)
	}
}
