package coord

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
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

// MinSubTaskBody is how much description a child must carry.
//
// Paperclip's rule for splitting work: "a child must be checkout-able by the
// owner from its title and description alone — reviewers should not have to
// re-read the parent plan to understand a child." That matters more here than
// it does there, because any agent may claim a child, on a different harness,
// with none of the parent's conversation. A title and "làm tiếp phần 2" is a
// task only its author can run.
//
// The bar is deliberately low. It rejects the reflex one-liner without
// turning a legitimate small child into a writing exercise.
const MinSubTaskBody = 40

// CreateSubTask creates a child of parentID. The child inherits the parent's
// context and sits one level deeper (MaxDepth applies, and now bites: the
// prompt tells the agent its depth). Only an open parent may have children —
// a finished task spawning work would be a task nobody is waiting on.
//
// n.Inputs are artifact ids the parent is handing down; they are linked to the
// child with role=input, so the child reads them by id instead of being told a
// path in prose.
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
	if utf8.RuneCountInString(strings.TrimSpace(n.Body)) < MinSubTaskBody {
		return nil, fmt.Errorf("coord: a child needs a brief of its own (at least %d characters). "+
			"Whoever claims it may be a different agent on a different harness with none of your "+
			"conversation: say what to do, where, and what done looks like", MinSubTaskBody)
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
	for _, id := range n.Inputs {
		if err := db.LinkArtifact(id, child.ID, RoleInput); err != nil {
			return nil, fmt.Errorf("hand %s down to %s: %w", id, child.ID, err)
		}
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
		checkpoint += db.childrenSummary(children)
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

// ChildResultRunes caps the result text quoted per child. It used to be 1500
// and was still not enough, because the result was carrying things a result
// should never carry — a whole patch, a whole report. Those are artifacts now,
// so the quote can go back to being a summary line and the substance is read
// by id, in full, only when the parent actually wants it.
const ChildResultRunes = 400

// childrenSummary is what the parent reads on its next run: one entry per
// child, in creation order, with its outcome and an index of what it produced.
//
// The index is the point. Before, eight children meant eight truncated result
// blobs competing with the parent's own brief for room in the prompt; now the
// parent sees ids, kinds and sizes, and spends context only on the artifacts
// it decides to open.
func (db *DB) childrenSummary(children []Task) string {
	var b strings.Builder
	if len(children) == 0 {
		b.WriteString("Children finished: none were created — you blocked on children without any. Do the work yourself or finish the task.")
		return b.String()
	}
	fmt.Fprintf(&b, "Children finished (%d):\n", len(children))
	anyArtifact := false
	for i, c := range children {
		fmt.Fprintf(&b, "\n%d. [%s] %s (%s", i+1, c.State, c.Title, c.ID)
		if c.ClaimedBy != "" {
			fmt.Fprintf(&b, ", by %s", c.ClaimedBy)
		}
		b.WriteString(")")
		if c.FailReason != "" {
			fmt.Fprintf(&b, " — %s", c.FailReason)
		}
		b.WriteString("\n")
		if r := strings.TrimSpace(c.Result); r != "" {
			fmt.Fprintf(&b, "   → %s\n", strings.ReplaceAll(truncateRunes(r, ChildResultRunes), "\n", "\n     "))
		}
		if a := strings.TrimSpace(c.Acceptance); a != "" {
			fmt.Fprintf(&b, "   accepted-if: %s\n", truncateRunes(a, 200))
		}
		// A failure to read artifacts must not cost the parent its wake-up:
		// the outcome above is still worth delivering.
		arts, err := db.TaskArtifacts(c.ID)
		if err != nil {
			fmt.Fprintf(&b, "   (artifact index unavailable: %v)\n", err)
			continue
		}
		var produced []Artifact
		for _, a := range arts {
			if a.TaskID == c.ID {
				produced = append(produced, a)
			}
		}
		if len(produced) == 0 {
			continue
		}
		anyArtifact = true
		b.WriteString("   artifacts:\n")
		for _, a := range produced {
			fmt.Fprintf(&b, "     %s  %-8s %s", a.ID, a.Kind, a.Title)
			if a.Kind == ArtifactLink {
				fmt.Fprintf(&b, "  %s", a.URL)
			} else {
				fmt.Fprintf(&b, "  (%s)", HumanBytes(a.Bytes))
			}
			b.WriteString("\n")
		}
	}
	if anyArtifact {
		b.WriteString("\nRead one in full with `bomclaw artifact get <id>`; " +
			"`bomclaw task show <id>` has a child's untruncated result.")
	}
	return strings.TrimRight(b.String(), "\n")
}

// HumanBytes renders a byte count for an index a person or an agent reads.
func HumanBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}

func truncateRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
