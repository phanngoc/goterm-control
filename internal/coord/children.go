package coord

import (
	"fmt"
	"strings"
	"time"
)

// Parent/child tasks (design §5.2). A parent does not manage its children: it
// creates them, parks itself with `task block --on children`, and is called
// back — with every child's result in its prompt — once the last child reaches
// a terminal state. Any agent may run the parent's next run; soft affinity
// (assigned_to = the last holder) just makes `--resume` likely.

// MaxOpenChildren caps how many unfinished children one parent may have at a
// time. OpenClaw's maxChildrenPerAgent, for the same reason: a parent that
// fans out without bound is a loop with extra steps.
const MaxOpenChildren = 8

// ChildrenDoneEvent is the task_events note that marks a parent waking.
const ChildrenDoneEvent = "children-done"

// CreateSubTask creates a child of parentID. The child inherits the parent's
// context and sits one level deeper (MaxDepth applies, and now bites: the
// prompt tells the agent its depth). Only an open parent may have children —
// a finished task spawning work would be a task nobody is waiting on.
func (db *DB) CreateSubTask(parentID, byAgent string, n NewTask) (*Task, error) {
	parent, err := db.GetTask(parentID)
	if err != nil {
		return nil, err
	}
	switch parent.State {
	case TaskWorking, TaskBlocked, TaskSubmitted:
	default:
		return nil, fmt.Errorf("coord: task %s is %s — a finished task cannot have children", parentID, parent.State)
	}
	open, err := db.openChildren(parentID)
	if err != nil {
		return nil, err
	}
	if open >= MaxOpenChildren {
		return nil, fmt.Errorf("coord: task %s already has %d unfinished children (max %d) — wait for some to finish", parentID, open, MaxOpenChildren)
	}
	n.ParentID = parentID
	n.ContextID = parent.ContextID
	n.Depth = parent.Depth + 1
	n.Kind = KindSub
	if n.CreatedBy == "" {
		n.CreatedBy = byAgent
	}
	child, err := db.CreateTask(n)
	if err != nil {
		return nil, err
	}
	_ = db.appendEvent(parentID, byAgent, parent.State, parent.State, "child created: "+child.ID+" "+truncateNote(child.Title))
	return child, nil
}

// Children lists a task's children, oldest first.
func (db *DB) Children(parentID string) ([]Task, error) {
	rows, err := db.conn.Query(`SELECT `+taskCols+` FROM tasks WHERE parent_id = ? ORDER BY created_at`, parentID)
	if err != nil {
		return nil, fmt.Errorf("children of %s: %w", parentID, err)
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

func (db *DB) openChildren(parentID string) (int, error) {
	var n int
	err := db.conn.QueryRow(`SELECT count(*) FROM tasks WHERE parent_id = ? AND state NOT IN (?, ?, ?, ?)`,
		parentID, TaskCompleted, TaskFailed, TaskCanceled, TaskRejected).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("open children of %s: %w", parentID, err)
	}
	return n, nil
}

// WokenParent is a parent WakeParents put back in the queue, with the agent
// its doorbell should ring.
type WokenParent struct {
	TaskID     string
	AssignedTo string // "" = anyone
}

// WakeParents returns every parent that is blocked on its children and whose
// children have all reached a terminal state to the queue, with the children's
// results appended to its checkpoint so the next run can gather them.
//
// It is a sweep rather than a hook inside each child's finish, because children
// end by many paths — FinishRun, CancelTask, ReapExhausted, a person on the
// dashboard — and one query that asks "is anything still open?" covers them
// all. Every gateway runs it; the unblock is guarded on state = blocked, so two
// sweeps seeing the same parent wake it once. The parent is pinned to whoever
// held it last (soft affinity, §5.4) so that agent's --resume finds the
// conversation that created the children.
func (db *DB) WakeParents(now time.Time) ([]WokenParent, error) {
	rows, err := db.conn.Query(`SELECT `+taskCols+` FROM tasks p
		WHERE p.state = ? AND p.blocked_on = ?
		  AND NOT EXISTS (SELECT 1 FROM tasks c WHERE c.parent_id = p.id AND c.state NOT IN (?, ?, ?, ?))`,
		TaskBlocked, BlockedOnChildren, TaskCompleted, TaskFailed, TaskCanceled, TaskRejected)
	if err != nil {
		return nil, fmt.Errorf("wake parents: %w", err)
	}
	var parents []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		parents = append(parents, *t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var woken []WokenParent
	for _, p := range parents {
		children, err := db.Children(p.ID)
		if err != nil {
			return woken, err
		}
		checkpoint := strings.TrimSpace(p.Checkpoint)
		if checkpoint != "" {
			checkpoint += "\n\n"
		}
		checkpoint += childrenSummary(children)
		// Pin to the last holder so its --resume picks the conversation up;
		// RelaxDeadAssignments lifts the pin if that agent is gone.
		assigned := p.AssignedTo
		if p.ClaimedBy != "" {
			assigned = p.ClaimedBy
		}
		res, err := db.conn.Exec(`UPDATE tasks SET state = ?, blocked_on = '', checkpoint = ?, assigned_to = ?,
				lease_until = ?, updated_at = ?
			WHERE id = ? AND state = ? AND blocked_on = ?`,
			TaskSubmitted, checkpoint, assigned, ts(now), ts(now), p.ID, TaskBlocked, BlockedOnChildren)
		if err != nil {
			return woken, fmt.Errorf("wake parent %s: %w", p.ID, err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue // another sweep got there first
		}
		_ = db.appendEvent(p.ID, "system", TaskBlocked, TaskSubmitted,
			fmt.Sprintf("%s: %d child task(s) finished", ChildrenDoneEvent, len(children)))
		woken = append(woken, WokenParent{TaskID: p.ID, AssignedTo: assigned})
	}
	return woken, nil
}

// childrenSummary is what the parent reads on its next run: one entry per
// child, in creation order, with its outcome. Results are cut so eight
// children cannot crowd the parent's own brief out of the prompt.
func childrenSummary(children []Task) string {
	var b strings.Builder
	if len(children) == 0 {
		b.WriteString("Children finished: none were created — you blocked on children without any. Do the work yourself or finish the task.")
		return b.String()
	}
	fmt.Fprintf(&b, "Children finished (%d):\n", len(children))
	for i, c := range children {
		fmt.Fprintf(&b, "\n%d. [%s] %s (%s", i+1, c.State, c.Title, c.ID)
		if c.ClaimedBy != "" {
			fmt.Fprintf(&b, ", by %s", c.ClaimedBy)
		}
		b.WriteString(")")
		if c.FailReason != "" {
			fmt.Fprintf(&b, " — %s", c.FailReason)
		}
		if r := strings.TrimSpace(c.Result); r != "" {
			b.WriteString("\n   ")
			b.WriteString(strings.ReplaceAll(truncateRunes(r, 1500), "\n", "\n   "))
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func truncateRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
