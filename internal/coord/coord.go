// Package coord is the coordination layer shared by every agent on this
// machine: one SQLite file that all gateways open at once.
//
// Why a second database instead of the per-agent one: session and message
// tables carry the high write volume of a live conversation, and the design
// (docs/design/shared-agent-memory.md) gates merging those behind a
// concurrency measurement. Coordination data — traces, tasks, inter-agent
// messages, heartbeats — is low volume and useless unless it is shared, so it
// lives here and every agent writes to the same file from its own process.
//
// The run/trace model follows LangSmith: a trace is a tree of runs joined by
// trace_id + parent_run_id, ordered by a sortable dotted_order key so the whole
// tree comes back correctly nested from a single indexed query.
package coord

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ngocp/goterm-control/internal/storage"

	_ "modernc.org/sqlite"
)

const schemaVersion = 2

// DB is the shared coordination database.
type DB struct {
	conn *sql.DB
	path string
}

// DefaultPath is where the shared database lives when config says nothing.
// It sits outside every agent's own data dir on purpose — no agent owns it.
func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".goterm-shared", "data", "coord.db")
}

// Open opens (or creates) the shared database and applies migrations.
// Safe to call from several processes at once: the schema statements are all
// IF NOT EXISTS, and the pragmas (WAL + a real busy timeout on every pooled
// connection) come from storage.DSN.
func Open(path string) (*DB, error) {
	if path == "" {
		path = DefaultPath()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("coord: mkdir %s: %w", filepath.Dir(path), err)
	}

	conn, err := sql.Open("sqlite", storage.DSN(path))
	if err != nil {
		return nil, fmt.Errorf("coord: open %s: %w", path, err)
	}

	db := &DB{conn: conn, path: path}
	if err := db.migrate(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("coord: migrate: %w", err)
	}
	return db, nil
}

// Close releases the database handle.
func (db *DB) Close() error { return db.conn.Close() }

// Conn exposes the underlying pool.
func (db *DB) Conn() *sql.DB { return db.conn }

// Path returns the database file path.
func (db *DB) Path() string { return db.path }

var ddl = []string{
	`CREATE TABLE IF NOT EXISTS meta (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	) STRICT`,

	// --- who is running -----------------------------------------------------
	`CREATE TABLE IF NOT EXISTS agents (
		id           TEXT PRIMARY KEY,          -- 'bomclaw', 'bomclaw2'
		display_name TEXT NOT NULL DEFAULT '',
		provider     TEXT NOT NULL DEFAULT '',  -- claude | codex
		model        TEXT NOT NULL DEFAULT '',
		ws_addr      TEXT NOT NULL DEFAULT '',  -- how a peer reaches it
		workspace    TEXT NOT NULL DEFAULT '',
		started_at   TEXT NOT NULL,
		last_seen_at TEXT NOT NULL              -- heartbeat; stale ⇒ presumed dead
	) STRICT`,

	// --- traces (LangSmith run tree) ----------------------------------------
	`CREATE TABLE IF NOT EXISTS runs (
		id            TEXT PRIMARY KEY,
		trace_id      TEXT NOT NULL,            -- id of the root run
		parent_run_id TEXT NOT NULL DEFAULT '', -- '' for a root run
		dotted_order  TEXT NOT NULL,            -- sortable path: reconstructs the tree
		agent_id      TEXT NOT NULL,
		session_id    TEXT NOT NULL DEFAULT '',
		chat_id       INTEGER NOT NULL DEFAULT 0,
		name          TEXT NOT NULL,
		run_type      TEXT NOT NULL,            -- chain | llm | tool | memory | task
		status        TEXT NOT NULL,            -- pending | success | error
		started_at    TEXT NOT NULL,
		ended_at      TEXT NOT NULL DEFAULT '',
		duration_ms   INTEGER NOT NULL DEFAULT 0,
		inputs        TEXT NOT NULL DEFAULT '',
		outputs       TEXT NOT NULL DEFAULT '',
		error         TEXT NOT NULL DEFAULT '',
		input_tokens  INTEGER NOT NULL DEFAULT 0,
		output_tokens INTEGER NOT NULL DEFAULT 0,
		model         TEXT NOT NULL DEFAULT '',
		provider      TEXT NOT NULL DEFAULT '',
		tags          TEXT NOT NULL DEFAULT ''  -- JSON array
	) STRICT`,
	`CREATE INDEX IF NOT EXISTS idx_runs_tree   ON runs(trace_id, dotted_order)`,
	`CREATE INDEX IF NOT EXISTS idx_runs_recent ON runs(started_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_runs_roots  ON runs(parent_run_id, started_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_runs_agent  ON runs(agent_id, started_at DESC)`,

	// --- work handed between agents ----------------------------------------
	`CREATE TABLE IF NOT EXISTS tasks (
		id           TEXT PRIMARY KEY,
		context_id   TEXT NOT NULL,             -- groups a chain of related tasks
		created_by   TEXT NOT NULL,
		assigned_to  TEXT NOT NULL DEFAULT '',  -- '' = anyone may claim
		claimed_by   TEXT NOT NULL DEFAULT '',
		state        TEXT NOT NULL DEFAULT 'submitted',
		priority     INTEGER NOT NULL DEFAULT 0,
		title        TEXT NOT NULL,
		body         TEXT NOT NULL DEFAULT '',
		result       TEXT NOT NULL DEFAULT '',
		trace_id     TEXT NOT NULL DEFAULT '',  -- trace of the run that executed it
		lease_until  TEXT NOT NULL,             -- past = free, future = held
		attempts     INTEGER NOT NULL DEFAULT 0,
		max_attempts INTEGER NOT NULL DEFAULT 3,
		depth        INTEGER NOT NULL DEFAULT 0, -- guards agent ping-pong
		created_at   TEXT NOT NULL,
		updated_at   TEXT NOT NULL
	) STRICT`,
	`CREATE INDEX IF NOT EXISTS idx_tasks_claimable ON tasks(state, lease_until, priority DESC, created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_tasks_context   ON tasks(context_id)`,

	`CREATE TABLE IF NOT EXISTS task_events (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		task_id    TEXT NOT NULL,
		agent_id   TEXT NOT NULL,
		from_state TEXT NOT NULL DEFAULT '',
		to_state   TEXT NOT NULL,
		note       TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL
	) STRICT`,
	`CREATE INDEX IF NOT EXISTS idx_task_events ON task_events(task_id, id)`,

	// --- talk between agents ------------------------------------------------
	`CREATE TABLE IF NOT EXISTS agent_messages (
		id         TEXT PRIMARY KEY,
		from_agent TEXT NOT NULL,
		to_agent   TEXT NOT NULL,
		task_id    TEXT NOT NULL DEFAULT '',
		body       TEXT NOT NULL,
		read_at    TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL
	) STRICT`,
	`CREATE INDEX IF NOT EXISTS idx_msgs_unread ON agent_messages(to_agent, read_at, created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_msgs_recent ON agent_messages(created_at DESC)`,

	// --- what the agents know ----------------------------------------------
	// Append-only: a correction is a NEW row that the old one points at via
	// superseded_by. Two agents editing one note in place would silently
	// overwrite each other, and the history would be gone either way.
	`CREATE TABLE IF NOT EXISTS shared_notes (
		id            TEXT PRIMARY KEY,
		author        TEXT NOT NULL,
		scope         TEXT NOT NULL DEFAULT 'shared',  -- 'shared' or an agent id
		kind          TEXT NOT NULL,                   -- fact | decision | result | gotcha
		title         TEXT NOT NULL,
		body          TEXT NOT NULL DEFAULT '',
		tags          TEXT NOT NULL DEFAULT '',        -- comma separated
		superseded_by TEXT NOT NULL DEFAULT '',        -- '' means this is current
		created_at    TEXT NOT NULL
	) STRICT`,
	`CREATE INDEX IF NOT EXISTS idx_notes_live ON shared_notes(scope, superseded_by, created_at DESC)`,

	`CREATE VIRTUAL TABLE IF NOT EXISTS shared_notes_fts USING fts5(
		title, body, content='shared_notes', content_rowid='rowid'
	)`,
	// External-content FTS5 does not track its source table on its own.
	`CREATE TRIGGER IF NOT EXISTS shared_notes_ai AFTER INSERT ON shared_notes BEGIN
		INSERT INTO shared_notes_fts(rowid, title, body) VALUES (new.rowid, new.title, new.body);
	END`,
	`CREATE TRIGGER IF NOT EXISTS shared_notes_ad AFTER DELETE ON shared_notes BEGIN
		INSERT INTO shared_notes_fts(shared_notes_fts, rowid, title, body)
		VALUES ('delete', old.rowid, old.title, old.body);
	END`,
	`CREATE TRIGGER IF NOT EXISTS shared_notes_au AFTER UPDATE ON shared_notes BEGIN
		INSERT INTO shared_notes_fts(shared_notes_fts, rowid, title, body)
		VALUES ('delete', old.rowid, old.title, old.body);
		INSERT INTO shared_notes_fts(rowid, title, body) VALUES (new.rowid, new.title, new.body);
	END`,
}

func (db *DB) migrate() error {
	for _, stmt := range ddl {
		if _, err := db.conn.Exec(stmt); err != nil {
			return fmt.Errorf("%s: %w", firstLine(stmt), err)
		}
	}
	_, err := db.conn.Exec(
		`INSERT INTO meta (key, value) VALUES ('schema_version', ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		fmt.Sprint(schemaVersion))
	return err
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
	}
	return s
}

// ts formats a timestamp the way every column in this schema stores it.
// RFC3339 with nanoseconds sorts lexicographically, which the dotted_order
// scheme and every "ORDER BY started_at" depend on.
func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// parseTS is the inverse of ts, tolerant of the plain RFC3339 rows that
// hand-written or older data may contain.
func parseTS(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	t, _ := time.Parse(time.RFC3339, s)
	return t
}
