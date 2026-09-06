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
	"strings"
	"time"

	"github.com/ngocp/goterm-control/internal/storage"

	_ "modernc.org/sqlite"
)

const schemaVersion = 3

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

	// _txlock=immediate makes every Begin() a BEGIN IMMEDIATE. This file is
	// written by several connections at once — two gateway processes, plus the
	// trace recorder's async spans inside each — and a transaction that starts
	// deferred (read first, write later) cannot upgrade to a write lock if
	// another writer committed in between: SQLite returns SQLITE_BUSY at once,
	// ignoring busy_timeout, because the snapshot it read is already stale.
	// FinishRun is exactly that shape, and a span landing between its SELECT
	// and its UPDATE lost a run's outcome in production. Taking the write lock
	// up front turns that into an ordinary wait.
	conn, err := sql.Open("sqlite", storage.DSN(path)+"&_txlock=immediate")
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
		updated_at   TEXT NOT NULL,
		-- v3: a task is many runs. These columns carry what survives between them.
		parent_id         TEXT NOT NULL DEFAULT '',   -- '' = a root task
		kind              TEXT NOT NULL DEFAULT 'manual', -- manual | scheduled | heartbeat | sub
		schedule_id       TEXT NOT NULL DEFAULT '',
		checkpoint        TEXT NOT NULL DEFAULT '',   -- latest progress note, fed to the next run
		session_ref       TEXT NOT NULL DEFAULT '',   -- JSON SessionRef: the CLI session to --resume
		continuations     INTEGER NOT NULL DEFAULT 0, -- runs that ended "not done yet" (≠ attempts, which are failures)
		max_continuations INTEGER NOT NULL DEFAULT 20,
		blocked_on        TEXT NOT NULL DEFAULT '',   -- '' | children | human
		fail_reason       TEXT NOT NULL DEFAULT ''    -- exhausted | continuations-exhausted | empty-exhausted
	) STRICT`,
	`CREATE INDEX IF NOT EXISTS idx_tasks_claimable ON tasks(state, lease_until, priority DESC, created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_tasks_context   ON tasks(context_id)`,

	// One row per claim. tasks.state says where the WORK is; a run's liveness
	// says how ONE attempt at it ended. Keeping them apart is what lets a task
	// outlive the 15-minute run cap without lying about either.
	`CREATE TABLE IF NOT EXISTS task_runs (
		id         TEXT PRIMARY KEY,
		task_id    TEXT NOT NULL,
		agent_id   TEXT NOT NULL,
		attempt    INTEGER NOT NULL,            -- fencing token of this claim
		liveness   TEXT NOT NULL DEFAULT 'running',
		           -- running|completed|advanced|plan_only|empty|blocked|failed|timed_out|canceled
		trace_id   TEXT NOT NULL DEFAULT '',
		started_at TEXT NOT NULL,
		ended_at   TEXT NOT NULL DEFAULT '',
		note       TEXT NOT NULL DEFAULT ''
	) STRICT`,
	`CREATE INDEX IF NOT EXISTS idx_task_runs_task ON task_runs(task_id, started_at)`,

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

// v3Columns are the columns added to tasks after it first shipped. CREATE TABLE
// IF NOT EXISTS leaves an existing table alone, so they are added one by one,
// each guarded by a catalogue check — which also makes it safe for two gateways
// to open the file at the same moment.
var v3Columns = []struct{ name, decl string }{
	{"parent_id", "TEXT NOT NULL DEFAULT ''"},
	{"kind", "TEXT NOT NULL DEFAULT 'manual'"},
	{"schedule_id", "TEXT NOT NULL DEFAULT ''"},
	{"checkpoint", "TEXT NOT NULL DEFAULT ''"},
	{"session_ref", "TEXT NOT NULL DEFAULT ''"},
	{"continuations", "INTEGER NOT NULL DEFAULT 0"},
	{"max_continuations", "INTEGER NOT NULL DEFAULT 20"},
	{"blocked_on", "TEXT NOT NULL DEFAULT ''"},
	{"fail_reason", "TEXT NOT NULL DEFAULT ''"},
}

// v3Indexes reference columns that v3Columns adds, so on a database created by
// the previous version they can only be built after those columns exist — a
// fresh database gets them from the CREATE TABLE and the ALTERs are no-ops,
// but an upgraded one would fail with "no such column" if these sat in ddl.
var v3Indexes = []string{
	`CREATE INDEX IF NOT EXISTS idx_tasks_parent ON tasks(parent_id, state)`,
}

func (db *DB) migrate() error {
	for _, stmt := range ddl {
		if _, err := db.conn.Exec(stmt); err != nil {
			return fmt.Errorf("%s: %w", firstLine(stmt), err)
		}
	}
	for _, c := range v3Columns {
		if err := db.ensureColumn("tasks", c.name, c.decl); err != nil {
			return err
		}
	}
	for _, stmt := range v3Indexes {
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

// ensureColumn adds a column if the table does not have it yet. A concurrent
// opener may add it between the check and the ALTER; that error is the one
// benign outcome and is swallowed.
func (db *DB) ensureColumn(table, column, decl string) error {
	var n int
	if err := db.conn.QueryRow(
		`SELECT count(*) FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&n); err != nil {
		return fmt.Errorf("inspect %s.%s: %w", table, column, err)
	}
	if n > 0 {
		return nil
	}
	_, err := db.conn.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, decl))
	if err != nil && strings.Contains(err.Error(), "duplicate column") {
		return nil
	}
	if err != nil {
		return fmt.Errorf("add %s.%s: %w", table, column, err)
	}
	return nil
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
