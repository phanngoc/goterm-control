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

const schemaVersion = 11

// ProgressPrefix marks a message that exists only while something is running.
// It lives here because two packages write these lines — the mention watcher
// and the task runner — and the startup sweep has to recognise both.
const ProgressPrefix = "⏳ "

// DB is the shared coordination database.
type DB struct {
	conn *sql.DB
	path string
	// artifactsDir is the root artifact bytes are written under; empty means
	// DefaultArtifactsDir. Config overrides it, tests point it at t.TempDir.
	artifactsDir string
	// maxTasksPerContext caps the size of one task tree; 0 means the default.
	maxTasksPerContext int
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
		last_seen_at TEXT NOT NULL,             -- heartbeat; stale ⇒ presumed dead
		scratch      TEXT NOT NULL DEFAULT ''   -- v4: what the agent's heartbeat should look at
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
		fail_reason       TEXT NOT NULL DEFAULT '',   -- exhausted | continuations-exhausted | empty-exhausted
		-- v5: set once the task's outcome has been reported to whoever asked for
		-- it. Empty on a terminal task means a report is still owed.
		reported_at       TEXT NOT NULL DEFAULT ''
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

	// --- schedules: automations decide WHEN, tasks record WHAT happened -----
	// A schedule never runs a model itself. When it is due, an `agent` payload
	// materialises one ordinary task row (kind='scheduled') that goes through
	// claim/lease/fencing like any other; a `command` payload runs a shell
	// command inside the gateway. next_run_at is the only "clock": firing is
	// a compare-and-set on it, so two gateways that both see a due schedule
	// produce exactly one run without a lock or a leader.
	`CREATE TABLE IF NOT EXISTS schedules (
		id                   TEXT PRIMARY KEY,          -- 'sch_' || uuid
		name                 TEXT NOT NULL UNIQUE,
		created_by           TEXT NOT NULL,
		owner_agent          TEXT NOT NULL DEFAULT '',  -- '' = any gateway may fire it
		kind                 TEXT NOT NULL,             -- at | every | cron
		spec                 TEXT NOT NULL,             -- RFC3339 | duration | 5-field cron
		tz                   TEXT NOT NULL,             -- IANA zone, always explicit
		payload_kind         TEXT NOT NULL,             -- agent | command
		payload              TEXT NOT NULL,             -- JSON
		enabled              INTEGER NOT NULL DEFAULT 1,
		system               INTEGER NOT NULL DEFAULT 0, -- 1 = owned by the gateway (heartbeat)
		skip_missed          INTEGER NOT NULL DEFAULT 0, -- 1 = re-arm after downtime instead of catching up
		next_run_at          TEXT NOT NULL,
		last_run_at          TEXT NOT NULL DEFAULT '',
		last_status          TEXT NOT NULL DEFAULT '',  -- ok | failed | skipped
		consecutive_failures INTEGER NOT NULL DEFAULT 0,
		created_at           TEXT NOT NULL,
		updated_at           TEXT NOT NULL
	) STRICT`,
	`CREATE INDEX IF NOT EXISTS idx_schedules_due ON schedules(enabled, next_run_at)`,

	// One row per firing. For an agent payload the row is 'pending' until the
	// task it created reaches a terminal state; whichever gateway notices
	// settles it (again by compare-and-set on status).
	`CREATE TABLE IF NOT EXISTS schedule_runs (
		id          TEXT PRIMARY KEY,               -- 'sr_' || uuid
		schedule_id TEXT NOT NULL,
		task_id     TEXT NOT NULL DEFAULT '',       -- when payload_kind = agent
		started_at  TEXT NOT NULL,
		ended_at    TEXT NOT NULL DEFAULT '',
		status      TEXT NOT NULL,                  -- pending | ok | failed | skipped
		exit_code   INTEGER NOT NULL DEFAULT 0,
		output      TEXT NOT NULL DEFAULT ''        -- command output, cut to 8KB
	) STRICT`,
	`CREATE INDEX IF NOT EXISTS idx_schedule_runs ON schedule_runs(schedule_id, started_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_schedule_runs_pending ON schedule_runs(status, task_id)`,

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

	// --- v6: artifacts — what a task produced -------------------------------
	// The bytes live on disk, not here: a parent that gathers eight children
	// would otherwise pull eight patches through SQLite and then through a
	// prompt. The row is the pointer, `preview` is the first few hundred runes
	// so an index reads usefully, and `bomclaw artifact get` fetches the rest
	// only when the agent actually needs it.
	`CREATE TABLE IF NOT EXISTS artifacts (
		id           TEXT PRIMARY KEY,          -- 'a_' || uuid
		context_id   TEXT NOT NULL,             -- the task tree this belongs to
		task_id      TEXT NOT NULL,             -- the task that produced it
		kind         TEXT NOT NULL,             -- document | patch | file | link | result
		title        TEXT NOT NULL,
		content_type TEXT NOT NULL DEFAULT '',
		path         TEXT NOT NULL DEFAULT '',  -- relative to the artifacts root; '' for kind=link
		url          TEXT NOT NULL DEFAULT '',  -- kind=link only
		preview      TEXT NOT NULL DEFAULT '',  -- first PreviewRunes of the content
		bytes        INTEGER NOT NULL DEFAULT 0,
		created_by   TEXT NOT NULL,
		created_at   TEXT NOT NULL
	) STRICT`,
	`CREATE INDEX IF NOT EXISTS idx_artifacts_task    ON artifacts(task_id, created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_artifacts_context ON artifacts(context_id, created_at)`,

	// A parent hands an artifact down to a child as an input without copying
	// it: same artifact, a second row with role='input'. The producing task
	// always has an implicit output link through artifacts.task_id, so only
	// the extra edges are stored here.
	`CREATE TABLE IF NOT EXISTS artifact_links (
		artifact_id TEXT NOT NULL,
		task_id     TEXT NOT NULL,
		role        TEXT NOT NULL,             -- input | output
		created_at  TEXT NOT NULL,
		PRIMARY KEY (artifact_id, task_id, role)
	) STRICT`,
	`CREATE INDEX IF NOT EXISTS idx_artifact_links_task ON artifact_links(task_id, role)`,

	// --- v6: channels — a place, not a pair ---------------------------------
	// agent_messages addressed one agent, so nothing existed independently of
	// two names and a third agent could not read along. A channel is the place;
	// membership says who is in the work; a mention is what claims attention.
	`CREATE TABLE IF NOT EXISTS channels (
		id          TEXT PRIMARY KEY,          -- 'ch_<slug>' or 'dm_<a>__<b>' (sorted, so it is derivable)
		name        TEXT NOT NULL,
		kind        TEXT NOT NULL DEFAULT 'channel', -- channel | dm
		purpose     TEXT NOT NULL DEFAULT '',
		created_by  TEXT NOT NULL,
		created_at  TEXT NOT NULL,
		archived_at TEXT NOT NULL DEFAULT ''
	) STRICT`,

	`CREATE TABLE IF NOT EXISTS channel_members (
		channel_id   TEXT NOT NULL,
		member_kind  TEXT NOT NULL,            -- agent | user
		member_id    TEXT NOT NULL,
		joined_at    TEXT NOT NULL,
		last_read_at TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (channel_id, member_kind, member_id)
	) STRICT`,
	`CREATE INDEX IF NOT EXISTS idx_channel_members_who ON channel_members(member_kind, member_id)`,

	// A thread is replies sharing a root message id — Slack's own model, and
	// one table fewer than a threads table that would only ever hold a title.
	// task_id on a root message is what binds a thread to a task.
	`CREATE TABLE IF NOT EXISTS channel_messages (
		id          TEXT PRIMARY KEY,          -- 'cm_' || uuid
		channel_id  TEXT NOT NULL,
		thread_root TEXT NOT NULL DEFAULT '',  -- '' = top level; else the root message id
		author_kind TEXT NOT NULL,             -- agent | user
		author_id   TEXT NOT NULL,
		body        TEXT NOT NULL,
		task_id     TEXT NOT NULL DEFAULT '',
		created_at  TEXT NOT NULL
	) STRICT`,
	`CREATE INDEX IF NOT EXISTS idx_channel_messages_ch     ON channel_messages(channel_id, created_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_channel_messages_thread ON channel_messages(thread_root, created_at)`,

	// Being mentioned is the only thing that wakes an agent. Unread lives here
	// rather than being derived from last_read_at because "someone called me"
	// and "I have not caught up on the channel" are different questions.
	`CREATE TABLE IF NOT EXISTS channel_mentions (
		message_id  TEXT NOT NULL,
		member_kind TEXT NOT NULL,
		member_id   TEXT NOT NULL,
		read_at     TEXT NOT NULL DEFAULT '',
		created_at  TEXT NOT NULL,
		PRIMARY KEY (message_id, member_kind, member_id)
	) STRICT`,
	`CREATE INDEX IF NOT EXISTS idx_channel_mentions_unread ON channel_mentions(member_kind, member_id, read_at, created_at DESC)`,

	// What an agent remembers of a thread. A mention is answered by a chat turn,
	// and a chat turn that forgets the last one is not a conversation — so the
	// CLI session the agent used is kept here and resumed next time, the way
	// tasks.session_ref keeps a task's own thread of thought across runs.
	//
	// Keyed by thread rather than by channel: two threads in one room are two
	// conversations, and Slack's own model says so. turns is the loop stop —
	// nothing inside a conversation ever says "enough".
	// --- v9: a channel that also speaks to Telegram -------------------------
	// One row per bound room. chat_id is the Telegram conversation it speaks
	// into; mode is how much of the room goes there; created_at is the cut-off,
	// so binding a room that has been busy all week does not empty that week
	// onto a phone.
	//
	// A table rather than a column on channels: most rooms are not bound, the
	// binding is an integration rather than a property of the place, and
	// dropping it should leave no trace on the channel itself.
	`CREATE TABLE IF NOT EXISTS channel_telegram (
		channel_id TEXT PRIMARY KEY,
		agent_id   TEXT NOT NULL DEFAULT '',       -- whose bot carries this room
		chat_id    INTEGER NOT NULL,
		mode       TEXT NOT NULL DEFAULT 'mentions',  -- all | mentions | off
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	) STRICT`,

	`CREATE TABLE IF NOT EXISTS channel_sessions (
		thread_key TEXT NOT NULL,             -- thread_root, or channel_id for the main line
		agent_id   TEXT NOT NULL,
		provider   TEXT NOT NULL DEFAULT '',
		session_id TEXT NOT NULL DEFAULT '',
		turns      INTEGER NOT NULL DEFAULT 0,
		updated_at TEXT NOT NULL,
		PRIMARY KEY (thread_key, agent_id)
	) STRICT`,

	// --- v11: a room speaks to several places, not to one bot --------------
	//
	// channel_telegram held exactly one binding by construction — channel_id
	// was its primary key — so #trading reached Telegram or it reached nothing.
	// A room with a Telegram chat, a Slack webhook and a dashboard is three
	// destinations for the same sentence, and there was nowhere to write the
	// second one down.
	//
	// agent_id is still the carrier, and still load-bearing: every gateway
	// process runs the same sweep, and a row is only ever seen by the process
	// named here. That is what keeps 6ac6462's triple send from coming back
	// now that one room can have several rows.
	//
	// since is split from created_at. One column used to carry both the row's
	// birth and the forward cut-off, which is why the re-bind path has a
	// comment defending not touching it — and why there was no way to move the
	// cut-off forward when a paused destination was switched back on.
	`CREATE TABLE IF NOT EXISTS channel_gateways (
		id         TEXT PRIMARY KEY,                 -- 'cg_' || uuid
		channel_id TEXT NOT NULL,
		kind       TEXT NOT NULL,                    -- telegram | webhook
		agent_id   TEXT NOT NULL,                    -- which process carries it
		target     TEXT NOT NULL,                    -- chat id (decimal) or URL
		secret     TEXT NOT NULL DEFAULT '',         -- bearer token for webhook
		mode       TEXT NOT NULL DEFAULT 'mentions', -- all | mentions | off
		label      TEXT NOT NULL DEFAULT '',         -- what a person calls it
		since      TEXT NOT NULL,                    -- cut-off: nothing older travels
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	) STRICT`,

	// The unique index deliberately leaves out agent_id. Two different bots
	// firing into one chat is the owner getting two notifications for one
	// sentence — 6ac6462 wearing a different hat. Several gateways means
	// several DESTINATIONS, not several roads to one destination.
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_channel_gateways_dest
		ON channel_gateways(channel_id, kind, target)`,
	`CREATE INDEX IF NOT EXISTS idx_channel_gateways_agent
		ON channel_gateways(agent_id, mode)`,

	// --- v11: one delivery per (line, destination) -------------------------
	//
	// This is the column-to-row move. forwarded_at/tg_message_id/forwarded_by
	// sat on the message itself, so a line could be delivered exactly once,
	// forever.
	//
	// state records both outcomes: 'sent' with the id the far side gave it,
	// and 'skipped' when the mode declined it — because a declined line whose
	// answer can never change must not be re-asked every ten seconds.
	//
	// The primary key is the idempotency latch: inserting is how a delivery is
	// claimed, and a second attempt is a no-op rather than a second
	// notification. It is not the thing that prevents the race — it cannot
	// recall a message that has already left. Carrier scoping does that.
	//
	// agent_id duplicates the gateway's carrier on purpose: it is the scope of
	// the return path, it replaces forwarded_by one for one, and moving a
	// binding to another agent must not orphan the Telegram ids already sent.
	`CREATE TABLE IF NOT EXISTS channel_deliveries (
		message_id  TEXT NOT NULL,
		gateway_id  TEXT NOT NULL,
		state       TEXT NOT NULL,              -- sent | skipped
		external_id TEXT NOT NULL DEFAULT '',   -- telegram message id; '' when there is none
		agent_id    TEXT NOT NULL DEFAULT '',   -- the bot that sent it: a tg id is per-bot
		created_at  TEXT NOT NULL,
		PRIMARY KEY (message_id, gateway_id)
	) STRICT`,
	`CREATE INDEX IF NOT EXISTS idx_channel_deliveries_ext
		ON channel_deliveries(agent_id, external_id)`,
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

// v4Columns: the heartbeat scratchpad on agents. Same guard as v3Columns.
var v4Columns = []struct{ table, name, decl string }{
	{"agents", "scratch", "TEXT NOT NULL DEFAULT ''"},
}

// v3Indexes reference columns that v3Columns adds, so on a database created by
// the previous version they can only be built after those columns exist — a
// fresh database gets them from the CREATE TABLE and the ALTERs are no-ops,
// but an upgraded one would fail with "no such column" if these sat in ddl.
// v5Columns adds the reporting marker to tasks. Same guarded-ALTER treatment as
// v3Columns: CREATE TABLE IF NOT EXISTS leaves an existing table alone, and the
// catalogue check makes it safe for two gateways to open the file at once.
var v5Columns = []struct{ name, decl string }{
	{"reported_at", "TEXT NOT NULL DEFAULT ''"},
}

// v8 turns a channel into a project: a room with a folder behind it, and tasks
// that belong to it.
//
// Two columns rather than a new table. A project IS a channel — the
// conversation, the work and the files are the same thing seen from three
// sides, and modelling them apart would mean keeping three names in step.
var v8ChannelColumns = []struct{ name, decl string }{
	// Where this project's work lives on disk: source, artifacts, whatever the
	// agents produce. Empty for rooms that are only rooms (#general, DMs).
	{"workspace", "TEXT NOT NULL DEFAULT ''"},
}

// v8TaskColumns: which project a task belongs to.
//
// context_id already groups a task with its own children; it does not say
// which piece of work the tree is part of. A board showing every task on the
// machine is a board nobody can read once there is more than one project.
var v8TaskColumns = []struct{ name, decl string }{
	{"channel_id", "TEXT NOT NULL DEFAULT ''"},
}

// v8ScheduleColumns: which project a schedule belongs to. Timed work is work —
// "check the prices every five minutes" belongs to the trading project the
// same way a task does, and a Schedules tab listing every clock on the machine
// has the same problem as a board listing every task.
var v8ScheduleColumns = []struct{ name, decl string }{
	{"channel_id", "TEXT NOT NULL DEFAULT ''"},
}

// v6Columns: the acceptance bar a child is judged against. Paperclip's rule —
// a child a reviewer could call "half done" was never scoped — so the bar is
// recorded with the work, not left in the parent's head.
var v6Columns = []struct{ name, decl string }{
	{"acceptance", "TEXT NOT NULL DEFAULT ''"},
}

// v10AgentColumns: which Telegram bot this agent answers on.
//
// Every agent here runs its own bot now, so "message this agent privately" is a
// different chat per agent — and the dashboard had no way to name which. Read
// from the bot itself once it logs in rather than from config: config holds a
// token, and the username is what a person clicks.
var v10AgentColumns = []struct{ name, decl string }{
	{"telegram_bot", "TEXT NOT NULL DEFAULT ''"},
}

// v9MessageColumns: what has left the room, and what it became out there.
//
// forwarded_at is "this line has been decided about", not "this line was sent"
// — a message the mode filtered out is stamped too, because the answer would
// never change and reconsidering it on every sweep is work that repeats
// forever. tg_message_id is 0 for those, and for everything that was sent it is
// the hook the return path hangs on: a reply on Telegram quotes a message id,
// and that is how the reply finds its thread.
var v9MessageColumns = []struct{ name, decl string }{
	{"forwarded_at", "TEXT NOT NULL DEFAULT ''"},
	{"tg_message_id", "INTEGER NOT NULL DEFAULT 0"},
	// Which agent's bot sent it. Every agent on this machine now runs its own
	// Telegram bot, and a message id is per-bot: @Goterm_bot's message 8821 and
	// @Goterm3_bot's message 8821 are different messages. Without this column a
	// reply to one would be matched against the other's line and answered into
	// the wrong thread.
	{"forwarded_by", "TEXT NOT NULL DEFAULT ''"},
}

// v9BindingColumns: which agent a binding belongs to. A chat id alone does not
// say who speaks into it, and three bots can all reach the same person.
var v9BindingColumns = []struct{ name, decl string }{
	{"agent_id", "TEXT NOT NULL DEFAULT ''"},
}

var v3Indexes = []string{
	`CREATE INDEX IF NOT EXISTS idx_tasks_parent ON tasks(parent_id, state)`,
	// Same rule, v8: this indexes a column the ALTERs above add, so it cannot
	// sit in ddl — a fresh database would build it before the column exists.
	// The test suite said so within a minute of it being put there.
	`CREATE INDEX IF NOT EXISTS idx_tasks_channel ON tasks(channel_id, state)`,
	// v9 indexed forwarded_at and (forwarded_by, tg_message_id) on
	// channel_messages. v11 moved delivery state out to channel_deliveries, so
	// all three are dead columns now. The columns themselves stay one more
	// version — if the migration got something wrong the data is still where it
	// was — but an index nothing reads is pure write cost on every message
	// inserted, so the indexes go. Dropping by name rather than deleting the
	// CREATEs: a database that already has them is the case that matters.
	`DROP INDEX IF EXISTS idx_channel_messages_tg`,
	`DROP INDEX IF EXISTS idx_channel_messages_forward`,
	`DROP INDEX IF EXISTS idx_channel_messages_tgmsg`,
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
	for _, c := range v4Columns {
		if err := db.ensureColumn(c.table, c.name, c.decl); err != nil {
			return err
		}
	}
	for _, c := range v5Columns {
		if err := db.ensureColumn("tasks", c.name, c.decl); err != nil {
			return err
		}
	}
	for _, c := range v6Columns {
		if err := db.ensureColumn("tasks", c.name, c.decl); err != nil {
			return err
		}
	}
	for _, c := range v8ChannelColumns {
		if err := db.ensureColumn("channels", c.name, c.decl); err != nil {
			return err
		}
	}
	for _, c := range v8TaskColumns {
		if err := db.ensureColumn("tasks", c.name, c.decl); err != nil {
			return err
		}
	}
	for _, c := range v8ScheduleColumns {
		if err := db.ensureColumn("schedules", c.name, c.decl); err != nil {
			return err
		}
	}
	for _, c := range v9MessageColumns {
		if err := db.ensureColumn("channel_messages", c.name, c.decl); err != nil {
			return err
		}
	}
	for _, c := range v9BindingColumns {
		if err := db.ensureColumn("channel_telegram", c.name, c.decl); err != nil {
			return err
		}
	}
	for _, c := range v10AgentColumns {
		if err := db.ensureColumn("agents", c.name, c.decl); err != nil {
			return err
		}
	}
	for _, stmt := range v3Indexes {
		if _, err := db.conn.Exec(stmt); err != nil {
			return fmt.Errorf("%s: %w", firstLine(stmt), err)
		}
	}
	if err := db.migrateMessagesToChannels(); err != nil {
		return err
	}
	if err := db.migrateBindingsToGateways(); err != nil {
		return err
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

// ts formats a timestamp the way every column in this schema stores it. The
// dotted_order scheme, every "ORDER BY started_at", every "lease_until <= now"
// and the scheduler's "next_run_at <= now" compare these as strings, so the
// format must sort like the instants do.
//
// time.RFC3339Nano does NOT: it trims trailing zeros from the fraction, so
// "…00.1Z" sorts after "…00.10000001Z" ('Z' > '0') although it is earlier.
// That made a task finished at .1 look unclaimable at .10000001 — a flake in
// tests, a 10-minute lease wait in production. Nine fixed digits keep the
// string order equal to the time order; parseTS still reads either form.
func ts(t time.Time) string { return t.UTC().Format(tsLayout) }

const tsLayout = "2006-01-02T15:04:05.000000000Z07:00"

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
