package coord

import (
	"testing"
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
