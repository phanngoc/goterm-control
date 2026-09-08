package coord

import (
	"testing"
	"time"
)

// delegated creates a task agent "a2" handed to agent "a1", claims it as a1 and
// finishes it — the shape the reporter is meant to pick up.
func delegated(t *testing.T, db *DB, title, state, result string) *Task {
	t.Helper()
	task, err := db.CreateTask(NewTask{CreatedBy: "a2", AssignedTo: "a1", Title: title})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	c, err := db.ClaimTask("a1")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := db.FinishTask(c.ID, "a1", state, result, c.Attempts); err != nil {
		t.Fatalf("finish: %v", err)
	}
	return task
}

func TestPendingReportsFindsDelegatedWorkThatFinished(t *testing.T) {
	db := testDB(t)
	task := delegated(t, db, "crawl listings", TaskCompleted, "62 jobs")

	got, err := db.PendingReports("a2", 0)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(got) != 1 || got[0].ID != task.ID {
		t.Fatalf("want the one finished task, got %+v", got)
	}
	if got[0].Result != "62 jobs" || got[0].ClaimedBy != "a1" {
		t.Errorf("the report needs the result and who ran it, got %+v", got[0])
	}
}

// The executor is not owed a report about its own work — it was there.
func TestPendingReportsIsScopedToTheRequester(t *testing.T) {
	db := testDB(t)
	delegated(t, db, "crawl listings", TaskCompleted, "62 jobs")

	if got, _ := db.PendingReports("a1", 0); len(got) != 0 {
		t.Errorf("a1 ran the task; it should owe itself nothing, got %+v", got)
	}
	if got, _ := db.PendingReports("a3", 0); len(got) != 0 {
		t.Errorf("an unrelated agent should see nothing, got %+v", got)
	}
}

// Failure is the outcome most worth hearing about, so it reports like any other.
func TestPendingReportsIncludesFailures(t *testing.T) {
	db := testDB(t)
	delegated(t, db, "broken job", TaskFailed, "exit 3")

	got, _ := db.PendingReports("a2", 0)
	if len(got) != 1 || got[0].State != TaskFailed {
		t.Fatalf("a failed hand-off must still be reported, got %+v", got)
	}
}

func TestPendingReportsSkipsWorkStillInFlight(t *testing.T) {
	db := testDB(t)
	if _, err := db.CreateTask(NewTask{CreatedBy: "a2", AssignedTo: "a1", Title: "queued"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.PendingReports("a2", 0); len(got) != 0 {
		t.Errorf("submitted work is not an outcome, got %+v", got)
	}

	if _, err := db.ClaimTask("a1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.PendingReports("a2", 0); len(got) != 0 {
		t.Errorf("working is not an outcome either, got %+v", got)
	}
}

// A task the requester ran itself needs no report: it watched it happen. This is
// what claimed_by != created_by buys, and it is why assigned_to is not the test
// — an unassigned task the requester happens to claim would slip through.
func TestPendingReportsSkipsWorkTheRequesterRanItself(t *testing.T) {
	db := testDB(t)
	if _, err := db.CreateTask(NewTask{CreatedBy: "a2", Title: "anyone can take this"}); err != nil {
		t.Fatal(err)
	}
	c, err := db.ClaimTask("a2") // the creator claims its own unassigned task
	if err != nil {
		t.Fatal(err)
	}
	if err := db.FinishTask(c.ID, "a2", TaskCompleted, "did it myself", c.Attempts); err != nil {
		t.Fatal(err)
	}

	if got, _ := db.PendingReports("a2", 0); len(got) != 0 {
		t.Errorf("the requester ran this one; reporting it would tell them what they just did: %+v", got)
	}
}

// Scheduled work is settled and delivered by scheduler.settle. Matching it here
// too would send every scheduled result to Telegram twice.
func TestPendingReportsLeavesScheduledWorkToTheScheduler(t *testing.T) {
	db := testDB(t)
	if _, err := db.CreateTask(NewTask{
		CreatedBy: "a2", AssignedTo: "a1", Title: "nightly", Kind: KindScheduled, ScheduleID: "sch_1",
	}); err != nil {
		t.Fatal(err)
	}
	c, _ := db.ClaimTask("a1")
	if err := db.FinishTask(c.ID, "a1", TaskCompleted, "done", c.Attempts); err != nil {
		t.Fatal(err)
	}

	if got, _ := db.PendingReports("a2", 0); len(got) != 0 {
		t.Errorf("the scheduler reports its own runs; this would double up: %+v", got)
	}
}

// A sub-task is a detail of its parent's work; the parent's outcome is the one
// that was actually asked for.
func TestPendingReportsReportsRootTasksOnly(t *testing.T) {
	db := testDB(t)
	parent, err := db.CreateTask(NewTask{CreatedBy: "a2", AssignedTo: "a1", Title: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateTask(NewTask{
		CreatedBy: "a2", AssignedTo: "a1", Title: "child", ParentID: parent.ID, Kind: KindSub, Depth: 1,
	}); err != nil {
		t.Fatal(err)
	}
	// Finish the child only.
	c, _ := db.ClaimTask("a1")
	for c.ParentID == "" { // claim order is not guaranteed; find the child
		if err := db.FinishTask(c.ID, "a1", TaskCompleted, "root done", c.Attempts); err != nil {
			t.Fatal(err)
		}
		if c, err = db.ClaimTask("a1"); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.FinishTask(c.ID, "a1", TaskCompleted, "child done", c.Attempts); err != nil {
		t.Fatal(err)
	}

	for _, got := range mustPending(t, db, "a2") {
		if got.ParentID != "" {
			t.Errorf("a sub-task should not be reported on its own: %+v", got)
		}
	}
}

func mustPending(t *testing.T, db *DB, agent string) []Task {
	t.Helper()
	got, err := db.PendingReports(agent, 0)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	return got
}

// The whole point of the marker: every gateway runs the same query against the
// same file, and exactly one of them may deliver.
func TestMarkReportedIsWonOnceOnly(t *testing.T) {
	db := testDB(t)
	task := delegated(t, db, "crawl listings", TaskCompleted, "62 jobs")
	now := time.Now()

	won, err := db.MarkReported(task.ID, now)
	if err != nil {
		t.Fatalf("mark: %v", err)
	}
	if !won {
		t.Fatal("the first caller must win the delivery")
	}

	again, err := db.MarkReported(task.ID, now.Add(time.Second))
	if err != nil {
		t.Fatalf("mark twice: %v", err)
	}
	if again {
		t.Error("a second gateway must lose, or the user hears the same result twice")
	}
}

func TestMarkReportedRemovesItFromThePendingList(t *testing.T) {
	db := testDB(t)
	task := delegated(t, db, "crawl listings", TaskCompleted, "62 jobs")

	if _, err := db.MarkReported(task.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := mustPending(t, db, "a2"); len(got) != 0 {
		t.Errorf("a reported task must not come back round: %+v", got)
	}

	// And the marker is readable, so `task show` can say it was delivered.
	after, err := db.GetTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ReportedAt == "" {
		t.Error("reported_at should be set and scannable through taskCols")
	}
}

func TestPendingReportsRequiresAnAgent(t *testing.T) {
	db := testDB(t)
	if _, err := db.PendingReports("", 0); err == nil {
		t.Error("an empty agent id would match every task in the file; it must be refused")
	}
}
