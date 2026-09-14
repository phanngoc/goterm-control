package coord

import (
	"testing"
	"time"
)

// The case this exists for: a deploy restarts the gateway while a task is
// mid-run. Before this the task sat untouched for the rest of its ten-minute
// lease, and the reclaim that followed charged it an attempt it never used.
func TestARestartGivesTheWorkBackWithItsAttempt(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw")

	task, err := db.CreateTask(NewTask{CreatedBy: "bomclaw2", AssignedTo: "bomclaw", Title: "visualize"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := db.ClaimTask("bomclaw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.StartRun(claimed.ID, "bomclaw", claimed.Attempts, ""); err != nil {
		t.Fatal(err)
	}
	if claimed.Attempts != 1 {
		t.Fatalf("attempts = %d after one claim", claimed.Attempts)
	}

	freed, err := db.ReleaseInterruptedRuns("bomclaw")
	if err != nil {
		t.Fatal(err)
	}
	if len(freed) != 1 || freed[0] != task.ID {
		t.Fatalf("freed %v, want the task that was in flight", freed)
	}

	back, err := db.GetTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if back.State != TaskSubmitted {
		t.Errorf("state = %q, want it back in the queue", back.State)
	}
	if back.Attempts != 0 {
		t.Errorf("attempts = %d — an interrupted claim never got to fail on its own terms,\n"+
			"and three deploys during one long task would kill it", back.Attempts)
	}
	if !back.LeaseUntil.Before(time.Now()) {
		t.Error("lease still held, so nothing can pick it up until it lapses — the whole problem")
	}

	// Claimable right now, not in ten minutes.
	again, err := db.ClaimTask("bomclaw")
	if err != nil {
		t.Fatalf("could not reclaim at once: %v", err)
	}
	if again.ID != task.ID {
		t.Fatalf("reclaimed %s", again.ID)
	}

	// And the run says what happened, rather than claiming to still be running.
	runs, err := db.TaskRuns(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if runs[0].Liveness != RunInterrupted {
		t.Errorf("first run liveness = %q, want %q", runs[0].Liveness, RunInterrupted)
	}
	if runs[0].EndedAt.IsZero() {
		t.Error("an interrupted run was left open")
	}
}

// The history is where "why did this stop for ten minutes" gets answered.
func TestARestartSaysSoInTheHistory(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw")
	task, err := db.CreateTask(NewTask{CreatedBy: "bomclaw", Title: "việc dài"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, _ := db.ClaimTask("bomclaw")
	if _, err := db.StartRun(claimed.ID, "bomclaw", claimed.Attempts, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ReleaseInterruptedRuns("bomclaw"); err != nil {
		t.Fatal(err)
	}
	events, err := db.TaskEvents(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	if last.ToState != TaskSubmitted || last.Note == "" {
		t.Fatalf("the board still cannot say why it stopped: %+v", last)
	}
}

// Another agent's run is not this one's to release. Three gateways share the
// database, and each only knows its own process just started.
func TestARestartLeavesOtherAgentsWorkAlone(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw", "bomclaw3")
	task, err := db.CreateTask(NewTask{CreatedBy: "bomclaw", AssignedTo: "bomclaw3", Title: "của agent 3"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := db.ClaimTask("bomclaw3")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.StartRun(claimed.ID, "bomclaw3", claimed.Attempts, ""); err != nil {
		t.Fatal(err)
	}

	freed, err := db.ReleaseInterruptedRuns("bomclaw")
	if err != nil {
		t.Fatal(err)
	}
	if len(freed) != 0 {
		t.Fatalf("agent 1 restarting pulled agent 3's running task out from under it: %v", freed)
	}
	still, err := db.GetTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if still.State != TaskWorking || still.ClaimedBy != "bomclaw3" {
		t.Fatalf("task disturbed: %+v", still)
	}
}

func TestFilingATaskUnderAProject(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw")
	task, err := db.CreateTask(NewTask{CreatedBy: "bomclaw", Title: "chưa xếp vào đâu"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetTaskChannel(task.ID, GeneralChannelID); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ChannelID != GeneralChannelID {
		t.Fatalf("channel = %q", got.ChannelID)
	}
	// A project that does not exist would send the next run into a folder that
	// does not either.
	if err := db.SetTaskChannel(task.ID, "ch_khong_co"); err == nil {
		t.Error("filed under a project that does not exist")
	}
	// Clearing is allowed: a task can be taken back out of a project.
	if err := db.SetTaskChannel(task.ID, ""); err != nil {
		t.Fatal(err)
	}
}

func TestAFinishedTaskKeepsTheProjectItRanUnder(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw")
	task, err := db.CreateTask(NewTask{CreatedBy: "bomclaw", Title: "xong rồi"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ClaimTask("bomclaw"); err != nil {
		t.Fatal(err)
	}
	if err := db.CancelTask(task.ID, "bomclaw"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetTaskChannel(task.ID, GeneralChannelID); err == nil {
		t.Error("moved a finished task, so its history now names a project the work never ran in")
	}
}

// A task can carry several open run rows: one from a restart two deploys ago
// that the orphan reaper has not swept yet, and the live one. Refunding once
// per row would hand back attempts nobody lost — and this is not theoretical,
// the task that prompted this fix had exactly two.
func TestOnlyTheLiveClaimGetsItsAttemptBack(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw")
	task, err := db.CreateTask(NewTask{CreatedBy: "bomclaw", Title: "việc dài, hai lần deploy"})
	if err != nil {
		t.Fatal(err)
	}

	// Claim 1 is killed by a restart, and its run row is left open.
	first, err := db.ClaimTask("bomclaw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.StartRun(first.ID, "bomclaw", first.Attempts, ""); err != nil {
		t.Fatal(err)
	}
	// The lease lapses and the reaper reclaims it, charging attempt 2, without
	// anything having closed run 1.
	if _, err := db.conn.Exec(`UPDATE tasks SET state = ?, lease_until = ? WHERE id = ?`,
		TaskSubmitted, ts(time.Now().Add(-time.Minute)), task.ID); err != nil {
		t.Fatal(err)
	}
	second, err := db.ClaimTask("bomclaw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.StartRun(second.ID, "bomclaw", second.Attempts, ""); err != nil {
		t.Fatal(err)
	}
	if second.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", second.Attempts)
	}

	if _, err := db.ReleaseInterruptedRuns("bomclaw"); err != nil {
		t.Fatal(err)
	}
	back, err := db.GetTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if back.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1: one open run is the live claim and the other is\n"+
			"a leftover — refunding for both hands back an attempt nobody lost", back.Attempts)
	}
	// Both rows are still closed out: a run that says "running" forever is its
	// own lie, whichever claim it belonged to.
	runs, err := db.TaskRuns(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		if r.Liveness == RunRunning {
			t.Errorf("run %s (attempt %d) still claims to be running", r.ID, r.Attempt)
		}
	}
}
