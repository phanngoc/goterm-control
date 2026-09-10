package coord

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testDB(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "coord.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Never let a test write artifacts into the real shared directory.
	db.SetArtifactsDir(filepath.Join(dir, "artifacts"))
	t.Cleanup(func() { db.Close() })
	return db
}

func TestClaimIsExclusive(t *testing.T) {
	db := testDB(t)

	const n = 20
	for i := 0; i < n; i++ {
		if _, err := db.CreateTask(NewTask{CreatedBy: "a1", Title: "work"}); err != nil {
			t.Fatalf("create: %v", err)
		}
	}

	// Many claimers race for the same queue; every task must go to exactly one.
	var mu sync.Mutex
	claimed := map[string]int{}
	var wg sync.WaitGroup
	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for {
				task, err := db.ClaimTask("agent")
				if errors.Is(err, ErrNoTask) {
					return
				}
				if err != nil {
					t.Errorf("claim: %v", err)
					return
				}
				mu.Lock()
				claimed[task.ID]++
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()

	if len(claimed) != n {
		t.Errorf("claimed %d distinct tasks, want %d", len(claimed), n)
	}
	for id, times := range claimed {
		if times != 1 {
			t.Errorf("task %s was claimed %d times — two agents would duplicate the work", id, times)
		}
	}
}

func TestAssignedTaskOnlyGoesToItsAgent(t *testing.T) {
	db := testDB(t)
	if _, err := db.CreateTask(NewTask{CreatedBy: "a1", AssignedTo: "a2", Title: "for a2"}); err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := db.ClaimTask("a3"); !errors.Is(err, ErrNoTask) {
		t.Errorf("a3 claimed a task addressed to a2 (err=%v)", err)
	}
	if _, err := db.ClaimTask("a2"); err != nil {
		t.Errorf("a2 could not claim its own task: %v", err)
	}
}

func TestExpiredLeaseReturnsTaskToTheQueue(t *testing.T) {
	db := testDB(t)
	created, err := db.CreateTask(NewTask{CreatedBy: "a1", Title: "long job"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	first, err := db.ClaimTask("a1")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := db.ClaimTask("a2"); !errors.Is(err, ErrNoTask) {
		t.Fatalf("a held task must not be claimable (err=%v)", err)
	}

	// Simulate the holder dying: its lease lapses.
	if _, err := db.conn.Exec(`UPDATE tasks SET lease_until = ? WHERE id = ?`,
		ts(time.Now().Add(-time.Minute)), created.ID); err != nil {
		t.Fatalf("expire lease: %v", err)
	}

	second, err := db.ClaimTask("a2")
	if err != nil {
		t.Fatalf("expired lease did not free the task: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("claimed %s, want the abandoned %s", second.ID, first.ID)
	}
	if second.Attempts != first.Attempts+1 {
		t.Errorf("attempts = %d, want %d — the fencing token must advance", second.Attempts, first.Attempts+1)
	}
}

func TestStaleHolderCannotOverwriteTheNewOwnersResult(t *testing.T) {
	db := testDB(t)
	created, _ := db.CreateTask(NewTask{CreatedBy: "a1", Title: "job"})

	stale, err := db.ClaimTask("a1")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	// a1 stalls; the lease expires and a2 picks the work up.
	if _, err := db.conn.Exec(`UPDATE tasks SET lease_until = ? WHERE id = ?`,
		ts(time.Now().Add(-time.Minute)), created.ID); err != nil {
		t.Fatalf("expire: %v", err)
	}
	fresh, err := db.ClaimTask("a2")
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}

	// a1 wakes up and tries to write its result with the old fencing token.
	err = db.FinishTask(stale.ID, "a1", TaskCompleted, "stale answer", stale.Attempts)
	if !errors.Is(err, ErrLostLease) {
		t.Fatalf("stale holder was allowed to finish the task: %v", err)
	}

	if err := db.FinishTask(fresh.ID, "a2", TaskCompleted, "real answer", fresh.Attempts); err != nil {
		t.Fatalf("current owner could not finish: %v", err)
	}

	tasks, _ := db.ListTasks(TaskFilter{})
	if tasks[0].Result != "real answer" {
		t.Errorf("result = %q, want the current owner's", tasks[0].Result)
	}
}

func TestRenewLeaseKeepsTheTaskHeld(t *testing.T) {
	db := testDB(t)
	db.CreateTask(NewTask{CreatedBy: "a1", Title: "job"})
	claimed, _ := db.ClaimTask("a1")

	if err := db.RenewLease(claimed.ID, "a1"); err != nil {
		t.Errorf("owner could not renew: %v", err)
	}
	if err := db.RenewLease(claimed.ID, "a2"); !errors.Is(err, ErrLostLease) {
		t.Errorf("a non-owner renewed the lease: %v", err)
	}
}

func TestDepthGuardStopsAgentPingPong(t *testing.T) {
	db := testDB(t)
	if _, err := db.CreateTask(NewTask{CreatedBy: "a1", Title: "deep", Depth: MaxDepth}); err != nil {
		t.Fatalf("depth == MaxDepth must still be allowed: %v", err)
	}
	if _, err := db.CreateTask(NewTask{CreatedBy: "a1", Title: "too deep", Depth: MaxDepth + 1}); err == nil {
		t.Error("a task past MaxDepth was accepted — two agents could loop forever")
	}
}

func TestExhaustedAttemptsStopBeingClaimed(t *testing.T) {
	db := testDB(t)
	created, _ := db.CreateTask(NewTask{CreatedBy: "a1", Title: "poison"})

	for i := 0; i < 3; i++ {
		if _, err := db.ClaimTask("a1"); err != nil {
			t.Fatalf("claim %d: %v", i, err)
		}
		if _, err := db.conn.Exec(`UPDATE tasks SET lease_until = ? WHERE id = ?`,
			ts(time.Now().Add(-time.Minute)), created.ID); err != nil {
			t.Fatalf("expire: %v", err)
		}
	}
	if _, err := db.ClaimTask("a1"); !errors.Is(err, ErrNoTask) {
		t.Errorf("a task past max_attempts is still being retried: %v", err)
	}
}

func TestEventsRecordTheLifecycle(t *testing.T) {
	db := testDB(t)
	created, _ := db.CreateTask(NewTask{CreatedBy: "a1", Title: "job"})
	claimed, _ := db.ClaimTask("a2")
	db.FinishTask(claimed.ID, "a2", TaskCompleted, "done", claimed.Attempts)

	events, err := db.TaskEvents(created.ID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events, want created/claimed/completed", len(events))
	}
	if events[0].ToState != TaskSubmitted || events[1].ToState != TaskWorking || events[2].ToState != TaskCompleted {
		t.Errorf("unexpected lifecycle: %v → %v → %v",
			events[0].ToState, events[1].ToState, events[2].ToState)
	}
}

// blockedOnPerson parks a claimed task on a human, with the agent's own note as
// the checkpoint — the shape the admin board shows as "waiting on you".
func blockedOnPerson(t *testing.T, db *DB, question string) *Task {
	t.Helper()
	task, err := db.CreateTask(NewTask{CreatedBy: "a2", AssignedTo: "a1", Title: "needs a decision"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	c, err := db.ClaimTask("a1")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := db.BlockTask(c.ID, "a1", c.Attempts, BlockedOnHuman, question); err != nil {
		t.Fatalf("block: %v", err)
	}
	return task
}

// The answer has to reach the agent, and checkpoint is the only field that does
// — taskPrompt feeds it into the next run. Recording it as an audit event alone
// would look correct and change nothing about what the agent reads.
func TestUnblockPutsTheAnswerWhereTheAgentReadsIt(t *testing.T) {
	db := testDB(t)
	task := blockedOnPerson(t, db, "Which of the two layouts should I ship?")

	if err := db.UnblockTask(task.ID, "human", "Ship the second one."); err != nil {
		t.Fatalf("unblock: %v", err)
	}

	got, err := db.GetTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != TaskSubmitted || got.BlockedOn != "" {
		t.Errorf("state=%s blocked_on=%q, want submitted and cleared", got.State, got.BlockedOn)
	}
	// Both halves survive: the question the agent wrote, and the reply.
	if !strings.Contains(got.Checkpoint, "Which of the two layouts") {
		t.Errorf("the agent's own note was lost:\n%s", got.Checkpoint)
	}
	if !strings.Contains(got.Checkpoint, "Ship the second one.") {
		t.Errorf("the answer never reached the checkpoint:\n%s", got.Checkpoint)
	}
	if !strings.Contains(got.Checkpoint, "Answer from human") {
		t.Errorf("the answer should be attributed:\n%s", got.Checkpoint)
	}
}

func TestUnblockWithoutAnAnswerLeavesTheCheckpointAlone(t *testing.T) {
	db := testDB(t)
	task := blockedOnPerson(t, db, "original note")

	if err := db.UnblockTask(task.ID, "human", ""); err != nil {
		t.Fatalf("unblock: %v", err)
	}
	got, _ := db.GetTask(task.ID)
	if got.Checkpoint != "original note" {
		t.Errorf("checkpoint = %q, want it untouched", got.Checkpoint)
	}
	if strings.Contains(got.Checkpoint, "Answer from") {
		t.Error("an empty answer must not be attributed to anyone")
	}
}

// The merge and the unblock are one guarded statement. Doing the merge first, as
// the CLI used to, wrote the answer onto a task that then turned out not to be
// blocked — leaving a reply to a question nobody had asked.
func TestUnblockingAnUnblockedTaskChangesNothing(t *testing.T) {
	db := testDB(t)
	task, err := db.CreateTask(NewTask{CreatedBy: "a2", AssignedTo: "a1", Title: "not blocked"})
	if err != nil {
		t.Fatal(err)
	}

	if err := db.UnblockTask(task.ID, "human", "an answer to nothing"); err == nil {
		t.Fatal("unblocking a task that is not blocked must fail")
	}

	got, _ := db.GetTask(task.ID)
	if got.Checkpoint != "" {
		t.Errorf("checkpoint was written despite the failure: %q", got.Checkpoint)
	}
	if got.State != TaskSubmitted {
		t.Errorf("state = %s, want it unchanged", got.State)
	}
}

// Answering twice is a person clicking twice; the second one must not reopen
// work that is already back in the queue and possibly running.
func TestUnblockIsNotRepeatable(t *testing.T) {
	db := testDB(t)
	task := blockedOnPerson(t, db, "question")

	if err := db.UnblockTask(task.ID, "human", "first"); err != nil {
		t.Fatal(err)
	}
	if err := db.UnblockTask(task.ID, "human", "second"); err == nil {
		t.Error("the second answer must be refused")
	}
	got, _ := db.GetTask(task.ID)
	if strings.Contains(got.Checkpoint, "second") {
		t.Errorf("the refused answer leaked into the checkpoint:\n%s", got.Checkpoint)
	}
}

// The deadlock seen in production: a task requeued as submitted with its
// attempts spent could not be claimed (ClaimTask needs attempts <
// max_attempts), could not be reaped (the sweep only looked at working), and
// could not be resumed (resume took a failed task only). Eight more tasks sat
// blocked behind it.
//
// This drives the state through the real path that created it — a run that
// advanced, repeatedly — rather than writing the row by hand.
func TestProgressDoesNotSpendTheFailureBudget(t *testing.T) {
	db := testDB(t)
	task, err := db.CreateTask(NewTask{CreatedBy: "a2", AssignedTo: "a1", Title: "long job"})
	if err != nil {
		t.Fatal(err)
	}

	// Six continuations — twice the three-attempt budget.
	for i := range 6 {
		c, err := db.ClaimTask("a1")
		if err != nil {
			t.Fatalf("claim %d: %v (a progressing task became unclaimable)", i+1, err)
		}
		run, err := db.StartRun(c.ID, "a1", c.Attempts, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.FinishRun(run.ID, RunOutcome{
			Liveness: RunAdvanced, Checkpoint: fmt.Sprintf("step %d done", i+1),
		}); err != nil {
			t.Fatalf("finish %d: %v", i+1, err)
		}
	}

	got, err := db.GetTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != TaskSubmitted {
		t.Fatalf("state = %s, want submitted and claimable", got.State)
	}
	if got.Attempts >= got.MaxAttempts {
		t.Errorf("attempts = %d/%d — progress spent the failure budget, which strands the task",
			got.Attempts, got.MaxAttempts)
	}
	if got.Continuations != 6 {
		t.Errorf("continuations = %d, want 6 — progress is what should be counted", got.Continuations)
	}
	// The proof: it can still be picked up.
	if _, err := db.ClaimTask("a1"); err != nil {
		t.Errorf("still unclaimable after 6 continuations: %v", err)
	}
}

// strandTask forces the dead-end state directly, standing in for the rows that
// already exist in a live database from before the refund above.
func strandTask(t *testing.T, db *DB) *Task {
	t.Helper()
	task, err := db.CreateTask(NewTask{CreatedBy: "a2", AssignedTo: "a1", Title: "stranded"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec(
		`UPDATE tasks SET state = ?, attempts = max_attempts WHERE id = ?`,
		TaskSubmitted, task.ID); err != nil {
		t.Fatal(err)
	}
	return task
}

func TestStrandedTaskIsUnclaimable(t *testing.T) {
	db := testDB(t)
	strandTask(t, db)
	if _, err := db.ClaimTask("a1"); !errors.Is(err, ErrNoTask) {
		t.Errorf("claim = %v, want ErrNoTask — this is the dead end, and the premise of the rest", err)
	}
}

// The sweep is what hands a stuck queue back to a person: once the row is
// failed, `task resume` can grant it more attempts.
func TestReapExhaustedRescuesAStrandedTask(t *testing.T) {
	db := testDB(t)
	task := strandTask(t, db)

	ids, err := db.ReapExhausted()
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if len(ids) != 1 || ids[0] != task.ID {
		t.Fatalf("reaped %v, want the stranded task", ids)
	}

	got, _ := db.GetTask(task.ID)
	if got.State != TaskFailed || got.FailReason != FailExhausted {
		t.Errorf("state=%s reason=%s, want failed/exhausted", got.State, got.FailReason)
	}
	// And the audit says which state it came from, not a guess.
	events, _ := db.TaskEvents(task.ID)
	last := events[len(events)-1]
	if last.FromState != TaskSubmitted || !strings.Contains(last.Note, "stranded") {
		t.Errorf("event = %s→%s %q, want it to record the stranding", last.FromState, last.ToState, last.Note)
	}
}

// A working task with an expired lease is the case the sweep already handled;
// it must keep working.
func TestReapExhaustedStillFailsADeadHolder(t *testing.T) {
	db := testDB(t)
	if _, err := db.CreateTask(NewTask{CreatedBy: "a2", AssignedTo: "a1", Title: "held"}); err != nil {
		t.Fatal(err)
	}
	c, _ := db.ClaimTask("a1")
	if _, err := db.Conn().Exec(
		`UPDATE tasks SET attempts = max_attempts, lease_until = ? WHERE id = ?`,
		"2000-01-01T00:00:00Z", c.ID); err != nil {
		t.Fatal(err)
	}

	ids, err := db.ReapExhausted()
	if err != nil || len(ids) != 1 {
		t.Fatalf("reap = %v, %v; want the dead holder failed", ids, err)
	}
	events, _ := db.TaskEvents(c.ID)
	if last := events[len(events)-1]; last.FromState != TaskWorking {
		t.Errorf("from-state = %s, want working for a dead holder", last.FromState)
	}
}

// A submitted task with attempts left is simply waiting its turn, and must not
// be touched.
func TestReapExhaustedLeavesClaimableWorkAlone(t *testing.T) {
	db := testDB(t)
	task, err := db.CreateTask(NewTask{CreatedBy: "a2", AssignedTo: "a1", Title: "queued"})
	if err != nil {
		t.Fatal(err)
	}
	if ids, err := db.ReapExhausted(); err != nil || len(ids) != 0 {
		t.Fatalf("reap = %v, %v; a fresh task must be left alone", ids, err)
	}
	if got, _ := db.GetTask(task.ID); got.State != TaskSubmitted {
		t.Errorf("state = %s, want submitted", got.State)
	}
}

// Resume is the escape hatch, and a person should not have to wait for a sweep.
func TestResumeAcceptsAStrandedTask(t *testing.T) {
	db := testDB(t)
	task := strandTask(t, db)
	before, _ := db.GetTask(task.ID)

	if err := db.ResumeTask(task.ID, "human", 5); err != nil {
		t.Fatalf("resume: %v — this is the state resume exists for", err)
	}
	got, _ := db.GetTask(task.ID)
	if got.MaxAttempts != before.MaxAttempts+5 {
		t.Errorf("max_attempts = %d, want %d", got.MaxAttempts, before.MaxAttempts+5)
	}
	if _, err := db.ClaimTask("a1"); err != nil {
		t.Errorf("still unclaimable after resume: %v", err)
	}
}

// The old message named the state and stopped; it now says what resume accepts,
// because the previous one sent people to a command that could not help.
func TestResumeStillRefusesWorkInFlight(t *testing.T) {
	db := testDB(t)
	if _, err := db.CreateTask(NewTask{CreatedBy: "a2", AssignedTo: "a1", Title: "busy"}); err != nil {
		t.Fatal(err)
	}
	c, _ := db.ClaimTask("a1") // now working, attempts 1/3

	err := db.ResumeTask(c.ID, "human", 5)
	if err == nil {
		t.Fatal("a working task with attempts left must not be resumable")
	}
	if !strings.Contains(err.Error(), "1/3") {
		t.Errorf("error should show the attempt count that explains the refusal: %v", err)
	}
}

// Two operators clicking Resume must grant the allowance once.
func TestResumeIsGuardedAgainstADoubleClick(t *testing.T) {
	db := testDB(t)
	task := strandTask(t, db)
	before, _ := db.GetTask(task.ID)

	if err := db.ResumeTask(task.ID, "human", 5); err != nil {
		t.Fatal(err)
	}
	if err := db.ResumeTask(task.ID, "human", 5); err == nil {
		t.Error("the second resume must be refused, not stack another +5")
	}
	got, _ := db.GetTask(task.ID)
	if got.MaxAttempts != before.MaxAttempts+5 {
		t.Errorf("max_attempts = %d, want a single +5 (%d)", got.MaxAttempts, before.MaxAttempts+5)
	}
}
