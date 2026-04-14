package storage

import "fmt"

const schemaVersion = 1

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

	if ver < schemaVersion {
		if err := db.createTables(); err != nil {
			return err
		}
		return db.setVersion(schemaVersion)
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
