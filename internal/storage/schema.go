package storage

import "fmt"

const schemaVersion = 2

// DDL statements executed in order.
var ddl = []string{
	`CREATE TABLE IF NOT EXISTS meta (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`,

	`CREATE TABLE IF NOT EXISTS sessions (
		id                TEXT PRIMARY KEY,
		chat_id           INTEGER UNIQUE NOT NULL,
		created_at        TEXT NOT NULL,
		updated_at        TEXT NOT NULL,
		claude_session_id TEXT DEFAULT '',
		message_count     INTEGER DEFAULT 0,
		input_tokens      INTEGER DEFAULT 0,
		output_tokens     INTEGER DEFAULT 0,
		compact_summary   TEXT DEFAULT ''
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

	`CREATE TABLE IF NOT EXISTS memory (
		id         TEXT PRIMARY KEY,
		created_at TEXT NOT NULL,
		session_id TEXT NOT NULL,
		chat_id    INTEGER DEFAULT 0,
		facts      TEXT NOT NULL,
		keywords   TEXT NOT NULL,
		summary    TEXT DEFAULT '',
		intent     TEXT DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS idx_memory_chat ON memory(chat_id, created_at DESC)`,

	`CREATE VIRTUAL TABLE IF NOT EXISTS memory_fts USING fts5(
		keywords, facts, summary, intent, content=memory, content_rowid=rowid
	)`,

	// FTS sync triggers
	`CREATE TRIGGER IF NOT EXISTS memory_ai AFTER INSERT ON memory BEGIN
		INSERT INTO memory_fts(rowid, keywords, facts, summary, intent)
		VALUES (new.rowid, new.keywords, new.facts, new.summary, new.intent);
	END`,

	`CREATE TRIGGER IF NOT EXISTS memory_ad AFTER DELETE ON memory BEGIN
		INSERT INTO memory_fts(memory_fts, rowid, keywords, facts, summary, intent)
		VALUES ('delete', old.rowid, old.keywords, old.facts, old.summary, old.intent);
	END`,
}

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
		if err := db.migrateV2AddIntent(); err != nil {
			return fmt.Errorf("migrate v2: %w", err)
		}
	}

	if ver < schemaVersion {
		if err := db.createTables(); err != nil {
			return err
		}
		return db.setVersion(schemaVersion)
	}
	return nil
}

// migrateV2AddIntent adds the intent column to memory table and rebuilds FTS.
func (db *DB) migrateV2AddIntent() error {
	// Add column (SQLite ignores if column already exists via error)
	_, _ = db.conn.Exec(`ALTER TABLE memory ADD COLUMN intent TEXT DEFAULT ''`)

	// Rebuild FTS table and triggers to include intent
	stmts := []string{
		`DROP TRIGGER IF EXISTS memory_ai`,
		`DROP TRIGGER IF EXISTS memory_ad`,
		`DROP TABLE IF EXISTS memory_fts`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS memory_fts USING fts5(
			keywords, facts, summary, intent, content=memory, content_rowid=rowid
		)`,
		`CREATE TRIGGER IF NOT EXISTS memory_ai AFTER INSERT ON memory BEGIN
			INSERT INTO memory_fts(rowid, keywords, facts, summary, intent)
			VALUES (new.rowid, new.keywords, new.facts, new.summary, new.intent);
		END`,
		`CREATE TRIGGER IF NOT EXISTS memory_ad AFTER DELETE ON memory BEGIN
			INSERT INTO memory_fts(memory_fts, rowid, keywords, facts, summary, intent)
			VALUES ('delete', old.rowid, old.keywords, old.facts, old.summary, old.intent);
		END`,
		// Rebuild FTS content from existing data
		`INSERT INTO memory_fts(rowid, keywords, facts, summary, intent)
			SELECT rowid, keywords, facts, summary, intent FROM memory`,
	}
	for _, stmt := range stmts {
		if _, err := db.conn.Exec(stmt); err != nil {
			return fmt.Errorf("migrate v2 DDL: %w\nstatement: %s", err, stmt)
		}
	}
	return nil
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
