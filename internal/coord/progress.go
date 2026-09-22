package coord

import (
	"fmt"
	"time"
)

// How far along a goal is.
//
// Per task the system has always been well instrumented — events, runs, traces,
// a live progress line. Per TREE it had one number, `SELECT count(*)`, and no
// way to filter a listing by context at all. So "how far along is this goal"
// could only be answered by walking parent_id by hand, which meant in practice
// that nobody answered it: not the person at the board, and not the agent that
// had just been woken with a wave of results and had to decide whether to split
// again or finish.
//
// This is that answer as data. It is the input to both.

// ContextProgress is the state of one task tree.
type ContextProgress struct {
	ContextID string `json:"context_id"`
	// Goal is the root task — the thing the tree exists to finish.
	Goal *Task `json:"goal,omitempty"`
	// Total counts every task in the tree, finished ones included, because
	// that is what MaxTasksPerContext counts.
	Total int `json:"total"`
	// ByState is every state present, so a caller can render without guessing
	// which ones exist.
	ByState map[string]int `json:"by_state"`
	// Open is the frontier: the tasks that are not in a terminal state,
	// oldest first. Empty means the tree has stopped moving — which is not the
	// same as the goal being met.
	Open []Task `json:"open,omitempty"`
	// Runs is how many attempts the whole tree has cost. This is the number a
	// per-goal budget is measured against: the per-task caps cannot see it.
	Runs int `json:"runs"`
	// LastMovedAt is when anything in the tree last changed state.
	LastMovedAt time.Time `json:"last_moved_at,omitempty"`
}

// Done reports whether every task in the tree has stopped.
//
// Deliberately NOT called "complete": an empty frontier means the decomposition
// that exists has been exhausted, which is only the goal if the decomposition
// was right and nothing was discovered along the way. Treating the two as the
// same thing is the single most common way these systems stop early.
func (p ContextProgress) Done() bool { return p.Total > 0 && len(p.Open) == 0 }

// ContextProgress reads one tree.
func (db *DB) ContextProgress(contextID string) (*ContextProgress, error) {
	if contextID == "" {
		return nil, fmt.Errorf("coord: a context id is required")
	}
	p := &ContextProgress{ContextID: contextID, ByState: map[string]int{}}

	rows, err := db.conn.Query(`SELECT `+taskCols+` FROM tasks
		WHERE context_id = ? ORDER BY created_at`, contextID)
	if err != nil {
		return nil, fmt.Errorf("progress of %s: %w", contextID, err)
	}
	defer rows.Close()
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		p.Total++
		p.ByState[t.State]++
		if t.ParentID == "" && p.Goal == nil {
			p.Goal = t
		}
		if !isTerminal(t.State) {
			p.Open = append(p.Open, *t)
		}
		if t.UpdatedAt.After(p.LastMovedAt) {
			p.LastMovedAt = t.UpdatedAt
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	runs, err := db.ContextRuns(contextID)
	if err != nil {
		return nil, err
	}
	p.Runs = runs
	return p, nil
}

// ContextRuns counts the attempts every task in a tree has taken.
//
// The existing ceilings are all per task (MaxAttempts, MaxContinuations) or on
// the shape of the tree (MaxOpenChildren, MaxDepth, MaxTasksPerContext). None
// of them can see a tree that keeps splitting, each split legal, until the
// quota is gone. This is the number that can.
func (db *DB) ContextRuns(contextID string) (int, error) {
	var n int
	err := db.conn.QueryRow(`SELECT count(*) FROM task_runs r
		JOIN tasks t ON t.id = r.task_id
		WHERE t.context_id = ?`, contextID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("runs in %s: %w", contextID, err)
	}
	return n, nil
}
