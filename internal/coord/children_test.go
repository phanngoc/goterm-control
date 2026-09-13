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

	child, err := db.CreateSubTask(parent.ID, "a1", NewTask{Title: "Source 1", Body: "Read the first source end to end and write down every claim it makes about latency.", AssignedTo: "a2"})
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
		if _, err := db.CreateSubTask(parent.ID, "a1", NewTask{Title: "more", Body: "Placeholder child used to fill the open-children quota in this test; does nothing real."}); err != nil {
			t.Fatalf("child %d: %v", i+1, err)
		}
	}
	if _, err := db.CreateSubTask(parent.ID, "a1", NewTask{Title: "one too many", Body: "Placeholder child that should be refused because the parent is already at its cap."}); err == nil || !strings.Contains(err.Error(), "unfinished children") {
		t.Errorf("9th open child: got %v", err)
	}
	// Finishing one frees a slot.
	if err := db.CancelTask(child.ID, "a1"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateSubTask(parent.ID, "a1", NewTask{Title: "fits now", Body: "Placeholder child created after a sibling finished, so the parent is back under its cap."}); err != nil {
		t.Errorf("after a child finished: %v", err)
	}

	// Depth has teeth: a chain of children stops at MaxDepth. Start the chain
	// from a fresh root — the parent above is at its open-children cap.
	db.CreateTask(NewTask{CreatedBy: "human", Title: "Chain", AssignedTo: "a4"})
	deep, _ := claimStart(t, db, "a4")
	for {
		next, err := db.CreateSubTask(deep.ID, "a1", NewTask{Title: "deeper", Body: "Placeholder child used to walk the depth limit one level at a time until it refuses."})
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
	if _, err := db.CreateSubTask(done.ID, "a3", NewTask{Title: "late", Body: "Placeholder child aimed at a finished parent, which must be refused."}); err == nil {
		t.Error("a completed task must not get children")
	}
}

func TestWakeParentsWhenEveryChildIsTerminal(t *testing.T) {
	db := testDB(t)
	db.CreateTask(NewTask{CreatedBy: "human", Title: "Digest", AssignedTo: "a1"})
	parent, prun := claimStart(t, db, "a1")
	c1, _ := db.CreateSubTask(parent.ID, "a1", NewTask{Title: "Source A", Body: "Read source A end to end and summarise what it says about the scanner failure."})
	c2, _ := db.CreateSubTask(parent.ID, "a1", NewTask{Title: "Source B", Body: "Read source B end to end and summarise what it says about the scanner failure."})
	c3, _ := db.CreateSubTask(parent.ID, "a1", NewTask{Title: "Source C", Body: "Read source C end to end and summarise what it says about the scanner failure."})
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

func TestParentWakesWithAnArtifactIndexNotTheContent(t *testing.T) {
	db := testDB(t)
	db.CreateTask(NewTask{CreatedBy: "human", Title: "Clean up codex errors", AssignedTo: "a1"})
	parent, prun := claimStart(t, db, "a1")
	child, err := db.CreateSubTask(parent.ID, "a1", NewTask{
		Title:      "Fix the stderr join",
		Body:       "Join stderr on turn.failed and scanner failures; keep the nonzero-exit path returning an error.",
		Acceptance: "go test -race ./internal/codex passes and the five fake-CLI regression cases stay green",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.BlockTask(parent.ID, "a1", parent.Attempts, BlockedOnChildren, "one child"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FinishRun(prun.ID, RunOutcome{Liveness: RunBlocked, BlockedOn: BlockedOnChildren}); err != nil {
		t.Fatal(err)
	}

	// A patch far larger than anything a prompt should carry.
	patch := strings.Repeat("diff --git a/internal/codex/client.go b/internal/codex/client.go\n", 500)
	art, err := db.PutArtifact(NewArtifact{
		TaskID: child.ID, Kind: ArtifactPatch, Title: "codex error cleanup",
		Filename: "cleanup.patch", Content: []byte(patch), CreatedBy: "a2",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, run := claimStart(t, db, "a2")
	if _, err := db.FinishRun(run.ID, RunOutcome{
		Liveness: RunCompleted, Result: "Fixed the stderr join; patch attached.",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := db.WakeParents(time.Now()); err != nil {
		t.Fatal(err)
	}
	woken, err := db.GetTask(parent.ID)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(woken.Checkpoint, art.ID) {
		t.Errorf("the parent cannot reach the work: %q is not in the checkpoint", art.ID)
	}
	if !strings.Contains(woken.Checkpoint, "codex error cleanup") {
		t.Error("the artifact index has no title")
	}
	if !strings.Contains(woken.Checkpoint, "accepted-if") {
		t.Error("the acceptance bar did not travel with the child's outcome")
	}
	// The whole point: the content stays on disk. A checkpoint that carried
	// the patch would be the 1500-rune truncation problem with a bigger cap.
	if strings.Contains(woken.Checkpoint, "diff --git") {
		t.Error("the patch body was pasted into the parent's prompt")
	}
	if n := len(woken.Checkpoint); n > 2000 {
		t.Errorf("checkpoint is %d bytes for one child; the index should be small", n)
	}
}

// TestContextCapStopsTheTreeFromSpreading: MaxOpenChildren and MaxDepth are
// both local — eight wide and five deep is 32768 tasks between them, and
// neither cap can see that number. This one can.
func TestContextCapStopsTheTreeFromSpreading(t *testing.T) {
	db := testDB(t)
	db.SetMaxTasksPerContext(5) // the root plus four children

	db.CreateTask(NewTask{CreatedBy: "human", Title: "Big piece of work", AssignedTo: "a1"})
	parent, _ := claimStart(t, db, "a1")

	body := "Placeholder child with a brief long enough to pass the minimum body rule."
	for i := 0; i < 4; i++ {
		if _, err := db.CreateSubTask(parent.ID, "a1", NewTask{Title: "child", Body: body}); err != nil {
			t.Fatalf("child %d: %v", i+1, err)
		}
	}

	_, err := db.CreateSubTask(parent.ID, "a1", NewTask{Title: "one too many", Body: body})
	if err == nil {
		t.Fatal("the 6th task in the context was allowed")
	}
	// The agent reading this has to decide what to drop, so the numbers matter.
	for _, want := range []string{"5 tasks", "max 5"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should say %q, got: %v", want, err)
		}
	}

	// Finishing one must NOT free a slot: the cap is about how big this piece
	// of work was allowed to become, and that is already answered.
	children, _ := db.Children(parent.ID)
	if err := db.CancelTask(children[0].ID, "a1"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateSubTask(parent.ID, "a1", NewTask{Title: "still too many", Body: body}); err == nil {
		t.Error("cancelling a child reopened the tree-wide cap")
	}
}

// TestContextCapIsPerTree: one piece of work hitting the cap must not stop
// another. Separate databases because ClaimTask takes the oldest claimable
// task, which in one database would be a child of the tree being filled.
func TestContextCapIsPerTree(t *testing.T) {
	body := "Placeholder child with a brief long enough to pass the minimum body rule."
	fill := func(db *DB, agent string) *Task {
		db.SetMaxTasksPerContext(2)
		db.CreateTask(NewTask{CreatedBy: "human", Title: "work", AssignedTo: agent})
		parent, _ := claimStart(t, db, agent)
		if _, err := db.CreateSubTask(parent.ID, agent, NewTask{Title: "child", Body: body}); err != nil {
			t.Fatalf("first child: %v", err)
		}
		return parent
	}

	full := testDB(t)
	parent := fill(full, "a1")
	if _, err := full.CreateSubTask(parent.ID, "a1", NewTask{Title: "over", Body: body}); err == nil {
		t.Fatal("the first tree was not capped")
	}

	fresh := testDB(t)
	fill(fresh, "a1") // its own tree, its own count
}

// TestContextCapDefaultsWhenUnset guards the knob: config absent means 50, not
// zero, and zero would refuse every child.
func TestContextCapDefaultsWhenUnset(t *testing.T) {
	db := testDB(t)
	if got := db.MaxTasksPerContext(); got != DefaultMaxTasksPerContext {
		t.Fatalf("cap with no config: got %d, want %d", got, DefaultMaxTasksPerContext)
	}
	db.SetMaxTasksPerContext(0)
	if got := db.MaxTasksPerContext(); got != DefaultMaxTasksPerContext {
		t.Fatalf("cap set to 0: got %d, want the default %d", got, DefaultMaxTasksPerContext)
	}
}
