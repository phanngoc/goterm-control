package coord

import (
	"fmt"
	"strings"
	"time"
)

// Reading the criteria back at the end.
//
// A goal finishes because the agent that did the work typed `task done`. That
// is self-declared, and the survey of how these systems fail is blunt about it:
// writing acceptance criteria down and never reading them back is the most
// conspicuous unclosed loop in the field. Everything terminates on task
// exhaustion, a cap, or an orchestrator's opinion.
//
// The same survey is equally blunt about what a check is worth — only as good
// as the distance between the thing checked and the thing checking. In
// descending order: a compiler, a deterministic assertion, a DIFFERENT model
// with different context holding the criteria, and last and worthless, the same
// model asked to reconsider its own answer.
//
// Three agents on three backends is the third rung, available here for nothing.
// So a goal with criteria on it is read by a peer that did not do the work.
//
// This is not a manager. It assigns nothing, watches nobody, decides nothing
// about who does what. It answers one question and dies.
//
// And it cannot undo the goal. Terminal states are immutable here — that came
// from A2A along with the state names, and there is no path out of `completed`
// anywhere in this package. A rejection opens the NEXT task in the same
// context instead, which is A2A's own rule for the same situation. The goal
// stays finished; the tree stops being finished, which is the true statement.

const (
	// KindVerify marks a task whose only job is to read a finished goal
	// against the criteria it was given.
	KindVerify = "verify"

	// VerifyFollowedUp is written into a rejected verification's fail_reason
	// once its follow-up exists. It is the compare-and-set that stops three
	// gateways opening three follow-ups for one rejection — the same idiom as
	// MarkReported and ClaimSchedule.
	VerifyFollowedUp = "followed-up"
)

// NeedsVerification lists goals that have finished and not yet been read.
//
// A goal here is a root task, completed, carrying criteria. Children are out:
// their bar belongs to their parent, which already saw it when it woke.
// Verifications are out, or the system would verify its own verifying.
func (db *DB) NeedsVerification(limit int) ([]Task, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	rows, err := db.conn.Query(`SELECT `+taskCols+` FROM tasks g
		WHERE g.parent_id = ''
		  AND g.state = ?
		  AND g.acceptance <> ''
		  AND g.kind <> ?
		  AND NOT EXISTS (SELECT 1 FROM tasks v WHERE v.parent_id = g.id AND v.kind = ?)
		ORDER BY g.updated_at
		LIMIT ?`, TaskCompleted, KindVerify, KindVerify, limit)
	if err != nil {
		return nil, fmt.Errorf("goals awaiting verification: %w", err)
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

// OpenVerification creates the reading task for one goal, assigned to a peer
// that did not do the work.
//
// Returns nil when there is nobody else to ask. A goal checked by the agent
// that wrote it is the bottom rung — the same model, the same context,
// reconsidering its own answer — and the survey's word for that is theatre.
// Better to leave the goal plainly unverified than to stamp it with a check
// worth nothing.
func (db *DB) OpenVerification(goal Task, peers []Agent) (*Task, error) {
	checker := pickChecker(goal, peers)
	if checker == "" {
		return nil, nil
	}
	return db.CreateTask(NewTask{
		ContextID:  goal.ContextID,
		ChannelID:  goal.ChannelID,
		ParentID:   goal.ID,
		Kind:       KindVerify,
		CreatedBy:  "system",
		AssignedTo: checker,
		Title:      "Verify: " + goal.Title,
		Body:       verifyBody(goal),
		// One look, not a job. A verification that needs five runs to decide
		// is a verification that has started doing the work itself.
		MaxContinuations: 2,
	})
}

// pickChecker chooses who reads the work: anyone but whoever did it, online
// first. Deliberately not the "best" peer — there is no measure of that here,
// and a routing table invented for one question is a routing table nobody
// maintains.
func pickChecker(goal Task, peers []Agent) string {
	did := goal.ClaimedBy
	if did == "" {
		did = goal.AssignedTo
	}
	var offline string
	for _, p := range peers {
		if p.ID == did || p.ID == "" {
			continue
		}
		if p.Online {
			return p.ID
		}
		if offline == "" {
			offline = p.ID
		}
	}
	return offline
}

func verifyBody(goal Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Another agent finished this goal and declared it done. Read the work against "+
		"the criteria below and say whether it meets them. You did not do this work and you are "+
		"not being asked to finish it.\n\n")
	fmt.Fprintf(&b, "Goal: %s (%s)\n", goal.Title, goal.ID)
	if goal.Body != "" {
		fmt.Fprintf(&b, "\nWhat was asked:\n%s\n", goal.Body)
	}
	fmt.Fprintf(&b, "\nAccepted when:\n%s\n", goal.Acceptance)
	if goal.Result != "" {
		fmt.Fprintf(&b, "\nWhat it reported:\n%s\n", truncateRunes(goal.Result, 2000))
	}
	fmt.Fprintf(&b, "\nRead what it actually produced, not only what it said about it: "+
		"`bomclaw artifact list --task %s --tree` and `bomclaw task tree --id %s`.\n\n", goal.ID, goal.ID)
	b.WriteString("Then, in one run:\n" +
		"  meets every criterion → `bomclaw task done --id <this task> --result \"pass: <how you checked>\"`\n" +
		"  falls short of any    → `bomclaw task reject --id <this task> --result \"<which criterion, and what is missing>\"`\n\n" +
		"A rejection opens follow-up work for the agent that did the original, carrying your words, " +
		"so name the criterion and what is missing rather than describing the work in general. " +
		"Being unable to check something is a third answer: say so in a pass, do not reject for it.")
	return b.String()
}

// RejectedVerifications lists readings that came back negative and have not yet
// produced follow-up work.
func (db *DB) RejectedVerifications(limit int) ([]Task, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	rows, err := db.conn.Query(`SELECT `+taskCols+` FROM tasks
		WHERE kind = ? AND state = ? AND fail_reason = ''
		ORDER BY updated_at
		LIMIT ?`, KindVerify, TaskRejected, limit)
	if err != nil {
		return nil, fmt.Errorf("rejected verifications: %w", err)
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

// FollowUpRejection opens the work a rejection asks for, once.
//
// The goal itself is not touched. It is completed, completed is terminal, and
// terminal is immutable here — so the tree gains a new open task instead, in
// the same context, which is exactly what makes ContextProgress stop reporting
// the tree as finished. The run budget applies to it like everything else in
// that context, so a fix-verify-fix cycle cannot run forever.
//
// Claimed with a compare-and-set on the verification's fail_reason before the
// task is created: three gateways run this sweep, and the loser must not open a
// second copy of the same work.
func (db *DB) FollowUpRejection(v Task, now time.Time) (*Task, error) {
	res, err := db.conn.Exec(`UPDATE tasks SET fail_reason = ?, updated_at = ?
		WHERE id = ? AND kind = ? AND state = ? AND fail_reason = ''`,
		VerifyFollowedUp, ts(now), v.ID, KindVerify, TaskRejected)
	if err != nil {
		return nil, fmt.Errorf("claim rejection %s: %w", v.ID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, nil // a peer got there first
	}

	goal, err := db.GetTask(v.ParentID)
	if err != nil {
		return nil, err
	}
	assigned := goal.ClaimedBy
	if assigned == "" {
		assigned = goal.AssignedTo
	}
	body := fmt.Sprintf("A peer read this goal against its criteria and found it short. "+
		"Its words:\n\n%s\n\nThe original goal is %s — its criteria are below, unchanged. "+
		"Fix what is missing; do not start the work over.\n\nAccepted when:\n%s",
		strings.TrimSpace(v.Result), goal.ID, goal.Acceptance)

	return db.CreateTask(NewTask{
		ContextID:  goal.ContextID,
		ChannelID:  goal.ChannelID,
		ParentID:   v.ID,
		CreatedBy:  "system",
		AssignedTo: assigned,
		Title:      "Rework: " + goal.Title,
		Body:       body,
		Acceptance: goal.Acceptance,
	})
}
