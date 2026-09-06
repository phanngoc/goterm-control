package storage

import (
	"fmt"
	"log"
	"time"
)

const schemaVersion = 7

// conversationDDL is shared by fresh installs (via ddl) and the v5→v6
// migration, so the two can never drift apart.
var conversationDDL = []string{
	// A conversation is keyed by the same integer the session manager uses for
	// a chat (sessions.chat_id). Two channels bound to one key share the active
	// session, the history and the execution lane — that is the whole
	// mechanism behind "the dashboard shows the Telegram conversation".
	`CREATE TABLE IF NOT EXISTS conversations (
		id         INTEGER PRIMARY KEY,
		title      TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS channel_bindings (
		channel         TEXT NOT NULL,   -- 'telegram' | 'web'
		external_id     TEXT NOT NULL,   -- telegram chat id, or the web account id
		conversation_id INTEGER NOT NULL REFERENCES conversations(id),
		created_at      TEXT NOT NULL,
		PRIMARY KEY (channel, external_id)
	)`,
}

// DDL statements executed in order for fresh installs.
var ddl = append([]string{
	`CREATE TABLE IF NOT EXISTS meta (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`,

	`CREATE TABLE IF NOT EXISTS sessions (
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
		provider          TEXT DEFAULT 'claude',
		account           TEXT DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS idx_sessions_chat ON sessions(chat_id)`,

	`CREATE TABLE IF NOT EXISTS chat_state (
		chat_id           INTEGER PRIMARY KEY,
		active_session_id TEXT NOT NULL,
		next_seq          INTEGER DEFAULT 1
	)`,

	`CREATE TABLE IF NOT EXISTS messages (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id   TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
		role         TEXT NOT NULL,
		content      TEXT NOT NULL,
		tool_calls   TEXT DEFAULT '',
		tool_results TEXT DEFAULT '',
		tokens       INTEGER DEFAULT 0,
		created_at   TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id, id)`,

	`CREATE TABLE IF NOT EXISTS users (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		username      TEXT UNIQUE NOT NULL,
		password_hash TEXT NOT NULL,
		role          TEXT DEFAULT 'admin',
		created_at    TEXT NOT NULL
	)`,

	`CREATE TABLE IF NOT EXISTS web_sessions (
		token_hash TEXT PRIMARY KEY,
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		expires_at TEXT NOT NULL,
		created_at TEXT NOT NULL
	)`,
}, conversationDDL...)

// migrate creates tables and imports legacy data if needed.
func (db *DB) migrate() error {
	ver, err := db.currentVersion()
	if err != nil {
		// meta table likely doesn't exist yet — create everything
		if err := db.createTables(); err != nil {
			return err
		}
		if err := db.setVersion(schemaVersion); err != nil {
			return err
		}
		// Attempt to import legacy JSON/JSONL data
		return db.migrateFromLegacy()
	}

	if ver < 2 {
		if err := db.migrateV1ToV2(); err != nil {
			return fmt.Errorf("migrate v1→v2: %w", err)
		}
		if err := db.setVersion(2); err != nil {
			return err
		}
		ver = 2
	}
	if ver < 3 {
		if err := db.migrateV2ToV3(); err != nil {
			return fmt.Errorf("migrate v2→v3: %w", err)
		}
		if err := db.setVersion(3); err != nil {
			return err
		}
		ver = 3
	}
	if ver < 4 {
		if err := db.migrateV3ToV4(); err != nil {
			return fmt.Errorf("migrate v3→v4: %w", err)
		}
		if err := db.setVersion(4); err != nil {
			return err
		}
		ver = 4
	}
	if ver < 5 {
		if err := db.migrateV4ToV5(); err != nil {
			return fmt.Errorf("migrate v4→v5: %w", err)
		}
		if err := db.setVersion(5); err != nil {
			return err
		}
		ver = 5
	}
	if ver < 6 {
		if err := db.migrateV5ToV6(); err != nil {
			return fmt.Errorf("migrate v5→v6: %w", err)
		}
		if err := db.setVersion(6); err != nil {
			return err
		}
		ver = 6
	}
	if ver < 7 {
		if err := db.migrateV6ToV7(); err != nil {
			return fmt.Errorf("migrate v6→v7: %w", err)
		}
		return db.setVersion(7)
	}
	return nil
}

// migrateV6ToV7 adds the account column: which credential in the pool a
// session runs under. Both CLIs keep their session store inside the same
// directory as their credentials, so a session cannot be read from any other
// account — the pin is what sends every later turn back to the right one.
//
// Existing rows stay empty, meaning "the ambient credentials", which is what
// they were started on.
func (db *DB) migrateV6ToV7() error {
	_, err := db.conn.Exec(`ALTER TABLE sessions ADD COLUMN account TEXT DEFAULT ''`)
	return err
}

// migrateV5ToV6 introduces conversations and channel bindings, and folds the
// dashboard's private chat into the Telegram conversation.
//
// Until now the dashboard talked to a hardcoded chat (id 1) and Telegram to its
// own chat id, so the same person had two unrelated histories. Every existing
// chat becomes a conversation; every non-dashboard chat is a Telegram chat and
// binds to itself. Then the single-user rule: with exactly one Telegram
// conversation, the dashboard chat is merged into it — its sessions move over,
// its chat_state goes, and the Telegram side's active session stays active so
// a live conversation is not yanked onto the dashboard's last session. With
// zero or several Telegram conversations the dashboard keeps its own; picking
// one of several people's chats for it is a decision, not a migration.
func (db *DB) migrateV5ToV6() error {
	for _, stmt := range conversationDDL {
		if _, err := db.conn.Exec(stmt); err != nil {
			return err
		}
	}

	tx, err := db.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.Query(`SELECT DISTINCT chat_id FROM sessions
		UNION SELECT chat_id FROM chat_state ORDER BY 1`)
	if err != nil {
		return fmt.Errorf("list chats: %w", err)
	}
	var chats []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		chats = append(chats, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	hasDashboard := false
	var telegram []int64
	for _, id := range chats {
		if _, err := tx.Exec(`INSERT INTO conversations (id, title, created_at, updated_at)
			VALUES (?, '', ?, ?) ON CONFLICT(id) DO NOTHING`, id, now, now); err != nil {
			return fmt.Errorf("conversation %d: %w", id, err)
		}
		if id == DashboardConversationID {
			hasDashboard = true
			continue
		}
		telegram = append(telegram, id)
		if _, err := tx.Exec(`INSERT INTO channel_bindings (channel, external_id, conversation_id, created_at)
			VALUES (?, ?, ?, ?) ON CONFLICT(channel, external_id) DO NOTHING`,
			ChannelTelegram, fmt.Sprint(id), id, now); err != nil {
			return fmt.Errorf("bind telegram %d: %w", id, err)
		}
	}

	if hasDashboard {
		webTarget := DashboardConversationID
		if len(telegram) == 1 {
			webTarget = telegram[0]
			if err := mergeChats(tx, DashboardConversationID, webTarget); err != nil {
				return err
			}
			log.Printf("storage: merged the dashboard chat into Telegram conversation %d", webTarget)
		} else if len(telegram) > 1 {
			log.Printf("storage: %d Telegram conversations — dashboard keeps its own chat; bind web:%s to one explicitly to merge",
				len(telegram), WebAccountID)
		}
		if _, err := tx.Exec(`INSERT INTO channel_bindings (channel, external_id, conversation_id, created_at)
			VALUES (?, ?, ?, ?) ON CONFLICT(channel, external_id) DO NOTHING`,
			ChannelWeb, WebAccountID, webTarget, now); err != nil {
			return fmt.Errorf("bind web: %w", err)
		}
	}
	return tx.Commit()
}

// migrateV4ToV5 adds the provider column. It records which CLI produced
// claude_session_id, so switching an agent between the claude and codex
// backends starts a fresh CLI session instead of resuming with the wrong one.
// Existing rows were all produced by the claude CLI.
func (db *DB) migrateV4ToV5() error {
	_, err := db.conn.Exec(`ALTER TABLE sessions ADD COLUMN provider TEXT DEFAULT 'claude'`)
	return err
}

// migrateV3ToV4 adds the users and web_sessions tables for dashboard auth.
func (db *DB) migrateV3ToV4() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			username      TEXT UNIQUE NOT NULL,
			password_hash TEXT NOT NULL,
			role          TEXT DEFAULT 'admin',
			created_at    TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS web_sessions (
			token_hash TEXT PRIMARY KEY,
			user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			expires_at TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
	}
	for _, stmt := range stmts {
		if _, err := db.conn.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

// migrateV2ToV3 adds the memory_flushed column used by the token-threshold
// memory flush (fires at most once per session).
func (db *DB) migrateV2ToV3() error {
	_, err := db.conn.Exec(`ALTER TABLE sessions ADD COLUMN memory_flushed INTEGER DEFAULT 0`)
	return err
}

// migrateV1ToV2 removes the UNIQUE constraint on chat_id, adds label/seq
// columns, and creates the chat_state table.
func (db *DB) migrateV1ToV2() error {
	tx, err := db.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	stmts := []string{
		// Recreate sessions table without UNIQUE on chat_id, with new columns.
		`CREATE TABLE IF NOT EXISTS sessions_new (
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
		`INSERT INTO sessions_new
			SELECT id, chat_id, created_at, updated_at, claude_session_id,
			       message_count, input_tokens, output_tokens, compact_summary,
			       '', 0
			FROM sessions`,
		`DROP TABLE sessions`,
		`ALTER TABLE sessions_new RENAME TO sessions`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_chat ON sessions(chat_id)`,

		// Create chat_state table.
		`CREATE TABLE IF NOT EXISTS chat_state (
			chat_id           INTEGER PRIMARY KEY,
			active_session_id TEXT NOT NULL,
			next_seq          INTEGER DEFAULT 1
		)`,

		// Populate chat_state from existing sessions.
		`INSERT OR IGNORE INTO chat_state (chat_id, active_session_id, next_seq)
			SELECT chat_id, id, 1 FROM sessions`,
	}

	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("exec: %w\nstatement: %s", err, stmt)
		}
	}
	return tx.Commit()
}

func (db *DB) createTables() error {
	tx, err := db.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	for _, stmt := range ddl {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("exec DDL: %w\nstatement: %s", err, stmt)
		}
	}
	return tx.Commit()
}

func (db *DB) currentVersion() (int, error) {
	var val string
	err := db.conn.QueryRow(`SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&val)
	if err != nil {
		return 0, err
	}
	var v int
	fmt.Sscanf(val, "%d", &v)
	return v, nil
}

func (db *DB) setVersion(v int) error {
	_, err := db.conn.Exec(
		`INSERT INTO meta(key, value) VALUES('schema_version', ?) ON CONFLICT(key) DO UPDATE SET value = ?`,
		fmt.Sprintf("%d", v), fmt.Sprintf("%d", v),
	)
	return err
}
