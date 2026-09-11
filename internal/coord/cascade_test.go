package coord

import (
	"strings"
	"testing"
	"time"
)

// Cancelling a parent takes its unfinished descendants with it — grandchildren
// too — and leaves finished ones as they are.
func TestCancelTaskTreeCascadesToOpenDescendants(t *testing.T) {
	db := testDB(t)
	db.CreateTask(NewTask{CreatedBy: "human", Title: "Report", AssignedTo: "a1"})
	parent, prun := claimStart(t, db, "a1")
	mustSub := func(title string) *Task {
		t.Helper()
		c, err := db.CreateSubTask(parent.ID, "a1", NewTask{Title: title, AssignedTo: "a2",
			Body: "Write the section " + title + " of the report: two paragraphs, cite the source, plain text."})
		if err != nil {
			t.Fatalf("sub %s: %v", title, err)
		}
		return c
	}
	c1, c2, c3 := mustSub("Part 1"), mustSub("Part 2"), mustSub("Part 3")
	if err := db.BlockTask(parent.ID, "a1", parent.Attempts, BlockedOnChildren, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FinishRun(prun.ID, RunOutcome{Liveness: RunBlocked, BlockedOn: BlockedOnChildren}); err != nil {
		t.Fatal(err)
	}

	// c1 finishes; c2 is running on a2 and has a grandchild; c3 is queued.
	t1, r1 := claimStart(t, db, "a2")
	if t1.ID != c1.ID {
		t.Fatal("precondition: c1 first")
	}
	db.FinishRun(r1.ID, RunOutcome{Liveness: RunCompleted, Result: "part 1 done"})
	t2, r2 := claimStart(t, db, "a2")
	if t2.ID != c2.ID {
		t.Fatal("precondition: c2 second")
	}
	grand, err := db.CreateSubTask(c2.ID, "a2", NewTask{Title: "Part 2a",
		Body: "Collect the figures for part 2 from the finance sheet and list them as a table."})
	if err != nil {
		t.Fatal(err)
	}

	canceled, err := db.CancelTaskTree(parent.ID, "human")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{c2.ID: true, c3.ID: true, grand.ID: true}
	if len(canceled) != len(want) {
		t.Fatalf("canceled %v, want c2, c3 and the grandchild", canceled)
	}
	for _, id := range canceled {
		if !want[id] {
			t.Errorf("unexpected cancel of %s", id)
		}
	}
	for id, wantState := range map[string]string{parent.ID: TaskCanceled, c1.ID: TaskCompleted, c2.ID: TaskCanceled, c3.ID: TaskCanceled, grand.ID: TaskCanceled} {
		got, _ := db.GetTask(id)
		if got.State != wantState {
			t.Errorf("%s: state %s, want %s", got.Title, got.State, wantState)
		}
	}
	// The finished child keeps its result; the canceled ones say why.
	if got, _ := db.GetTask(c1.ID); got.Result != "part 1 done" {
		t.Errorf("finished child touched: %+v", got)
	}
	events, _ := db.TaskEvents(grand.ID)
	if len(events) == 0 || !strings.Contains(events[len(events)-1].Note, "canceled with parent "+parent.ID) {
		t.Errorf("grandchild should record why: %+v", events)
	}
	// The run still going on a2 closes against the canceled task, not over it.
	if _, err := db.FinishRun(r2.ID, RunOutcome{Liveness: RunCompleted, Result: "too late"}); err != ErrTaskFinished {
		t.Errorf("late finish: %v, want ErrTaskFinished", err)
	}
	if got, _ := db.GetTask(c2.ID); got.State != TaskCanceled || got.Result == "too late" {
		t.Errorf("late result overwrote the cancel: %+v", got)
	}
	// Nothing is left for the parent to be woken by, and nothing is claimable.
	if woken, _ := db.WakeParents(time.Now()); len(woken) != 0 {
		t.Error("a canceled parent must not be woken")
	}
	if _, err := db.ClaimTask("a2"); err != ErrNoTask {
		t.Errorf("claim after cascade: %v", err)
	}
	// Cancelling again is refused, and a leaf cancel cascades to nothing.
	if _, err := db.CancelTaskTree(parent.ID, "human"); err == nil {
		t.Error("second cancel should be refused")
	}
	leaf, _ := db.CreateTask(NewTask{CreatedBy: "h", Title: "leaf"})
	if kids, err := db.CancelTaskTree(leaf.ID, "h"); err != nil || len(kids) != 0 {
		t.Errorf("leaf cancel: %v %v", kids, err)
	}
}

func TestPurgeScheduleRunsBeforeKeepsPendingAndRecent(t *testing.T) {
	db := testDB(t)
	s := newSchedule(t, db, "s", time.Now())
	now := time.Now()
	old := now.Add(-40 * 24 * time.Hour)
	db.RecordScheduleRun(ScheduleRun{ScheduleID: s.ID, StartedAt: old, EndedAt: old, Status: ScheduleRunOK})
	db.RecordScheduleRun(ScheduleRun{ScheduleID: s.ID, StartedAt: old, EndedAt: old, Status: ScheduleRunFailed})
	db.RecordScheduleRun(ScheduleRun{ScheduleID: s.ID, TaskID: "t_x", StartedAt: old, Status: ScheduleRunPending})
	db.RecordScheduleRun(ScheduleRun{ScheduleID: s.ID, StartedAt: now, EndedAt: now, Status: ScheduleRunOK})

	n, err := db.PurgeScheduleRunsBefore(now.Add(-30 * 24 * time.Hour))
	if err != nil || n != 2 {
		t.Fatalf("purged %d (%v), want 2", n, err)
	}
	runs, _ := db.ScheduleRuns(s.ID, 10)
	if len(runs) != 2 {
		t.Fatalf("left %d runs, want the pending and the recent one", len(runs))
	}
	for _, r := range runs {
		if r.Status != ScheduleRunPending && !r.StartedAt.After(now.Add(-time.Minute)) {
			t.Errorf("wrong survivor: %+v", r)
		}
	}
	if n, _ := db.PurgeScheduleRunsBefore(now.Add(-30 * 24 * time.Hour)); n != 0 {
		t.Error("second purge should find nothing")
	}
}
