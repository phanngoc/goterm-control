package coord

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "coord.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
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
