package coord

import (
	"strings"
	"testing"
	"time"
)

// claimStart claims the next task for agent and opens its run.
func claimStart(t *testing.T, db *DB, agent string) (*Task, *TaskRun) {
	t.Helper()
	task, err := db.ClaimTask(agent)
	if err != nil {
		t.Fatalf("claim by %s: %v", agent, err)
	}
	run, err := db.StartRun(task.ID, agent, task.Attempts, "")
	if err != nil {
		t.Fatal(err)
	}
	return task, run
}

func TestCreateSubTaskInheritsAndLimits(t *testing.T) {
	db := testDB(t)
	root, _ := db.CreateTask(NewTask{CreatedBy: "human", Title: "Weekly digest", AssignedTo: "a1"})
	parent, _ := claimStart(t, db, "a1")

	child, err := db.CreateSubTask(parent.ID, "a1", NewTask{Title: "Source 1", AssignedTo: "a2"})
	if err != nil {
		t.Fatal(err)
	}
	if child.ParentID != root.ID || child.ContextID != root.ContextID || child.Depth != 1 || child.Kind != KindSub || child.CreatedBy != "a1" || child.AssignedTo != "a2" {
		t.Errorf("child: %+v", child)
	}
	events, _ := db.TaskEvents(root.ID)
	if len(events) == 0 || !strings.Contains(events[len(events)-1].Note, "child created: "+child.ID) {
		t.Errorf("parent should record the child: %+v", events)
	}

	// Cap on open children.
	for i := 1; i < MaxOpenChildren; i++ {
		if _, err := db.CreateSubTask(parent.ID, "a1", NewTask{Title: "more"}); err != nil {
			t.Fatalf("child %d: %v", i+1, err)
		}
	}
	if _, err := db.CreateSubTask(parent.ID, "a1", NewTask{Title: "one too many"}); err == nil || !strings.Contains(err.Error(), "unfinished children") {
		t.Errorf("9th open child: got %v", err)
	}
	// Finishing one frees a slot.
	if err := db.CancelTask(child.ID, "a1"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateSubTask(parent.ID, "a1", NewTask{Title: "fits now"}); err != nil {
		t.Errorf("after a child finished: %v", err)
	}

	// Depth has teeth: a chain of children stops at MaxDepth. Start the chain
	// from a fresh root — the parent above is at its open-children cap.
	db.CreateTask(NewTask{CreatedBy: "human", Title: "Chain", AssignedTo: "a4"})
	deep, _ := claimStart(t, db, "a4")
	for {
		next, err := db.CreateSubTask(deep.ID, "a1", NewTask{Title: "deeper"})
		if err != nil {
			if deep.Depth != MaxDepth || !strings.Contains(err.Error(), "depth") {
				t.Fatalf("stopped at depth %d with %v, want a depth error at %d", deep.Depth, err, MaxDepth)
			}
			break
		}
		deep = next
	}

	// A finished parent cannot spawn. High priority so a3 claims it ahead of
	// the unassigned children created above.
	done, _ := db.CreateTask(NewTask{CreatedBy: "h", Title: "done", AssignedTo: "a3", Priority: 100})
	dt, run := claimStart(t, db, "a3")
	if dt.ID != done.ID {
		t.Fatalf("precondition: claimed %s (%s), want %s", dt.ID, dt.Title, done.ID)
	}
	db.FinishRun(run.ID, RunOutcome{Liveness: RunCompleted, Result: "ok"})
	if _, err := db.CreateSubTask(done.ID, "a3", NewTask{Title: "late"}); err == nil {
		t.Error("a completed task must not get children")
	}
}

func TestWakeParentsWhenEveryChildIsTerminal(t *testing.T) {
	db := testDB(t)
	db.CreateTask(NewTask{CreatedBy: "human", Title: "Digest", AssignedTo: "a1"})
	parent, prun := claimStart(t, db, "a1")
	c1, _ := db.CreateSubTask(parent.ID, "a1", NewTask{Title: "Source A"})
	c2, _ := db.CreateSubTask(parent.ID, "a1", NewTask{Title: "Source B"})
	c3, _ := db.CreateSubTask(parent.ID, "a1", NewTask{Title: "Source C"})
	if err := db.BlockTask(parent.ID, "a1", parent.Attempts, BlockedOnChildren, "fanned out to 3 sources"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FinishRun(prun.ID, RunOutcome{Liveness: RunBlocked, BlockedOn: BlockedOnChildren}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()

	// Two of three done: parent stays blocked.
	for _, id := range []string{c1.ID, c2.ID} {
		task, run := claimStart(t, db, "a2")
		if task.ID != id {
			t.Fatalf("claimed %s, want %s (children are claimable oldest first)", task.ID, id)
		}
		db.FinishRun(run.ID, RunOutcome{Liveness: RunCompleted, Result: "summary of " + task.Title})
	}
	woken, err := db.WakeParents(now)
	if err != nil || len(woken) != 0 {
		t.Fatalf("parent woke with a child still open: %v %v", woken, err)
	}
	if p, _ := db.GetTask(parent.ID); p.State != TaskBlocked {
		t.Fatalf("parent state %s", p.State)
	}

	// Last child canceled by a person — still terminal, still counts.
	if err := db.CancelTask(c3.ID, "human"); err != nil {
		t.Fatal(err)
	}
	woken, err = db.WakeParents(now)
	if err != nil || len(woken) != 1 || woken[0].TaskID != parent.ID || woken[0].AssignedTo != "a1" {
		t.Fatalf("woken: %+v err=%v", woken, err)
	}
	p, _ := db.GetTask(parent.ID)
	if p.State != TaskSubmitted || p.BlockedOn != "" || p.AssignedTo != "a1" {
		t.Errorf("parent after wake: state=%s blocked_on=%q assigned=%s", p.State, p.BlockedOn, p.AssignedTo)
	}
	for _, want := range []string{"fanned out to 3 sources", "Children finished (3)", "[completed] Source A", "summary of Source A", "[completed] Source B", "[canceled] Source C"} {
		if !strings.Contains(p.Checkpoint, want) {
			t.Errorf("checkpoint missing %q:\n%s", want, p.Checkpoint)
		}
	}
	events, _ := db.TaskEvents(parent.ID)
	if !strings.Contains(events[len(events)-1].Note, ChildrenDoneEvent) {
		t.Errorf("last event should be children-done: %+v", events[len(events)-1])
	}

	// A second sweep (the other gateway) finds nothing to wake.
	if woken, _ := db.WakeParents(now); len(woken) != 0 {
		t.Error("parent woken twice")
	}
	// It is claimable again by the pinned agent, and not by another.
	if _, err := db.ClaimTask("a2"); err == nil {
		t.Error("parent is pinned to a1; a2 must not claim it")
	}
	again, err := db.ClaimTask("a1")
	if err != nil || again.ID != parent.ID {
		t.Fatalf("a1 should claim the parent back: %v %v", again, err)
	}
}

func TestWakeParentsBlockedWithoutChildren(t *testing.T) {
	db := testDB(t)
	db.CreateTask(NewTask{CreatedBy: "human", Title: "Lonely", AssignedTo: "a1"})
	parent, prun := claimStart(t, db, "a1")
	db.BlockTask(parent.ID, "a1", parent.Attempts, BlockedOnChildren, "")
	db.FinishRun(prun.ID, RunOutcome{Liveness: RunBlocked, BlockedOn: BlockedOnChildren})
	woken, err := db.WakeParents(time.Now())
	if err != nil || len(woken) != 1 {
		t.Fatalf("a parent blocked on children it never created must not wait forever: %v %v", woken, err)
	}
	p, _ := db.GetTask(parent.ID)
	if !strings.Contains(p.Checkpoint, "none were created") {
		t.Errorf("checkpoint should say so: %q", p.Checkpoint)
	}
}

func TestWakeParentsLeavesHumanBlocksAlone(t *testing.T) {
	db := testDB(t)
	db.CreateTask(NewTask{CreatedBy: "human", Title: "Ask", AssignedTo: "a1"})
	parent, prun := claimStart(t, db, "a1")
	db.BlockTask(parent.ID, "a1", parent.Attempts, BlockedOnHuman, "which budget?")
	db.FinishRun(prun.ID, RunOutcome{Liveness: RunBlocked, BlockedOn: BlockedOnHuman})
	if woken, _ := db.WakeParents(time.Now()); len(woken) != 0 {
		t.Error("a task waiting on a person is not a parent waiting on children")
	}
}
