package coord

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Run liveness: how ONE claim of a task ended. Deliberately a different
// vocabulary from tasks.state. A task is where the work stands; a run is one
// bounded attempt at it. Writing "failed" on the task because one 15-minute
// run hit its cap is exactly the confusion this separation exists to prevent.
// (Vocabulary after Paperclip's run liveness; timed_out/canceled added so the
// cause is kept — see docs/design/scheduling-and-long-tasks.md §5.1.)
const (
	RunRunning   = "running"
	RunCompleted = "completed" // the deliverable is in; task is done
	RunAdvanced  = "advanced"  // real progress, not finished; call me back
	RunPlanOnly  = "plan_only" // the agent described work instead of doing it
	RunEmpty     = "empty"     // nothing came back
	RunBlocked   = "blocked"   // waiting on children or a person
	RunFailed    = "failed"    // the run itself broke (CLI error, exception)
	RunTimedOut  = "timed_out" // hit the per-run cap
	RunCanceled  = "canceled"  // ctx cancelled: shutdown or a person stopping it
)

// TaskRun is one row of task_runs.
type TaskRun struct {
	ID        string    `json:"id"`
	TaskID    string    `json:"task_id"`
	AgentID   string    `json:"agent_id"`
	Attempt   int       `json:"attempt"`
	Liveness  string    `json:"liveness"`
	TraceID   string    `json:"trace_id,omitempty"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
	Note      string    `json:"note,omitempty"`
}

// RunOutcome is what the runner reports when a run ends.
type RunOutcome struct {
	Liveness   string
	Result     string     // the deliverable (completed) or the partial reply
	Checkpoint string     // progress note written this run, if any
	SessionRef SessionRef // the CLI session this run used; recorded for --resume
	BlockedOn  string     // for RunBlocked: children | human
	Note       string     // short human-readable reason (error text, cap hit…)
}

// ErrTaskFinished means the task reached a terminal state (canceled by a
// person, say) while the run was still going. The run is recorded; the task
// is left alone.
var ErrTaskFinished = errors.New("coord: task already finished; run recorded, task untouched")

// StartRun opens the ledger row for one claim. attempt is the fencing token
// ClaimTask returned.
func (db *DB) StartRun(taskID, agentID string, attempt int, traceID string) (*TaskRun, error) {
	r := &TaskRun{
		ID: "tr_" + uuid.NewString(), TaskID: taskID, AgentID: agentID,
		Attempt: attempt, Liveness: RunRunning, TraceID: traceID, StartedAt: time.Now(),
	}
	_, err := db.conn.Exec(`INSERT INTO task_runs
		(id, task_id, agent_id, attempt, liveness, trace_id, started_at, ended_at, note)
		VALUES (?, ?, ?, ?, ?, ?, ?, '', '')`,
		r.ID, r.TaskID, r.AgentID, r.Attempt, r.Liveness, r.TraceID, ts(r.StartedAt))
	if err != nil {
		return nil, fmt.Errorf("start run: %w", err)
	}
	return r, nil
}

// FinishRun closes a run and moves the task accordingly — in one transaction,
// so a crash between the two cannot leave a run that says "advanced" beside a
// task that still says "working".
//
// The fencing check is the same as FinishTask's: the task must still be held
// by this run's agent at this run's attempt. Otherwise the lease was lost and
// only the run row is written, with ErrLostLease returned so the caller drops
// its result. A task that a person finished meanwhile (canceled) is likewise
// left alone, with ErrTaskFinished.
//
// Transitions (docs/design/scheduling-and-long-tasks.md §5.3):
//
//	completed  → completed
//	advanced   → submitted, continuations+1, pinned to this agent (session lives here)
//	plan_only  → as advanced
//	empty      → as advanced; the maxEmptyRuns-th empty fails the task instead
//	timed_out  → as advanced when a checkpoint was written this run;
//	             otherwise submitted and the attempt is spent (ClaimTask counted it)
//	blocked    → blocked, lease released
//	failed     → submitted for retry, or failed(exhausted) at max_attempts
//	canceled   → submitted, attempt refunded: a shutdown must not cost a retry
func (db *DB) FinishRun(runID string, o RunOutcome) (*Task, error) {
	tx, err := db.conn.Begin()
	if err != nil {
		return nil, fmt.Errorf("finish run: begin: %w", err)
	}
	defer tx.Rollback()

	var run TaskRun
	if err := tx.QueryRow(`SELECT id, task_id, agent_id, attempt, liveness FROM task_runs WHERE id = ?`, runID).
		Scan(&run.ID, &run.TaskID, &run.AgentID, &run.Attempt, &run.Liveness); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("finish run: load run: %w", err)
	}
	if run.Liveness != RunRunning {
		return nil, fmt.Errorf("coord: run %s already ended as %s", runID, run.Liveness)
	}

	t, err := scanTask(tx.QueryRow(`SELECT `+taskCols+` FROM tasks WHERE id = ?`, run.TaskID))
	if err != nil {
		return nil, fmt.Errorf("finish run: load task: %w", err)
	}

	now := time.Now()
	closeRun := func(liveness, note string) error {
		_, err := tx.Exec(`UPDATE task_runs SET liveness = ?, ended_at = ?, note = ? WHERE id = ?`,
			liveness, ts(now), truncateNote(note), runID)
		return err
	}
	event := func(from, to, note string) {
		_, _ = tx.Exec(`INSERT INTO task_events (task_id, agent_id, from_state, to_state, note, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`, t.ID, run.AgentID, from, to, note, ts(now))
	}

	// A person finished the task while the run was going. Record the run,
	// leave the task exactly as they set it.
	if isTerminal(t.State) {
		if err := closeRun(o.Liveness, "task was already "+t.State); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return t, ErrTaskFinished
	}

	// The agent parked the task itself with `bomclaw task block` during this
	// run: the task is already where it should be, only the run needs closing.
	if t.State == TaskBlocked && t.ClaimedBy == run.AgentID && t.Attempts == run.Attempt {
		if err := closeRun(RunBlocked, "blocked on "+t.BlockedOn); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return t, nil
	}

	// Fencing: still ours?
	if t.ClaimedBy != run.AgentID || t.Attempts != run.Attempt || t.State != TaskWorking {
		if err := closeRun(o.Liveness, "lease was lost; result discarded"); err != nil {
			return nil, err
		}
		event(TaskWorking, TaskWorking, "result discarded: lease was lost to another agent")
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return t, ErrLostLease
	}

	// Things every non-terminal outcome may carry.
	if o.Checkpoint != "" {
		t.Checkpoint = o.Checkpoint
	}
	if o.SessionRef.SessionID != "" {
		t.SessionRef = o.SessionRef.String()
	}

	// notDone is the shared path for "call me back": the task goes back to the
	// queue, pinned to this agent because the CLI session it would resume lives
	// in this agent's config directory. RelaxDeadAssignments lifts the pin if
	// the agent dies.
	notDone := func(reason string) error {
		t.Continuations++
		if t.Continuations >= t.MaxContinuations {
			t.State, t.FailReason = TaskFailed, FailContinuationsExhausted
			event(TaskWorking, TaskFailed, fmt.Sprintf("continuations exhausted after %d runs; last checkpoint kept", t.Continuations))
			return nil
		}
		t.State, t.AssignedTo, t.LeaseUntil = TaskSubmitted, run.AgentID, now
		event(TaskWorking, TaskSubmitted, reason)
		return nil
	}

	liveness := o.Liveness
	switch liveness {
	case RunCompleted:
		t.State, t.Result, t.FailReason = TaskCompleted, o.Result, ""
		event(TaskWorking, TaskCompleted, "")

	case RunAdvanced:
		_ = notDone(fmt.Sprintf("advanced (continuation %d)", t.Continuations+1))

	case RunPlanOnly:
		_ = notDone(fmt.Sprintf("plan only, no work done (continuation %d)", t.Continuations+1))

	case RunEmpty:
		var empties int
		_ = tx.QueryRow(`SELECT count(*) FROM task_runs WHERE task_id = ? AND liveness = ?`,
			t.ID, RunEmpty).Scan(&empties)
		if empties+1 >= maxEmptyRuns {
			t.State, t.FailReason = TaskFailed, FailEmptyExhausted
			event(TaskWorking, TaskFailed, fmt.Sprintf("%d empty replies in a row", empties+1))
		} else {
			_ = notDone("empty reply; trying once more")
		}

	case RunTimedOut:
		if o.Checkpoint != "" {
			// It was working and said so: not a failure, a long job.
			_ = notDone(fmt.Sprintf("run hit its time cap with progress recorded (continuation %d)", t.Continuations+1))
		} else if t.Attempts >= t.MaxAttempts {
			t.State, t.FailReason = TaskFailed, FailExhausted
			event(TaskWorking, TaskFailed, "timed out with no progress on the last attempt")
		} else {
			t.State, t.LeaseUntil, t.AssignedTo = TaskSubmitted, now, run.AgentID
			event(TaskWorking, TaskSubmitted, fmt.Sprintf("timed out with no progress (attempt %d/%d)", t.Attempts, t.MaxAttempts))
		}

	case RunBlocked:
		t.State, t.BlockedOn, t.LeaseUntil = TaskBlocked, o.BlockedOn, now
		if t.BlockedOn == "" {
			t.BlockedOn = BlockedOnHuman
		}
		event(TaskWorking, TaskBlocked, "blocked on "+t.BlockedOn+": "+truncateNote(o.Note))

	case RunFailed:
		if t.Attempts >= t.MaxAttempts {
			t.State, t.FailReason, t.Result = TaskFailed, FailExhausted, o.Result
			event(TaskWorking, TaskFailed, "exhausted: "+truncateNote(o.Note))
		} else {
			t.State, t.LeaseUntil = TaskSubmitted, now
			event(TaskWorking, TaskSubmitted, fmt.Sprintf("run failed (attempt %d/%d): %s", t.Attempts, t.MaxAttempts, truncateNote(o.Note)))
		}

	case RunCanceled:
		// The run never really happened; give the attempt back so a gateway
		// restart is free instead of costing one of three retries.
		t.State, t.LeaseUntil = TaskSubmitted, now
		if t.Attempts > 0 {
			t.Attempts--
		}
		event(TaskWorking, TaskSubmitted, "run canceled; attempt refunded")

	default:
		return nil, fmt.Errorf("coord: %q is not a run liveness", liveness)
	}

	if _, err := tx.Exec(`UPDATE tasks SET
			state = ?, result = ?, assigned_to = ?, lease_until = ?, attempts = ?,
			checkpoint = ?, session_ref = ?, continuations = ?, blocked_on = ?,
			fail_reason = ?, updated_at = ?
		WHERE id = ?`,
		t.State, t.Result, t.AssignedTo, ts(t.LeaseUntil), t.Attempts,
		t.Checkpoint, t.SessionRef, t.Continuations, t.BlockedOn,
		t.FailReason, ts(now), t.ID); err != nil {
		return nil, fmt.Errorf("finish run: update task: %w", err)
	}
	if err := closeRun(liveness, o.Note); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("finish run: commit: %w", err)
	}
	t.UpdatedAt = now
	return t, nil
}

// TaskRuns lists a task's runs, oldest first.
func (db *DB) TaskRuns(taskID string) ([]TaskRun, error) {
	rows, err := db.conn.Query(`SELECT id, task_id, agent_id, attempt, liveness, trace_id,
		started_at, ended_at, note FROM task_runs WHERE task_id = ? ORDER BY started_at`, taskID)
	if err != nil {
		return nil, fmt.Errorf("task runs: %w", err)
	}
	defer rows.Close()
	out := []TaskRun{}
	for rows.Next() {
		var r TaskRun
		var started, ended string
		if err := rows.Scan(&r.ID, &r.TaskID, &r.AgentID, &r.Attempt, &r.Liveness, &r.TraceID,
			&started, &ended, &r.Note); err != nil {
			return nil, err
		}
		r.StartedAt, r.EndedAt = parseTS(started), parseTS(ended)
		out = append(out, r)
	}
	return out, rows.Err()
}

// PurgeTaskRunsBefore removes run rows older than cutoff for tasks that are
// finished. Runs of open tasks are kept: they are the task's memory.
func (db *DB) PurgeTaskRunsBefore(cutoff time.Time) (int64, error) {
	res, err := db.conn.Exec(`DELETE FROM task_runs WHERE started_at < ?
		AND task_id IN (SELECT id FROM tasks WHERE state IN (?, ?, ?, ?))`,
		ts(cutoff), TaskCompleted, TaskFailed, TaskCanceled, TaskRejected)
	if err != nil {
		return 0, fmt.Errorf("purge task runs: %w", err)
	}
	return res.RowsAffected()
}

func isTerminal(state string) bool {
	switch state {
	case TaskCompleted, TaskFailed, TaskCanceled, TaskRejected:
		return true
	}
	return false
}
