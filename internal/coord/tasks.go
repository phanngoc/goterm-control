package coord

import (
	"database/sql"
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
	TaskInputRequired = "input-required"
)

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
}

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
		ID:          "t_" + uuid.NewString(),
		ContextID:   n.ContextID,
		CreatedBy:   n.CreatedBy,
		AssignedTo:  n.AssignedTo,
		State:       TaskSubmitted,
		Priority:    n.Priority,
		Title:       n.Title,
		Body:        n.Body,
		LeaseUntil:  now, // already in the past ⇒ immediately claimable
		MaxAttempts: 3,
		Depth:       n.Depth,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if t.ContextID == "" {
		t.ContextID = "ctx_" + uuid.NewString()
	}

	_, err := db.conn.Exec(`INSERT INTO tasks
		(id, context_id, created_by, assigned_to, claimed_by, state, priority,
		 title, body, result, trace_id, lease_until, attempts, max_attempts, depth,
		 created_at, updated_at)
		VALUES (?, ?, ?, ?, '', ?, ?, ?, ?, '', '', ?, 0, ?, ?, ?, ?)`,
		t.ID, t.ContextID, t.CreatedBy, t.AssignedTo, t.State, t.Priority,
		t.Title, t.Body, ts(t.LeaseUntil), t.MaxAttempts, t.Depth,
		ts(t.CreatedAt), ts(t.UpdatedAt))
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
		RETURNING id, context_id, created_by, assigned_to, claimed_by, state,
		          priority, title, body, result, trace_id, lease_until, attempts,
		          max_attempts, depth, created_at, updated_at`,
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
		WHERE id = ? AND state IN (?, ?, ?)`,
		TaskCanceled, ts(time.Now()), taskID, TaskSubmitted, TaskWorking, TaskInputRequired)
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

	rows, err := db.conn.Query(`SELECT id, context_id, created_by, assigned_to,
		claimed_by, state, priority, title, body, result, trace_id, lease_until,
		attempts, max_attempts, depth, created_at, updated_at
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
		&lease, &t.Attempts, &t.MaxAttempts, &t.Depth, &created, &updated); err != nil {
		return nil, err
	}
	t.LeaseUntil = parseTS(lease)
	t.CreatedAt = parseTS(created)
	t.UpdatedAt = parseTS(updated)
	return &t, nil
}
