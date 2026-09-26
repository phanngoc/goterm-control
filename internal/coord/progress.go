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

// DefaultRunsPerGoal is how many runs one tree may cost before it stops to ask
// a person. Measured rather than guessed: the heaviest tree this machine has
// run took 11 runs and the average is 2.2, so this is about four times the
// worst case seen.
const DefaultRunsPerGoal = 40

// SetRunsPerGoal overrides the ceiling. Config calls it at startup; a negative
// value removes it.
func (db *DB) SetRunsPerGoal(n int) { db.runsPerGoal = n }

// RunsPerGoal is the ceiling in force. Zero from config means "unset", so it
// resolves to the default; a caller wanting no ceiling passes a negative.
func (db *DB) RunsPerGoal() int {
	if db.runsPerGoal == 0 {
		return DefaultRunsPerGoal
	}
	return db.runsPerGoal
}

// DefaultGoalExtensions is how many further budgets a goal may grant itself
// while it is still producing something. Three, so a tree that keeps working
// can reach four times the base before anyone is asked — and no further.
const DefaultGoalExtensions = 3

// SetGoalExtensions overrides how many times a working goal may extend itself.
// Zero means it never does: the base budget is the whole budget.
func (db *DB) SetGoalExtensions(n int) { db.goalExtensions, db.goalExtensionsSet = n, true }

// GoalExtensions is the allowance in force.
func (db *DB) GoalExtensions() int {
	if db.goalExtensions == 0 && !db.goalExtensionsSet {
		return DefaultGoalExtensions
	}
	return db.goalExtensions
}

// GoalBudgetSpent reports whether a tree has used up its run budget, and what
// it has spent. A tree with no ceiling never has.
//
// A goal that is still producing extends itself rather than stopping. The
// budget exists because three agents share one quota and a tree that splits
// forever is a real way to lose it — but the thing worth stopping is a goal
// going in circles, not a goal that is working and happens to be long. Stopping
// a healthy tree at two in the morning costs a night for nothing, and the whole
// point of the system is that it runs while nobody is watching.
//
// So the extension is conditional on evidence, not on optimism: the last wave
// must have completed at least one child. That is the same signal the stall
// rule reads, from the other side — fruitless_waves is zero exactly when the
// most recent round produced something. A goal that stops producing falls back
// to the base budget immediately and stops for a person, which is the case the
// gate was built for.
//
// Only trees extend. A goal that never split is bounded by MaxContinuations
// long before the run budget reaches it, and "it is still writing checkpoints"
// is not evidence of progress the way a completed child is.
func (db *DB) GoalBudgetSpent(contextID string) (bool, int, error) {
	base := db.RunsPerGoal()
	if base <= 0 {
		return false, 0, nil
	}
	runs, err := db.ContextRuns(contextID)
	if err != nil {
		return false, 0, err
	}
	if runs < base {
		return false, runs, nil
	}
	limit, err := db.goalLimit(contextID, base)
	if err != nil {
		return false, runs, err
	}
	return runs >= limit, runs, nil
}

// GoalLimit is the ceiling in force for one tree, base budget or extended.
func (db *DB) GoalLimit(contextID string) (int, error) {
	base := db.RunsPerGoal()
	if base <= 0 {
		return 0, nil
	}
	return db.goalLimit(contextID, base)
}

func (db *DB) goalLimit(contextID string, base int) (int, error) {
	ext := db.GoalExtensions()
	if ext <= 0 {
		return base, nil
	}
	var fruitless, total int
	err := db.conn.QueryRow(`SELECT
			coalesce(max(CASE WHEN parent_id = '' THEN fruitless_waves END), 0),
			count(*)
		FROM tasks WHERE context_id = ?`, contextID).Scan(&fruitless, &total)
	if err != nil {
		return base, fmt.Errorf("goal limit for %s: %w", contextID, err)
	}
	// Still producing, and actually a tree. Anything else keeps the base.
	if fruitless == 0 && total > 1 {
		return base * (1 + ext), nil
	}
	return base, nil
}
