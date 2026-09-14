package coord

import (
	"fmt"
	"time"
)

// RunInterrupted is a run that died with its gateway — a deploy, a restart, a
// crash. It is not a failure of the work and must not be counted as one.
const RunInterrupted = "interrupted"

// ReleaseInterruptedRuns puts back the work this agent was holding when it
// stopped.
//
// A run that dies with its process leaves two lies behind. The task_runs row
// says "running" although nothing is running, and the task says "working" with
// a ten-minute lease — so nothing touches it until that lease lapses, and then
// the reclaim charges it an attempt. Three deploys during one long task and
// the task is dead, having failed at nothing.
//
// Startup is the one moment this is unambiguously safe, the same reasoning as
// SweepProgressLines: the process has just begun, so any run still open under
// this agent's name is from an instance that no longer exists. Running it at
// shutdown too would be tidier, but a SIGKILL never gets there and this does.
//
// The attempt is given back. attempts is incremented at claim time, before any
// work happens, so an interrupted claim has consumed one without ever getting
// the chance to fail on its own terms. The task keeps its checkpoint and its
// CLI session, so the next run picks up where this one was — it loses the
// minutes, not the thread.
func (db *DB) ReleaseInterruptedRuns(agentID string) ([]string, error) {
	if agentID == "" {
		return nil, nil
	}
	rows, err := db.conn.Query(`SELECT task_id, id, attempt FROM task_runs
		WHERE liveness = ? AND agent_id = ?`, RunRunning, agentID)
	if err != nil {
		return nil, fmt.Errorf("interrupted runs of %s: %w", agentID, err)
	}
	type open struct{ taskID, runID string }
	var found []open
	for rows.Next() {
		var o open
		var attempt int
		if err := rows.Scan(&o.taskID, &o.runID, &attempt); err != nil {
			rows.Close()
			return nil, err
		}
		found = append(found, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	now := time.Now()
	var freed []string
	for _, o := range found {
		if _, err := db.conn.Exec(`UPDATE task_runs SET liveness = ?, ended_at = ?,
				note = 'gateway restarted while this run was in flight'
			WHERE id = ? AND liveness = ?`, RunInterrupted, ts(now), o.runID, RunRunning); err != nil {
			return freed, fmt.Errorf("close interrupted run %s: %w", o.runID, err)
		}
		// Only the task this agent is still holding. One that has already moved
		// on — canceled by a person, reclaimed elsewhere — is not ours to touch,
		// and the run row above is the whole correction it needed.
		res, err := db.conn.Exec(`UPDATE tasks SET
				state       = ?,
				lease_until = ?,
				attempts    = CASE WHEN attempts > 0 THEN attempts - 1 ELSE 0 END,
				updated_at  = ?
			WHERE id = ? AND state = ? AND claimed_by = ?`,
			TaskSubmitted, ts(now.Add(-time.Second)), ts(now), o.taskID, TaskWorking, agentID)
		if err != nil {
			return freed, fmt.Errorf("requeue %s: %w", o.taskID, err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue
		}
		// Said in the history, because "why did this stop for ten minutes" is
		// exactly the question the board could not answer.
		_ = db.appendEvent(o.taskID, agentID, TaskWorking, TaskSubmitted,
			"gateway restarted mid-run — requeued, and the attempt was given back")
		freed = append(freed, o.taskID)
	}
	return freed, nil
}
