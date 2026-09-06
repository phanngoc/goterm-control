package coord

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Task states, following the A2A vocabulary.
const (
	TaskSubmitted     = "submitted"
	TaskWorking       = "working"
	TaskCompleted     = "completed"
	TaskFailed        = "failed"
	TaskCanceled      = "canceled"
	TaskRejected      = "rejected"
	TaskInputRequired = "input-required" // legacy; new code uses TaskBlocked + BlockedOn
	TaskBlocked       = "blocked"        // waiting on children or a human; not claimable
)

// Task kinds: how a task came to exist.
const (
	KindManual    = "manual"    // a person or an agent queued it
	KindScheduled = "scheduled" // materialised from a schedule
	KindHeartbeat = "heartbeat"
	KindSub       = "sub" // a child of another task
)

// What a task can be blocked on.
const (
	BlockedOnChildren = "children"
	BlockedOnHuman    = "human"
)

// Fail reasons: why the system, rather than the agent, gave up.
const (
	FailExhausted              = "exhausted"               // max_attempts of runtime failures
	FailContinuationsExhausted = "continuations-exhausted" // ran max_continuations times without finishing
	FailEmptyExhausted         = "empty-exhausted"         // kept returning nothing
)

// DefaultMaxContinuations bounds how many "not done yet" runs a task may take.
// At the default 15-minute run cap this is roughly five hours of actual work.
const DefaultMaxContinuations = 20

// maxEmptyRuns is how many empty replies a task tolerates before it is failed:
// an agent that returns nothing twice is not going to return something.
const maxEmptyRuns = 2

// DefaultLease is how long a claim holds a task before other agents may take
// it. An agent that dies mid-task simply stops renewing and the work returns.
const DefaultLease = 10 * time.Minute

// MaxDepth caps how deep a chain of agent-created tasks may go. Two agents
// that can hand each other work will ping-pong forever without this.
const MaxDepth = 5

// ErrNoTask means nothing was claimable, not that anything went wrong.
var ErrNoTask = errors.New("coord: no claimable task")

// ErrLostLease means the task was reassigned while this agent was working on
// it; the agent must discard its result rather than overwrite the new owner's.
var ErrLostLease = errors.New("coord: lease lost, result discarded")

// Task is one unit of work, optionally handed from one agent to another.
type Task struct {
	ID          string    `json:"id"`
	ContextID   string    `json:"context_id"`
	CreatedBy   string    `json:"created_by"`
	AssignedTo  string    `json:"assigned_to,omitempty"`
	ClaimedBy   string    `json:"claimed_by,omitempty"`
	State       string    `json:"state"`
	Priority    int       `json:"priority"`
	Title       string    `json:"title"`
	Body        string    `json:"body,omitempty"`
	Result      string    `json:"result,omitempty"`
	TraceID     string    `json:"trace_id,omitempty"`
	LeaseUntil  time.Time `json:"lease_until"`
	Attempts    int       `json:"attempts"`
	MaxAttempts int       `json:"max_attempts"`
	Depth       int       `json:"depth"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`

	// A task is many runs; these survive between them.
	ParentID         string `json:"parent_id,omitempty"`
	Kind             string `json:"kind"`
	ScheduleID       string `json:"schedule_id,omitempty"`
	Checkpoint       string `json:"checkpoint,omitempty"`  // latest progress note, fed to the next run
	SessionRef       string `json:"session_ref,omitempty"` // JSON SessionRef; the CLI session to --resume
	Continuations    int    `json:"continuations"`
	MaxContinuations int    `json:"max_continuations"`
	BlockedOn        string `json:"blocked_on,omitempty"`
	FailReason       string `json:"fail_reason,omitempty"`
}

// SessionRef names the CLI session a task's work lives in. Both CLIs keep the
// session inside the account's config directory, so a continuation can only
// --resume it from the same agent and account — which is why all three are
// recorded, and why a task with a SessionRef is soft-pinned to its agent.
type SessionRef struct {
	Provider  string `json:"provider"`
	SessionID string `json:"session_id"`
	Account   string `json:"account,omitempty"`
}

// ParseSessionRef decodes tasks.session_ref; empty or malformed yields a zero ref.
func ParseSessionRef(raw string) SessionRef {
	var r SessionRef
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &r)
	}
	return r
}

// String encodes the ref for storage.
func (r SessionRef) String() string {
	if r.SessionID == "" {
		return ""
	}
	b, _ := json.Marshal(r)
	return string(b)
}

// taskCols is every column of tasks in scanTask order. One list, so a column
// added in a migration cannot be read by one query and forgotten by another.
const taskCols = `id, context_id, created_by, assigned_to, claimed_by, state,
	priority, title, body, result, trace_id, lease_until, attempts,
	max_attempts, depth, created_at, updated_at,
	parent_id, kind, schedule_id, checkpoint, session_ref, continuations,
	max_continuations, blocked_on, fail_reason`

// TaskEvent is an append-only record of one state transition.
type TaskEvent struct {
	ID        int64     `json:"id"`
	TaskID    string    `json:"task_id"`
	AgentID   string    `json:"agent_id"`
	FromState string    `json:"from_state,omitempty"`
	ToState   string    `json:"to_state"`
	Note      string    `json:"note,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// NewTask describes work to create.
type NewTask struct {
	CreatedBy  string
	AssignedTo string // "" = any agent may claim it
	Title      string
	Body       string
	Priority   int
	ContextID  string // "" starts a new chain
	Depth      int    // parent depth + 1 when an agent spawns follow-up work
	ParentID   string // set for a child task; see KindSub
	Kind       string // "" = KindManual
	ScheduleID string
}

// CreateTask records new work. It refuses to go past MaxDepth so a pair of
// agents cannot drive each other in an endless loop.
func (db *DB) CreateTask(n NewTask) (*Task, error) {
	if strings.TrimSpace(n.Title) == "" {
		return nil, fmt.Errorf("coord: task title is required")
	}
	if n.Depth > MaxDepth {
		return nil, fmt.Errorf("coord: task depth %d exceeds max %d — refusing to extend the chain", n.Depth, MaxDepth)
	}

	now := time.Now()
	t := &Task{
		ID:               "t_" + uuid.NewString(),
		ContextID:        n.ContextID,
		CreatedBy:        n.CreatedBy,
		AssignedTo:       n.AssignedTo,
		State:            TaskSubmitted,
		Priority:         n.Priority,
		Title:            n.Title,
		Body:             n.Body,
		LeaseUntil:       now, // already in the past ⇒ immediately claimable
		MaxAttempts:      3,
		Depth:            n.Depth,
		CreatedAt:        now,
		UpdatedAt:        now,
		ParentID:         n.ParentID,
		Kind:             n.Kind,
		ScheduleID:       n.ScheduleID,
		MaxContinuations: DefaultMaxContinuations,
	}
	if t.ContextID == "" {
		t.ContextID = "ctx_" + uuid.NewString()
	}
	if t.Kind == "" {
		t.Kind = KindManual
	}

	_, err := db.conn.Exec(`INSERT INTO tasks
		(id, context_id, created_by, assigned_to, claimed_by, state, priority,
		 title, body, result, trace_id, lease_until, attempts, max_attempts, depth,
		 created_at, updated_at,
		 parent_id, kind, schedule_id, checkpoint, session_ref, continuations,
		 max_continuations, blocked_on, fail_reason)
		VALUES (?, ?, ?, ?, '', ?, ?, ?, ?, '', '', ?, 0, ?, ?, ?, ?,
		        ?, ?, ?, '', '', 0, ?, '', '')`,
		t.ID, t.ContextID, t.CreatedBy, t.AssignedTo, t.State, t.Priority,
		t.Title, t.Body, ts(t.LeaseUntil), t.MaxAttempts, t.Depth,
		ts(t.CreatedAt), ts(t.UpdatedAt),
		t.ParentID, t.Kind, t.ScheduleID, t.MaxContinuations)
	if err != nil {
		return nil, fmt.Errorf("create task: %w", err)
	}
	_ = db.appendEvent(t.ID, n.CreatedBy, "", TaskSubmitted, "created")
	return t, nil
}

// ClaimTask atomically takes the next claimable task for agentID.
//
// The whole selection and claim is ONE statement on purpose: a SELECT followed
// by an UPDATE would let two agents pick the same row. SQLite's single-writer
// rule makes this statement the serialisation point.
//
// A task is claimable when its lease has expired — which covers both "never
// started" (lease set in the past at creation) and "the agent holding it died".
func (db *DB) ClaimTask(agentID string) (*Task, error) {
	now := time.Now()
	row := db.conn.QueryRow(`UPDATE tasks SET
			state       = ?,
			claimed_by  = ?,
			lease_until = ?,
			attempts    = attempts + 1,
			updated_at  = ?
		WHERE id = (
			SELECT id FROM tasks
			WHERE state IN (?, ?)
			  AND lease_until <= ?
			  AND attempts < max_attempts
			  AND (assigned_to = '' OR assigned_to = ?)
			ORDER BY priority DESC, created_at
			LIMIT 1
		)
		RETURNING `+taskCols,
		TaskWorking, agentID, ts(now.Add(DefaultLease)), ts(now),
		TaskSubmitted, TaskWorking, ts(now), agentID)

	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoTask
	}
	if err != nil {
		return nil, fmt.Errorf("claim task: %w", err)
	}
	_ = db.appendEvent(t.ID, agentID, TaskSubmitted, TaskWorking,
		fmt.Sprintf("claimed (attempt %d)", t.Attempts))
	return t, nil
}

// RenewLease pushes a held task's lease out. An agent running a long task must
// call this periodically or the work will be handed to someone else.
func (db *DB) RenewLease(taskID, agentID string) error {
	res, err := db.conn.Exec(`UPDATE tasks SET lease_until = ?, updated_at = ?
		WHERE id = ? AND claimed_by = ? AND state = ?`,
		ts(time.Now().Add(DefaultLease)), ts(time.Now()), taskID, agentID, TaskWorking)
	if err != nil {
		return fmt.Errorf("renew lease: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrLostLease
	}
	return nil
}

// FinishTask closes a task the agent holds. state must be a terminal state.
// attempts is the value seen at claim time and acts as a fencing token: if the
// task was reassigned meanwhile the update matches nothing and the caller is
// told to drop its result instead of overwriting the new owner's work.
func (db *DB) FinishTask(taskID, agentID, state, result string, attempts int) error {
	switch state {
	case TaskCompleted, TaskFailed, TaskCanceled, TaskRejected, TaskInputRequired:
	default:
		return fmt.Errorf("coord: %q is not a terminal state", state)
	}

	res, err := db.conn.Exec(`UPDATE tasks SET state = ?, result = ?, updated_at = ?
		WHERE id = ? AND claimed_by = ? AND attempts = ?`,
		state, result, ts(time.Now()), taskID, agentID, attempts)
	if err != nil {
		return fmt.Errorf("finish task: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		_ = db.appendEvent(taskID, agentID, TaskWorking, TaskWorking,
			"result discarded: lease was lost to another agent")
		return ErrLostLease
	}
	_ = db.appendEvent(taskID, agentID, TaskWorking, state, "")
	return nil
}

// AttachTrace links a task to the trace of the run that executed it, so the
// admin page can jump from a task straight to its waterfall.
func (db *DB) AttachTrace(taskID, traceID string) error {
	_, err := db.conn.Exec(`UPDATE tasks SET trace_id = ? WHERE id = ?`, traceID, taskID)
	return err
}

// CancelTask stops a task from any non-terminal state. Used by the dashboard,
// which is not the claiming agent and so carries no fencing token.
func (db *DB) CancelTask(taskID, byAgent string) error {
	res, err := db.conn.Exec(`UPDATE tasks SET state = ?, updated_at = ?
		WHERE id = ? AND state IN (?, ?, ?, ?)`,
		TaskCanceled, ts(time.Now()), taskID, TaskSubmitted, TaskWorking, TaskInputRequired, TaskBlocked)
	if err != nil {
		return fmt.Errorf("cancel task: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("coord: task %s is already finished", taskID)
	}
	return db.appendEvent(taskID, byAgent, "", TaskCanceled, "canceled")
}

// TaskFilter narrows a task listing.
type TaskFilter struct {
	State   string
	AgentID string // matches either the creator or the claimer
	Limit   int
}

// ListTasks returns tasks newest first.
func (db *DB) ListTasks(f TaskFilter) ([]Task, error) {
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	where := []string{"1=1"}
	args := []any{}
	if f.State != "" {
		where = append(where, "state = ?")
		args = append(args, f.State)
	}
	if f.AgentID != "" {
		where = append(where, "(created_by = ? OR claimed_by = ? OR assigned_to = ?)")
		args = append(args, f.AgentID, f.AgentID, f.AgentID)
	}
	args = append(args, limit)

	rows, err := db.conn.Query(`SELECT `+taskCols+`
		FROM tasks WHERE `+strings.Join(where, " AND ")+`
		ORDER BY created_at DESC LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()

	out := []Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// TaskEvents returns the audit trail for one task, oldest first.
func (db *DB) TaskEvents(taskID string) ([]TaskEvent, error) {
	rows, err := db.conn.Query(`SELECT id, task_id, agent_id, from_state, to_state, note, created_at
		FROM task_events WHERE task_id = ? ORDER BY id`, taskID)
	if err != nil {
		return nil, fmt.Errorf("task events: %w", err)
	}
	defer rows.Close()

	out := []TaskEvent{}
	for rows.Next() {
		var e TaskEvent
		var created string
		if err := rows.Scan(&e.ID, &e.TaskID, &e.AgentID, &e.FromState, &e.ToState, &e.Note, &created); err != nil {
			return nil, fmt.Errorf("scan task event: %w", err)
		}
		e.CreatedAt = parseTS(created)
		out = append(out, e)
	}
	return out, rows.Err()
}

// SetCheckpoint records progress on a task the agent currently holds. Fenced
// by attempts like FinishTask: a note from a lease that was lost must not land
// on the new owner's task.
func (db *DB) SetCheckpoint(taskID, agentID string, attempts int, note string) error {
	note = strings.TrimSpace(note)
	if note == "" {
		return fmt.Errorf("coord: checkpoint note is required")
	}
	res, err := db.conn.Exec(`UPDATE tasks SET checkpoint = ?, updated_at = ?
		WHERE id = ? AND claimed_by = ? AND attempts = ? AND state = ?`,
		note, ts(time.Now()), taskID, agentID, attempts, TaskWorking)
	if err != nil {
		return fmt.Errorf("set checkpoint: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrLostLease
	}
	return db.appendEvent(taskID, agentID, TaskWorking, TaskWorking, "checkpoint: "+truncateNote(note))
}

// SetSessionRef records which CLI session a task's work lives in, so a later
// run can --resume it. Fenced the same way.
func (db *DB) SetSessionRef(taskID, agentID string, attempts int, ref SessionRef) error {
	res, err := db.conn.Exec(`UPDATE tasks SET session_ref = ?, updated_at = ?
		WHERE id = ? AND claimed_by = ? AND attempts = ?`,
		ref.String(), ts(time.Now()), taskID, agentID, attempts)
	if err != nil {
		return fmt.Errorf("set session ref: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrLostLease
	}
	return nil
}

// BlockTask parks a task the agent holds until a person or its children free
// it. The agent types this from inside a run (`bomclaw task block`); the run
// then ends and FinishRun records it as blocked without touching the task
// again. Fenced like every other write from a holder.
func (db *DB) BlockTask(taskID, agentID string, attempts int, on, note string) error {
	switch on {
	case BlockedOnChildren, BlockedOnHuman:
	default:
		return fmt.Errorf("coord: blocked_on must be %q or %q", BlockedOnChildren, BlockedOnHuman)
	}
	now := time.Now()
	res, err := db.conn.Exec(`UPDATE tasks SET state = ?, blocked_on = ?, lease_until = ?,
			checkpoint = CASE WHEN ? != '' THEN ? ELSE checkpoint END, updated_at = ?
		WHERE id = ? AND claimed_by = ? AND attempts = ? AND state = ?`,
		TaskBlocked, on, ts(now), note, note, ts(now), taskID, agentID, attempts, TaskWorking)
	if err != nil {
		return fmt.Errorf("block task: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrLostLease
	}
	return db.appendEvent(taskID, agentID, TaskWorking, TaskBlocked, "blocked on "+on+": "+truncateNote(note))
}

// UnblockTask returns a blocked task to the queue — a person answering, or the
// last child finishing. Not fenced: nobody holds a blocked task.
func (db *DB) UnblockTask(taskID, byAgent, note string) error {
	now := time.Now()
	res, err := db.conn.Exec(`UPDATE tasks SET state = ?, blocked_on = '', lease_until = ?, updated_at = ?
		WHERE id = ? AND state = ?`, TaskSubmitted, ts(now), ts(now), taskID, TaskBlocked)
	if err != nil {
		return fmt.Errorf("unblock task: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("coord: task %s is not blocked", taskID)
	}
	return db.appendEvent(taskID, byAgent, TaskBlocked, TaskSubmitted, "unblocked: "+truncateNote(note))
}

// ReapExhausted moves tasks that have used every attempt into failed. Until
// now they sat in `working` with an expired lease: unclaimable (ClaimTask
// filters on attempts), yet counted as open and shown as "will be reclaimed".
// Returns the ids it failed.
func (db *DB) ReapExhausted() ([]string, error) {
	now := ts(time.Now())
	rows, err := db.conn.Query(`UPDATE tasks SET state = ?, fail_reason = ?, updated_at = ?
		WHERE state = ? AND lease_until <= ? AND attempts >= max_attempts
		RETURNING id`,
		TaskFailed, FailExhausted, now, TaskWorking, now)
	if err != nil {
		return nil, fmt.Errorf("reap exhausted: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return ids, err
		}
		ids = append(ids, id)
	}
	for _, id := range ids {
		_ = db.appendEvent(id, "system", TaskWorking, TaskFailed,
			"exhausted: every attempt ended without a result")
	}
	return ids, rows.Err()
}

// RelaxDeadAssignments frees tasks addressed to an agent that has stopped
// heartbeating: assigned_to goes back to "anyone", and the session ref is
// dropped because no other agent can resume a session that lives in the dead
// agent's config directory — the next run starts from the checkpoint instead.
// Returns the ids it relaxed.
func (db *DB) RelaxDeadAssignments(staleAfter time.Duration) ([]string, error) {
	cutoff := ts(time.Now().Add(-staleAfter))
	rows, err := db.conn.Query(`UPDATE tasks SET assigned_to = '', session_ref = '', updated_at = ?
		WHERE state = ? AND assigned_to != ''
		  AND assigned_to IN (SELECT id FROM agents WHERE last_seen_at < ?)
		RETURNING id, assigned_to`,
		ts(time.Now()), TaskSubmitted, cutoff)
	if err != nil {
		return nil, fmt.Errorf("relax dead assignments: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id, was string
		if err := rows.Scan(&id, &was); err != nil {
			return ids, err
		}
		ids = append(ids, id)
	}
	for _, id := range ids {
		_ = db.appendEvent(id, "system", TaskSubmitted, TaskSubmitted,
			"assignee stopped heartbeating; task opened to any agent, session dropped")
	}
	return ids, rows.Err()
}

// ResumeTask reopens a task the system gave up on, granting `more` further
// attempts or continuations depending on why it stopped. This is a person's
// decision, so it is not fenced.
func (db *DB) ResumeTask(taskID, byAgent string, more int) error {
	if more <= 0 {
		more = 5
	}
	t, err := db.GetTask(taskID)
	if err != nil {
		return err
	}
	if t.State != TaskFailed {
		return fmt.Errorf("coord: task %s is %s, only a failed task can be resumed", taskID, t.State)
	}
	set := "max_attempts = max_attempts + ?"
	switch t.FailReason {
	case FailContinuationsExhausted:
		set = "max_continuations = max_continuations + ?"
	case FailEmptyExhausted:
		// Empties are not counted on the task; just reopen and let the run
		// counter start over from this point.
		set = "max_continuations = max_continuations + ?"
	}
	_, err = db.conn.Exec(`UPDATE tasks SET state = ?, fail_reason = '', lease_until = ?, updated_at = ?, `+set+`
		WHERE id = ?`, TaskSubmitted, ts(time.Now()), ts(time.Now()), more, taskID)
	if err != nil {
		return fmt.Errorf("resume task: %w", err)
	}
	return db.appendEvent(taskID, byAgent, TaskFailed, TaskSubmitted,
		fmt.Sprintf("resumed by %s (+%d)", byAgent, more))
}

func truncateNote(s string) string {
	if r := []rune(s); len(r) > 200 {
		return string(r[:199]) + "…"
	}
	return s
}

func (db *DB) appendEvent(taskID, agentID, from, to, note string) error {
	_, err := db.conn.Exec(`INSERT INTO task_events
		(task_id, agent_id, from_state, to_state, note, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		taskID, agentID, from, to, note, ts(time.Now()))
	return err
}

// scanner covers both *sql.Row and *sql.Rows.
type scanner interface{ Scan(dest ...any) error }

func scanTask(s scanner) (*Task, error) {
	var t Task
	var lease, created, updated string
	if err := s.Scan(&t.ID, &t.ContextID, &t.CreatedBy, &t.AssignedTo, &t.ClaimedBy,
		&t.State, &t.Priority, &t.Title, &t.Body, &t.Result, &t.TraceID,
		&lease, &t.Attempts, &t.MaxAttempts, &t.Depth, &created, &updated,
		&t.ParentID, &t.Kind, &t.ScheduleID, &t.Checkpoint, &t.SessionRef, &t.Continuations,
		&t.MaxContinuations, &t.BlockedOn, &t.FailReason); err != nil {
		return nil, err
	}
	t.LeaseUntil = parseTS(lease)
	t.CreatedAt = parseTS(created)
	t.UpdatedAt = parseTS(updated)
	return &t, nil
}
