package coord

import (
	"strings"
	"testing"
	"time"
)

func progressDB(t *testing.T) *DB {
	t.Helper()
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw", "bomclaw2", "bomclaw3")
	return db
}

// The question nobody could answer before: what is still open in this goal?
func TestContextProgressCountsTheOpenFrontier(t *testing.T) {
	db := progressDB(t)
	goal, err := db.CreateTask(NewTask{Title: "việc lớn", CreatedBy: "bomclaw2"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ClaimTask("bomclaw2"); err != nil {
		t.Fatal(err)
	}
	done, err := db.CreateSubTask(goal.ID, "bomclaw2", NewTask{
		Title: "mảnh xong", Body: "Một mô tả đủ dài để qua ràng buộc brief tự đứng được của con.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateSubTask(goal.ID, "bomclaw2", NewTask{
		Title: "mảnh chưa xong", Body: "Một mô tả đủ dài để qua ràng buộc brief tự đứng được của con.",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.Exec(`UPDATE tasks SET state = ? WHERE id = ?`, TaskCompleted, done.ID); err != nil {
		t.Fatal(err)
	}

	p, err := db.ContextProgress(goal.ContextID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Total != 3 {
		t.Errorf("total = %d, want 3 (the goal and both pieces)", p.Total)
	}
	if p.Goal == nil || p.Goal.ID != goal.ID {
		t.Errorf("the tree does not know its own goal: %+v", p.Goal)
	}
	if p.ByState[TaskCompleted] != 1 {
		t.Errorf("by-state = %v, want one completed", p.ByState)
	}
	if len(p.Open) != 2 {
		t.Fatalf("frontier = %d tasks, want 2 (the goal is still open too)", len(p.Open))
	}
	for _, o := range p.Open {
		if o.ID == done.ID {
			t.Error("a finished piece is still on the frontier")
		}
	}
	if p.Done() {
		t.Error("a tree with open work reported itself finished")
	}
}

// An empty frontier is not the same as a goal being met — it only means the
// decomposition that exists has been exhausted. Done() is named for that.
func TestATreeWithNothingOpenHasStoppedButIsNotJudged(t *testing.T) {
	db := progressDB(t)
	goal, err := db.CreateTask(NewTask{Title: "việc", CreatedBy: "bomclaw", Acceptance: "phải chạy được"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.ContextProgress(goal.ContextID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Done() {
		t.Fatal("a tree whose only task is still submitted reported itself stopped")
	}
	if _, err := db.conn.Exec(`UPDATE tasks SET state = ? WHERE id = ?`, TaskCompleted, goal.ID); err != nil {
		t.Fatal(err)
	}
	p, err = db.ContextProgress(goal.ContextID)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Done() {
		t.Fatal("a tree with nothing open did not report itself stopped")
	}
	// And the criteria are still sitting there unjudged, which is the point:
	// stopping is a fact about the queue, not a verdict on the work.
	if p.Goal.Acceptance == "" {
		t.Error("the goal lost its acceptance criteria")
	}
}

// The per-task ceilings cannot see a tree that keeps splitting legally. This
// number can.
func TestContextRunsCountsTheWholeTree(t *testing.T) {
	db := progressDB(t)
	goal, err := db.CreateTask(NewTask{Title: "việc lớn", CreatedBy: "bomclaw2"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := db.ClaimTask("bomclaw2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.StartRun(claimed.ID, "bomclaw2", claimed.Attempts, ""); err != nil {
		t.Fatal(err)
	}
	child, err := db.CreateSubTask(goal.ID, "bomclaw2", NewTask{
		Title: "mảnh", Body: "Một mô tả đủ dài để qua ràng buộc brief tự đứng được của con.",
	})
	if err != nil {
		t.Fatal(err)
	}
	c2, err := db.ClaimTask("bomclaw")
	if err != nil {
		t.Fatal(err)
	}
	if c2.ID != child.ID {
		t.Fatalf("claimed %s, expected the child", c2.ID)
	}
	if _, err := db.StartRun(c2.ID, "bomclaw", c2.Attempts, ""); err != nil {
		t.Fatal(err)
	}

	n, err := db.ContextRuns(goal.ContextID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("runs in the tree = %d, want 2 — a per-goal budget measured on this\n"+
			"is the only ceiling that can see a tree splitting legally forever", n)
	}
}

// Listing by tree is what the CLI and the board read; without it a goal could
// only be inspected by walking parent_id by hand.
func TestListTasksCanScopeToOneTree(t *testing.T) {
	db := progressDB(t)
	mine, err := db.CreateTask(NewTask{Title: "cây A", CreatedBy: "bomclaw"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateTask(NewTask{Title: "cây B", CreatedBy: "bomclaw"}); err != nil {
		t.Fatal(err)
	}
	got, err := db.ListTasks(TaskFilter{ContextID: mine.ContextID})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != mine.ID {
		t.Fatalf("scoping to a tree returned %d tasks from other trees too", len(got))
	}
}

// A goal that has spent its budget stops to ask, and is not thrown away. The
// work is not wrong, it has become expensive, and only a person can say whether
// to keep paying — failing it would discard a tree that may be nearly done.
func TestAGoalStopsToAskWhenItHasSpentItsRuns(t *testing.T) {
	db := progressDB(t)
	db.SetRunsPerGoal(2)

	goal, err := db.CreateTask(NewTask{Title: "việc lớn", CreatedBy: "bomclaw"})
	if err != nil {
		t.Fatal(err)
	}
	// Two runs: the budget is now spent.
	for i := 0; i < 2; i++ {
		c, err := db.ClaimTask("bomclaw")
		if err != nil {
			t.Fatal(err)
		}
		run, err := db.StartRun(c.ID, "bomclaw", c.Attempts, "")
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if _, err := db.FinishRun(run.ID, RunOutcome{Liveness: RunAdvanced, Checkpoint: "đang làm"}); err != nil {
				t.Fatal(err)
			}
		} else {
			final, err := db.FinishRun(run.ID, RunOutcome{Liveness: RunAdvanced, Checkpoint: "vẫn đang làm"})
			if err != nil {
				t.Fatal(err)
			}
			if final.State != TaskBlocked || final.BlockedOn != BlockedOnHuman {
				t.Fatalf("a goal over budget went to %s/%s, want blocked on a human",
					final.State, final.BlockedOn)
			}
			if final.FailReason != "" {
				t.Errorf("a goal over budget was failed (%q) — a tree that may be nearly done was thrown away",
					final.FailReason)
			}
			if !strings.Contains(final.Checkpoint, "budget") {
				t.Errorf("the checkpoint does not say why it stopped: %q", final.Checkpoint)
			}
			if !strings.Contains(final.Checkpoint, "đang làm") {
				t.Errorf("stopping overwrote what earlier runs recorded: %q", final.Checkpoint)
			}
		}
	}
	_ = goal
}

// And splitting again is refused, with the numbers in the message: the agent
// reading it is the one deciding what to do instead.
func TestAGoalOverBudgetCannotSplitAgain(t *testing.T) {
	db := progressDB(t)
	goal, err := db.CreateTask(NewTask{Title: "việc lớn", CreatedBy: "bomclaw2"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := db.ClaimTask("bomclaw2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.StartRun(c.ID, "bomclaw2", c.Attempts, ""); err != nil {
		t.Fatal(err)
	}
	db.SetRunsPerGoal(1)

	_, err = db.CreateSubTask(goal.ID, "bomclaw2", NewTask{
		Title: "mảnh", Body: "Một mô tả đủ dài để qua ràng buộc brief tự đứng được của con.",
	})
	if err == nil {
		t.Fatal("a goal with no budget left was allowed to split again")
	}
	if !strings.Contains(err.Error(), "budget") {
		t.Errorf("the refusal does not say why: %v", err)
	}

	// No ceiling means no refusal.
	db.SetRunsPerGoal(-1)
	if _, err := db.CreateSubTask(goal.ID, "bomclaw2", NewTask{
		Title: "mảnh", Body: "Một mô tả đủ dài để qua ràng buộc brief tự đứng được của con.",
	}); err != nil {
		t.Fatalf("a negative ceiling should remove the budget entirely: %v", err)
	}
}

// A goal that is still producing extends itself rather than stopping. The gate
// exists to stop a tree going in circles, not a tree that is working and
// happens to be long — stopping a healthy one at two in the morning costs a
// night for nothing.
func TestAProducingGoalExtendsItselfInsteadOfStopping(t *testing.T) {
	db := progressDB(t)
	db.SetRunsPerGoal(2)
	db.SetGoalExtensions(3)

	goal, err := db.CreateTask(NewTask{Title: "việc lớn", CreatedBy: "bomclaw"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ClaimTask("bomclaw"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateSubTask(goal.ID, "bomclaw", NewTask{
		Title: "mảnh", Body: "Một mô tả đủ dài để qua ràng buộc brief tự đứng được của con.",
	}); err != nil {
		t.Fatal(err)
	}
	// Three runs against a base budget of two, with the last round productive.
	for i := 0; i < 3; i++ {
		if _, err := db.conn.Exec(`INSERT INTO task_runs (id, task_id, agent_id, attempt, liveness, started_at)
			VALUES (?, ?, 'bomclaw', 1, ?, ?)`,
			"tr_ext"+string(rune('a'+i)), goal.ID, RunAdvanced, ts(time.Now())); err != nil {
			t.Fatal(err)
		}
	}

	spent, runs, err := db.GoalBudgetSpent(goal.ContextID)
	if err != nil {
		t.Fatal(err)
	}
	if spent {
		t.Fatalf("a producing goal stopped at %d runs — it would sit idle overnight while it\n"+
			"was still getting somewhere", runs)
	}
	if limit, _ := db.GoalLimit(goal.ContextID); limit != 8 {
		t.Errorf("extended limit = %d, want base 2 × (1+3)", limit)
	}

	// The moment it stops producing, the extension is gone and so is the goal.
	if _, err := db.conn.Exec(`UPDATE tasks SET fruitless_waves = 2 WHERE id = ?`, goal.ID); err != nil {
		t.Fatal(err)
	}
	spent, _, err = db.GoalBudgetSpent(goal.ContextID)
	if err != nil {
		t.Fatal(err)
	}
	if !spent {
		t.Fatal("a goal that stopped producing kept its extension — it can now burn four times\n" +
			"the budget going in circles, which is the case the gate was built for")
	}
}

// A goal that never split is bounded by MaxContinuations long before the run
// budget reaches it, and "still writing checkpoints" is not the evidence a
// completed child is.
func TestALinearGoalDoesNotExtendItself(t *testing.T) {
	db := progressDB(t)
	db.SetRunsPerGoal(1)
	db.SetGoalExtensions(3)

	goal, err := db.CreateTask(NewTask{Title: "việc lẻ", CreatedBy: "bomclaw"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.Exec(`INSERT INTO task_runs (id, task_id, agent_id, attempt, liveness, started_at)
		VALUES ('tr_lin', ?, 'bomclaw', 1, ?, ?)`, goal.ID, RunAdvanced, ts(time.Now())); err != nil {
		t.Fatal(err)
	}
	spent, _, err := db.GoalBudgetSpent(goal.ContextID)
	if err != nil {
		t.Fatal(err)
	}
	if !spent {
		t.Fatal("a single task extended itself on the strength of writing checkpoints")
	}
}
