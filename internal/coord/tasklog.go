package coord

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// A task's session, readable while it happens — and a way to talk into it.
//
// A task ran in a CLI session of its own that nobody could open: the board
// showed "Bash(until ../scout.md) · 11 tool calls" and a link to a chat page
// that no longer existed. The conversation lived in one agent's CLI store, in
// that CLI's own format, on whichever agent happened to claim it.
//
// task_log is that conversation in the shared database, written by the runner
// as it streams: what the agent was asked, what it said, the tools it called,
// and what the owner said to it. Every agent writes it the same way whatever
// its CLI, so the dashboard reads one table for all of them.
//
// task_followups is the other direction: a line the owner typed into the
// task's session. Who delivers it depends on where the task is —
//   - running: it waits, and the run that comes next reads it;
//   - open (queued, continuing): the next run's prompt carries it;
//   - waiting on a person: it IS the answer, and unblocks the task;
//   - finished: the agent that ran it resumes the same CLI session for one
//     more turn, without reopening the task.

// Task log entry kinds.
const (
	LogPrompt     = "prompt"      // what the run was given
	LogText       = "text"        // the agent's reply, as it streamed
	LogThinking   = "thinking"    // the model's reasoning, where the CLI reports it
	LogTool       = "tool"        // a tool call: Label is the short form, Content the input
	LogToolResult = "tool_result" // its output, truncated; Label "error" when it failed
	LogUser       = "user"        // the owner, through the dashboard
	LogStatus     = "status"      // the run started or ended, and how
)

// Follow-up states.
const (
	FollowUpQueued    = "queued"    // not yet in front of the agent
	FollowUpDelivered = "delivered" // folded into a task run's prompt
	FollowUpRunning   = "running"   // a follow-up turn is answering it now
	FollowUpAnswered  = "answered"
	FollowUpFailed    = "failed"
)

// maxLogContent caps one entry. A tool that prints a megabyte is a megabyte
// nobody will scroll through, written to a file three processes share.
const maxLogContent = 16 << 10

// followUpStale is how long a follow-up may sit in "running" before it is
// taken to be orphaned — its agent restarted mid-turn — and offered again.
const followUpStale = 30 * time.Minute

var taskLogSchema = []string{
	`CREATE TABLE IF NOT EXISTS task_log (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		task_id    TEXT NOT NULL,
		run_id     TEXT NOT NULL DEFAULT '',  -- task_runs.id, or the follow-up's id
		agent_id   TEXT NOT NULL DEFAULT '',
		kind       TEXT NOT NULL,
		label      TEXT NOT NULL DEFAULT '',
		content    TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL
	) STRICT`,
	`CREATE INDEX IF NOT EXISTS idx_task_log_task ON task_log(task_id, id)`,
	`CREATE TABLE IF NOT EXISTS task_followups (
		id         TEXT PRIMARY KEY,
		task_id    TEXT NOT NULL,
		author     TEXT NOT NULL,
		body       TEXT NOT NULL,
		state      TEXT NOT NULL DEFAULT 'queued',
		agent_id   TEXT NOT NULL DEFAULT '',   -- who answered it
		mode       TEXT NOT NULL DEFAULT 'queue', -- queue | interrupt (stop the run in flight)
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	) STRICT`,
	`CREATE INDEX IF NOT EXISTS idx_task_followups_state ON task_followups(state, created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_task_followups_task ON task_followups(task_id, created_at)`,
}

// TaskLogEntry is one line of a task's session.
type TaskLogEntry struct {
	ID        int64     `json:"id"`
	TaskID    string    `json:"task_id"`
	RunID     string    `json:"run_id,omitempty"`
	AgentID   string    `json:"agent_id,omitempty"`
	Kind      string    `json:"kind"`
	Label     string    `json:"label,omitempty"`
	Content   string    `json:"content,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// FollowUp is a line the owner typed into a task's session.
type FollowUp struct {
	ID        string    `json:"id"`
	TaskID    string    `json:"task_id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	State     string    `json:"state"`
	AgentID   string    `json:"agent_id,omitempty"`
	// Mode is how it reaches a task that is running: "queue" waits for the
	// run to end; "interrupt" stops it, and the run that follows — same
	// agent, same CLI session — starts from this line. Claude Code's
	// Esc-and-type, for a task.
	Mode      string    `json:"mode"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Follow-up modes.
const (
	FollowUpQueue     = "queue"
	FollowUpInterrupt = "interrupt"
)

// IsTerminalState reports whether a task in this state will not run again by
// itself.
func IsTerminalState(state string) bool {
	switch state {
	case TaskCompleted, TaskFailed, TaskCanceled, TaskRejected:
		return true
	}
	return false
}

// clip keeps a log entry within maxLogContent, on a rune boundary, and says so.
func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && cut < len(s) && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + fmt.Sprintf("\n… (%d bytes more)", len(s)-cut)
}

// AppendTaskLog writes one entry. Content over the cap is cut, not refused.
func (db *DB) AppendTaskLog(e TaskLogEntry) error {
	if e.TaskID == "" || e.Kind == "" {
		return fmt.Errorf("task log: task and kind are required")
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now()
	}
	_, err := db.conn.Exec(`INSERT INTO task_log (task_id, run_id, agent_id, kind, label, content, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		e.TaskID, e.RunID, e.AgentID, e.Kind, clip(e.Label, 512), clip(e.Content, maxLogContent), ts(e.CreatedAt))
	if err != nil {
		return fmt.Errorf("task log %s: %w", e.TaskID, err)
	}
	return nil
}

// TaskLog returns a task's session after the entry afterID (0 = from the
// start), oldest first, at most limit entries. A poller passes the last id it
// has and gets only what is new.
func (db *DB) TaskLog(taskID string, afterID int64, limit int) ([]TaskLogEntry, error) {
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	rows, err := db.conn.Query(`SELECT id, task_id, run_id, agent_id, kind, label, content, created_at
		FROM task_log WHERE task_id = ? AND id > ? ORDER BY id LIMIT ?`, taskID, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("task log %s: %w", taskID, err)
	}
	defer rows.Close()
	out := []TaskLogEntry{}
	for rows.Next() {
		var e TaskLogEntry
		var created string
		if err := rows.Scan(&e.ID, &e.TaskID, &e.RunID, &e.AgentID, &e.Kind, &e.Label, &e.Content, &created); err != nil {
			return nil, fmt.Errorf("scan task log: %w", err)
		}
		e.CreatedAt = parseTS(created)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ErrFollowUpRefused means the task cannot take a follow-up at all.
var ErrFollowUpRefused = errors.New("coord: this task cannot take a follow-up")

// AddFollowUp records a line from the owner and puts it in the task's session
// at once, so it is on screen before any agent has picked it up.
//
// A task that never ran and never will — canceled or rejected before anyone
// claimed it — has no session to continue; that is refused rather than queued
// for an agent that does not exist.
func (db *DB) AddFollowUp(taskID, author, body string, interrupt bool) (*FollowUp, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, fmt.Errorf("follow-up: message is required")
	}
	t, err := db.GetTask(taskID)
	if err != nil {
		return nil, err
	}
	if IsTerminalState(t.State) && t.ClaimedBy == "" {
		return nil, fmt.Errorf("%w: it ended before any agent ran it", ErrFollowUpRefused)
	}
	now := time.Now()
	f := &FollowUp{
		ID: "fu_" + uuid.NewString()[:13], TaskID: taskID, Author: author, Body: body,
		State: FollowUpQueued, Mode: FollowUpQueue, CreatedAt: now, UpdatedAt: now,
	}
	if interrupt {
		f.Mode = FollowUpInterrupt
	}
	if _, err := db.conn.Exec(`INSERT INTO task_followups (id, task_id, author, body, state, mode, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, f.ID, f.TaskID, f.Author, f.Body, f.State, f.Mode, ts(now), ts(now)); err != nil {
		return nil, fmt.Errorf("follow-up %s: %w", taskID, err)
	}
	_ = db.AppendTaskLog(TaskLogEntry{TaskID: taskID, RunID: f.ID, AgentID: author, Kind: LogUser, Content: body, CreatedAt: now})
	return f, nil
}

// FollowUps lists a task's follow-ups, oldest first.
func (db *DB) FollowUps(taskID string) ([]FollowUp, error) {
	rows, err := db.conn.Query(`SELECT id, task_id, author, body, state, agent_id, mode, created_at, updated_at
		FROM task_followups WHERE task_id = ? ORDER BY created_at`, taskID)
	if err != nil {
		return nil, fmt.Errorf("follow-ups %s: %w", taskID, err)
	}
	return scanFollowUps(rows)
}

func scanFollowUps(rows *sql.Rows) ([]FollowUp, error) {
	defer rows.Close()
	out := []FollowUp{}
	for rows.Next() {
		var f FollowUp
		var created, updated string
		if err := rows.Scan(&f.ID, &f.TaskID, &f.Author, &f.Body, &f.State, &f.AgentID, &f.Mode, &created, &updated); err != nil {
			return nil, fmt.Errorf("scan follow-up: %w", err)
		}
		f.CreatedAt, f.UpdatedAt = parseTS(created), parseTS(updated)
		out = append(out, f)
	}
	return out, rows.Err()
}

// TakeFollowUps hands the queued follow-ups of a task to the run about to
// start, marking them delivered: they go into that run's prompt, once.
func (db *DB) TakeFollowUps(taskID, agentID string) ([]FollowUp, error) {
	tx, err := db.conn.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT id, task_id, author, body, state, agent_id, mode, created_at, updated_at
		FROM task_followups WHERE task_id = ? AND state = ? ORDER BY created_at`, taskID, FollowUpQueued)
	if err != nil {
		return nil, err
	}
	fs, err := scanFollowUps(rows)
	if err != nil || len(fs) == 0 {
		return fs, err
	}
	now := ts(time.Now())
	for _, f := range fs {
		if _, err := tx.Exec(`UPDATE task_followups SET state = ?, agent_id = ?, updated_at = ? WHERE id = ?`,
			FollowUpDelivered, agentID, now, f.ID); err != nil {
			return nil, err
		}
	}
	return fs, tx.Commit()
}

// ClaimFollowUp takes the oldest follow-up this agent should answer with a
// turn of its own: one on a finished task it ran. busy says whether a task's
// session is in use right now on this agent; those are left for later.
//
// One at a time per task, and the claim is a compare-and-set, so two passes
// of the loop never answer the same line twice.
func (db *DB) ClaimFollowUp(agentID string, busy func(taskID string) bool) (*FollowUp, *Task, error) {
	stale := ts(time.Now().Add(-followUpStale))
	rows, err := db.conn.Query(`SELECT f.id, f.task_id, f.author, f.body, f.state, f.agent_id, f.mode, f.created_at, f.updated_at
		FROM task_followups f JOIN tasks t ON t.id = f.task_id
		WHERE (f.state = ? OR (f.state = ? AND f.updated_at < ?))
		  AND t.claimed_by = ?
		  AND t.state IN (?, ?, ?, ?)
		  AND NOT EXISTS (SELECT 1 FROM task_followups g
		                  WHERE g.task_id = f.task_id AND g.state = ? AND g.updated_at >= ?)
		ORDER BY f.created_at LIMIT 20`,
		FollowUpQueued, FollowUpRunning, stale, agentID,
		TaskCompleted, TaskFailed, TaskCanceled, TaskRejected,
		FollowUpRunning, stale)
	if err != nil {
		return nil, nil, fmt.Errorf("claim follow-up: %w", err)
	}
	cands, err := scanFollowUps(rows)
	if err != nil {
		return nil, nil, err
	}
	for _, f := range cands {
		if busy != nil && busy(f.TaskID) {
			continue
		}
		res, err := db.conn.Exec(`UPDATE task_followups SET state = ?, agent_id = ?, updated_at = ?
			WHERE id = ? AND state = ? AND updated_at = ?`,
			FollowUpRunning, agentID, ts(time.Now()), f.ID, f.State, ts(f.UpdatedAt))
		if err != nil {
			return nil, nil, fmt.Errorf("claim follow-up %s: %w", f.ID, err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue // somebody else's pass got it
		}
		t, err := db.GetTask(f.TaskID)
		if err != nil {
			return nil, nil, err
		}
		f.State, f.AgentID = FollowUpRunning, agentID
		return &f, t, nil
	}
	return nil, nil, ErrNoTask
}

// WantsInterrupt reports whether the owner asked to stop this task's run in
// flight: an interrupting line no run has taken yet. The runner asks every
// few seconds while a run is open.
func (db *DB) WantsInterrupt(taskID string) bool {
	var n int
	_ = db.conn.QueryRow(`SELECT count(*) FROM task_followups WHERE task_id = ? AND state = ? AND mode = ?`,
		taskID, FollowUpQueued, FollowUpInterrupt).Scan(&n)
	return n > 0
}

// FinishFollowUp records how a follow-up turn ended.
func (db *DB) FinishFollowUp(id, state string) error {
	_, err := db.conn.Exec(`UPDATE task_followups SET state = ?, updated_at = ? WHERE id = ?`,
		state, ts(time.Now()), id)
	return err
}

// MarkFollowUpAnswered closes a follow-up that was answered another way — the
// owner's words unblocked a task that was waiting on them.
func (db *DB) MarkFollowUpAnswered(id, agentID string) error {
	_, err := db.conn.Exec(`UPDATE task_followups SET state = ?, agent_id = ?, updated_at = ? WHERE id = ?`,
		FollowUpAnswered, agentID, ts(time.Now()), id)
	return err
}

// SetTaskSessionRef records the CLI session a follow-up turn ended in, so the
// next turn resumes the same conversation. Only the ref: a follow-up does not
// move the task.
func (db *DB) SetTaskSessionRef(taskID string, ref SessionRef) error {
	if ref.SessionID == "" {
		return nil // nothing to resume; keep whatever was there
	}
	_, err := db.conn.Exec(`UPDATE tasks SET session_ref = ? WHERE id = ?`, ref.String(), taskID)
	return err
}
