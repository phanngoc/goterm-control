package coord

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ngocp/goterm-control/internal/storage"
)

// claimAndStart is the runner's opening move: claim, then open the run ledger.
func claimAndStart(t *testing.T, db *DB, agent string) (*Task, *TaskRun) {
	t.Helper()
	task, err := db.ClaimTask(agent)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	run, err := db.StartRun(task.ID, agent, task.Attempts, "")
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	return task, run
}

func newTask(t *testing.T, db *DB) *Task {
	t.Helper()
	task, err := db.CreateTask(NewTask{CreatedBy: "human", Title: "long job"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return task
}

func TestRunCompletedFinishesTheTask(t *testing.T) {
	db := testDB(t)
	newTask(t, db)
	_, run := claimAndStart(t, db, "a1")

	task, err := db.FinishRun(run.ID, RunOutcome{Liveness: RunCompleted, Result: "the report"})
	if err != nil {
		t.Fatal(err)
	}
	if task.State != TaskCompleted || task.Result != "the report" {
		t.Fatalf("task = %s %q, want completed with the result", task.State, task.Result)
	}
	runs, _ := db.TaskRuns(task.ID)
	if len(runs) != 1 || runs[0].Liveness != RunCompleted || runs[0].EndedAt.IsZero() {
		t.Fatalf("run ledger = %+v", runs)
	}
}

// The whole point: a run that ran out of time but recorded progress is not a
// failure. The task goes back to the queue, pinned to the agent whose config
// directory holds the CLI session, with the checkpoint and session kept for
// the next run to resume from.
func TestAdvancedRunRequeuesWithContextAndAffinity(t *testing.T) {
	db := testDB(t)
	newTask(t, db)
	_, run := claimAndStart(t, db, "a1")

	ref := SessionRef{Provider: "claude", SessionID: "sess-123", Account: "work"}
	task, err := db.FinishRun(run.ID, RunOutcome{
		Liveness: RunAdvanced, Checkpoint: "sources 1-3 summarised", SessionRef: ref,
	})
	if err != nil {
		t.Fatal(err)
	}
	if task.State != TaskSubmitted {
		t.Errorf("state = %s, want submitted (call me back)", task.State)
	}
	if task.AssignedTo != "a1" {
		t.Errorf("assigned_to = %q, want a1: the session lives in a1's config dir", task.AssignedTo)
	}
	if task.Continuations != 1 || task.Attempts != 1 {
		t.Errorf("continuations=%d attempts=%d; progress must count as a continuation, not a failed attempt", task.Continuations, task.Attempts)
	}
	if task.Checkpoint != "sources 1-3 summarised" {
		t.Errorf("checkpoint lost: %q", task.Checkpoint)
	}
	if got := ParseSessionRef(task.SessionRef); got != ref {
		t.Errorf("session ref = %+v, want %+v", got, ref)
	}

	// And it is immediately claimable again — by a1, not by a2.
	if _, err := db.ClaimTask("a2"); !errors.Is(err, ErrNoTask) {
		t.Errorf("a2 claimed a task pinned to a1 (err=%v)", err)
	}
	again, err := db.ClaimTask("a1")
	if err != nil {
		t.Fatalf("a1 could not continue its own task: %v", err)
	}
	if again.Attempts != 2 {
		t.Errorf("second claim attempts = %d, want 2", again.Attempts)
	}
}

func TestTimedOutWithCheckpointIsProgressWithoutIsAnAttempt(t *testing.T) {
	db := testDB(t)

	// With a checkpoint: a long job, continuation, no attempt lost.
	newTask(t, db)
	_, run := claimAndStart(t, db, "a1")
	task, err := db.FinishRun(run.ID, RunOutcome{Liveness: RunTimedOut, Checkpoint: "halfway"})
	if err != nil {
		t.Fatal(err)
	}
	if task.State != TaskSubmitted || task.Continuations != 1 {
		t.Errorf("timed out WITH progress: state=%s continuations=%d, want submitted/1", task.State, task.Continuations)
	}

	// Without a checkpoint on the last permitted attempt: exhausted. A fresh
	// database, so the requeued task above cannot be the one claimed here.
	db = testDB(t)
	t2 := newTask(t, db)
	var last *Task
	for i := 0; i < 3; i++ {
		_, run := claimAndStart(t, db, "a1")
		last, err = db.FinishRun(run.ID, RunOutcome{Liveness: RunTimedOut})
		if err != nil {
			t.Fatal(err)
		}
		if i < 2 && last.State != TaskSubmitted {
			t.Fatalf("attempt %d: state = %s, want submitted (retry)", i+1, last.State)
		}
	}
	if last.ID != t2.ID || last.State != TaskFailed || last.FailReason != FailExhausted {
		t.Errorf("after 3 silent timeouts: state=%s reason=%q, want failed/exhausted", last.State, last.FailReason)
	}
}

func TestContinuationsAreBounded(t *testing.T) {
	db := testDB(t)
	task := newTask(t, db)
	// Shrink the cap so the test is quick.
	if _, err := db.conn.Exec(`UPDATE tasks SET max_continuations = 3 WHERE id = ?`, task.ID); err != nil {
		t.Fatal(err)
	}
	var last *Task
	for i := 0; i < 3; i++ {
		_, run := claimAndStart(t, db, "a1")
		var err error
		last, err = db.FinishRun(run.ID, RunOutcome{Liveness: RunAdvanced, Checkpoint: "more"})
		if err != nil {
			t.Fatal(err)
		}
	}
	if last.State != TaskFailed || last.FailReason != FailContinuationsExhausted {
		t.Fatalf("after 3 continuations: state=%s reason=%q", last.State, last.FailReason)
	}
	if last.Checkpoint != "more" {
		t.Error("the last checkpoint must survive the failure — it is what a person reads")
	}

	// A person can grant more and the task comes back, keeping its checkpoint.
	if err := db.ResumeTask(task.ID, "human", 5); err != nil {
		t.Fatal(err)
	}
	got, _ := db.GetTask(task.ID)
	if got.State != TaskSubmitted || got.MaxContinuations != 8 || got.FailReason != "" {
		t.Errorf("resumed: state=%s max_cont=%d reason=%q", got.State, got.MaxContinuations, got.FailReason)
	}
}

func TestEmptyRepliesGiveUpQuickly(t *testing.T) {
	db := testDB(t)
	newTask(t, db)

	_, run := claimAndStart(t, db, "a1")
	task, err := db.FinishRun(run.ID, RunOutcome{Liveness: RunEmpty})
	if err != nil {
		t.Fatal(err)
	}
	if task.State != TaskSubmitted {
		t.Fatalf("first empty should retry once, got %s", task.State)
	}
	_, run = claimAndStart(t, db, "a1")
	task, err = db.FinishRun(run.ID, RunOutcome{Liveness: RunEmpty})
	if err != nil {
		t.Fatal(err)
	}
	if task.State != TaskFailed || task.FailReason != FailEmptyExhausted {
		t.Fatalf("second empty: state=%s reason=%q, want failed/empty-exhausted", task.State, task.FailReason)
	}
}

func TestBlockedReleasesTheLeaseButIsNotClaimable(t *testing.T) {
	db := testDB(t)
	newTask(t, db)
	_, run := claimAndStart(t, db, "a1")

	task, err := db.FinishRun(run.ID, RunOutcome{Liveness: RunBlocked, BlockedOn: BlockedOnHuman, Note: "need the budget figure"})
	if err != nil {
		t.Fatal(err)
	}
	if task.State != TaskBlocked || task.BlockedOn != BlockedOnHuman {
		t.Fatalf("state=%s blocked_on=%q", task.State, task.BlockedOn)
	}
	if _, err := db.ClaimTask("a1"); !errors.Is(err, ErrNoTask) {
		t.Error("a blocked task must not be claimable")
	}
	// A person can still cancel it.
	if err := db.CancelTask(task.ID, "human"); err != nil {
		t.Errorf("cancel blocked: %v", err)
	}
}

func TestFailedRunRetriesThenExhausts(t *testing.T) {
	db := testDB(t)
	newTask(t, db)
	var last *Task
	for i := 1; i <= 3; i++ {
		_, run := claimAndStart(t, db, "a1")
		var err error
		last, err = db.FinishRun(run.ID, RunOutcome{Liveness: RunFailed, Note: "CLI crashed"})
		if err != nil {
			t.Fatal(err)
		}
		if i < 3 && last.State != TaskSubmitted {
			t.Fatalf("attempt %d: %s, want submitted (retry)", i, last.State)
		}
	}
	if last.State != TaskFailed || last.FailReason != FailExhausted {
		t.Fatalf("after 3 failures: state=%s reason=%q", last.State, last.FailReason)
	}
	// Once failed, ClaimTask never sees it again — no more "working with an
	// expired lease" limbo.
	if _, err := db.ClaimTask("a1"); !errors.Is(err, ErrNoTask) {
		t.Error("an exhausted task must not be claimable")
	}
}

// A gateway restart cancels the run's context. That must not spend one of the
// task's three attempts, or three restarts would kill any task.
func TestCanceledRunRefundsTheAttempt(t *testing.T) {
	db := testDB(t)
	newTask(t, db)
	_, run := claimAndStart(t, db, "a1")

	task, err := db.FinishRun(run.ID, RunOutcome{Liveness: RunCanceled})
	if err != nil {
		t.Fatal(err)
	}
	if task.State != TaskSubmitted || task.Attempts != 0 || task.Continuations != 0 {
		t.Fatalf("canceled: state=%s attempts=%d continuations=%d, want submitted/0/0", task.State, task.Attempts, task.Continuations)
	}
}

// If the lease was lost mid-run, the run is recorded and the task — now
// someone else's — is not touched.
func TestFinishRunIsFenced(t *testing.T) {
	db := testDB(t)
	task := newTask(t, db)
	_, run := claimAndStart(t, db, "a1")

	// Simulate a1 dying and a2 taking over.
	if _, err := db.conn.Exec(`UPDATE tasks SET lease_until = ? WHERE id = ?`, ts(time.Now().Add(-time.Minute)), task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ClaimTask("a2"); err != nil {
		t.Fatalf("a2 reclaim: %v", err)
	}

	got, err := db.FinishRun(run.ID, RunOutcome{Liveness: RunCompleted, Result: "stale answer"})
	if !errors.Is(err, ErrLostLease) {
		t.Fatalf("expected ErrLostLease, got %v", err)
	}
	if got.ClaimedBy != "a2" || got.State != TaskWorking || got.Result != "" {
		t.Errorf("a2's task was modified by a1's stale run: %+v", got)
	}
	runs, _ := db.TaskRuns(task.ID)
	if runs[0].Liveness != RunCompleted || runs[0].Note == "" {
		t.Errorf("the stale run should still be recorded with a note, got %+v", runs[0])
	}
}

// A person cancelled the task while the run was going: record the run, leave
// the task cancelled — never resurrect it.
func TestFinishRunLeavesAFinishedTaskAlone(t *testing.T) {
	db := testDB(t)
	task := newTask(t, db)
	_, run := claimAndStart(t, db, "a1")
	if err := db.CancelTask(task.ID, "human"); err != nil {
		t.Fatal(err)
	}

	got, err := db.FinishRun(run.ID, RunOutcome{Liveness: RunAdvanced, Checkpoint: "still going"})
	if !errors.Is(err, ErrTaskFinished) {
		t.Fatalf("expected ErrTaskFinished, got %v", err)
	}
	if got.State != TaskCanceled {
		t.Errorf("task was resurrected to %s", got.State)
	}
}

func TestFinishRunTwiceIsRefused(t *testing.T) {
	db := testDB(t)
	newTask(t, db)
	_, run := claimAndStart(t, db, "a1")
	if _, err := db.FinishRun(run.ID, RunOutcome{Liveness: RunCompleted}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FinishRun(run.ID, RunOutcome{Liveness: RunFailed}); err == nil {
		t.Error("a run must not be closed twice")
	}
}

func TestSetCheckpointIsFenced(t *testing.T) {
	db := testDB(t)
	task := newTask(t, db)
	claimed, _ := claimAndStart(t, db, "a1")

	if err := db.SetCheckpoint(task.ID, "a1", claimed.Attempts, "step 1 done"); err != nil {
		t.Fatalf("holder's checkpoint refused: %v", err)
	}
	if err := db.SetCheckpoint(task.ID, "a1", claimed.Attempts+1, "wrong token"); !errors.Is(err, ErrLostLease) {
		t.Errorf("wrong fencing token should be ErrLostLease, got %v", err)
	}
	if err := db.SetCheckpoint(task.ID, "a2", claimed.Attempts, "not mine"); !errors.Is(err, ErrLostLease) {
		t.Errorf("another agent should be ErrLostLease, got %v", err)
	}
	if err := db.SetCheckpoint(task.ID, "a1", claimed.Attempts, "   "); err == nil {
		t.Error("an empty note is not a checkpoint")
	}
	got, _ := db.GetTask(task.ID)
	if got.Checkpoint != "step 1 done" {
		t.Errorf("checkpoint = %q", got.Checkpoint)
	}
}

// The old dead-letter bug: after max_attempts the task sat in `working` with an
// expired lease forever, unclaimable but counted as open.
func TestReapExhaustedFailsStuckTasks(t *testing.T) {
	db := testDB(t)
	task := newTask(t, db)
	// Three claims that never reported back (the agent died each time).
	for i := 0; i < 3; i++ {
		if _, err := db.ClaimTask("a1"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.conn.Exec(`UPDATE tasks SET lease_until = ? WHERE id = ?`, ts(time.Now().Add(-time.Minute)), task.ID); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := db.GetTask(task.ID)
	if before.State != TaskWorking {
		t.Fatalf("setup: expected the stuck `working` state, got %s", before.State)
	}

	ids, err := db.ReapExhausted()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != task.ID {
		t.Fatalf("reaped %v, want [%s]", ids, task.ID)
	}
	after, _ := db.GetTask(task.ID)
	if after.State != TaskFailed || after.FailReason != FailExhausted {
		t.Errorf("state=%s reason=%q", after.State, after.FailReason)
	}
	st, _ := db.Stats()
	if st.OpenTasks != 0 {
		t.Errorf("open tasks = %d, want 0 — the reaped task must stop counting as open", st.OpenTasks)
	}
}

func TestReapLeavesLiveAndRetryableTasksAlone(t *testing.T) {
	db := testDB(t)
	newTask(t, db)
	if _, err := db.ClaimTask("a1"); err != nil { // attempt 1 of 3, lease live
		t.Fatal(err)
	}
	ids, err := db.ReapExhausted()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Errorf("reaped a task that still has attempts and a live lease: %v", ids)
	}
}

func TestRelaxDeadAssignmentsOpensTaskAndDropsSession(t *testing.T) {
	db := testDB(t)
	for _, id := range []string{"alive", "dead"} {
		if err := db.RegisterAgent(Agent{ID: id, DisplayName: id}); err != nil {
			t.Fatal(err)
		}
	}
	// "dead" last heartbeated an hour ago.
	if _, err := db.conn.Exec(`UPDATE agents SET last_seen_at = ? WHERE id = 'dead'`, ts(time.Now().Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}

	toDead, _ := db.CreateTask(NewTask{CreatedBy: "human", AssignedTo: "dead", Title: "for the dead one"})
	toAlive, _ := db.CreateTask(NewTask{CreatedBy: "human", AssignedTo: "alive", Title: "for the live one"})
	if _, err := db.conn.Exec(`UPDATE tasks SET session_ref = ? WHERE id = ?`,
		SessionRef{Provider: "claude", SessionID: "s1"}.String(), toDead.ID); err != nil {
		t.Fatal(err)
	}

	ids, err := db.RelaxDeadAssignments(StaleAfter)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != toDead.ID {
		t.Fatalf("relaxed %v, want only the dead agent's task", ids)
	}
	got, _ := db.GetTask(toDead.ID)
	if got.AssignedTo != "" || got.SessionRef != "" {
		t.Errorf("assigned_to=%q session_ref=%q, want both cleared", got.AssignedTo, got.SessionRef)
	}
	still, _ := db.GetTask(toAlive.ID)
	if still.AssignedTo != "alive" {
		t.Errorf("the live agent's assignment was relaxed")
	}
	// Anyone can take it now.
	if _, err := db.ClaimTask("alive"); err != nil {
		t.Errorf("relaxed task not claimable by another agent: %v", err)
	}
}

// A database created by the previous version — no v3 columns, no task_runs —
// must open and gain them, so a deploy does not strand existing tasks.
func TestMigrateV2DatabaseGainsV3Columns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coord.db")
	raw, err := sql.Open("sqlite", storage.DSN(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL) STRICT`,
		`INSERT INTO meta VALUES ('schema_version', '2')`,
		`CREATE TABLE tasks (
			id TEXT PRIMARY KEY, context_id TEXT NOT NULL, created_by TEXT NOT NULL,
			assigned_to TEXT NOT NULL DEFAULT '', claimed_by TEXT NOT NULL DEFAULT '',
			state TEXT NOT NULL DEFAULT 'submitted', priority INTEGER NOT NULL DEFAULT 0,
			title TEXT NOT NULL, body TEXT NOT NULL DEFAULT '', result TEXT NOT NULL DEFAULT '',
			trace_id TEXT NOT NULL DEFAULT '', lease_until TEXT NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0, max_attempts INTEGER NOT NULL DEFAULT 3,
			depth INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		) STRICT`,
		`INSERT INTO tasks (id, context_id, created_by, title, lease_until, created_at, updated_at)
		 VALUES ('t_old', 'ctx', 'a1', 'from before', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt[:30], err)
		}
	}
	raw.Close()

	db, err := Open(path)
	if err != nil {
		t.Fatalf("open v2 db: %v", err)
	}
	defer db.Close()

	old, err := db.GetTask("t_old")
	if err != nil {
		t.Fatalf("old row unreadable after migration: %v", err)
	}
	if old.Kind != KindManual || old.MaxContinuations != DefaultMaxContinuations || old.SessionRef != "" {
		t.Errorf("old row defaults: kind=%q max_cont=%d ref=%q", old.Kind, old.MaxContinuations, old.SessionRef)
	}
	// And the migrated DB works end to end.
	if _, err := db.ClaimTask("a1"); err != nil {
		t.Fatalf("claim on migrated db: %v", err)
	}
	var ver string
	_ = db.conn.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&ver)
	if ver != "3" {
		t.Errorf("schema_version = %s, want 3", ver)
	}
	// Re-opening (a second gateway) must not trip on the columns already existing.
	db2, err := Open(path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	db2.Close()
}

// The production failure: FinishRun begins a read-then-write transaction while
// the trace recorder is inserting spans into the same file from another
// connection. With a deferred BEGIN the upgrade to a write lock fails at once
// with SQLITE_BUSY (stale snapshot), ignoring busy_timeout, and the run is
// never closed. With BEGIN IMMEDIATE it waits its turn. This test hammers
// exactly that interleaving; it fails without _txlock=immediate in coord.Open.
func TestFinishRunSurvivesConcurrentSpanWrites(t *testing.T) {
	db := testDB(t)
	const tasks = 40

	var ids []string
	for i := 0; i < tasks; i++ {
		tk := newTask(t, db)
		ids = append(ids, tk.ID)
	}

	// A writer that never stops: what the trace recorder looks like.
	stop := make(chan struct{})
	var writerErrs int64
	var wg sync.WaitGroup
	for w := 0; w < 3; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				id := fmt.Sprintf("run-%d-%d", w, i)
				if err := db.InsertRun(&Run{ID: id, TraceID: id, DottedOrder: id, AgentID: "a1",
					Name: "span", RunType: RunTypeTool, Status: StatusPending, StartedAt: time.Now()}); err != nil {
					atomic.AddInt64(&writerErrs, 1)
				}
				_ = db.EndRun(id, time.Now(), "", "", 0, 0)
			}
		}(w)
	}

	var busy int64
	var finishWG sync.WaitGroup
	for _, id := range ids {
		finishWG.Add(1)
		go func(taskID string) {
			defer finishWG.Done()
			tk, err := db.ClaimTask("a1")
			if err != nil {
				atomic.AddInt64(&busy, 1)
				return
			}
			run, err := db.StartRun(tk.ID, "a1", tk.Attempts, "")
			if err != nil {
				atomic.AddInt64(&busy, 1)
				return
			}
			if _, err := db.FinishRun(run.ID, RunOutcome{Liveness: RunCompleted, Result: "ok"}); err != nil {
				t.Logf("FinishRun: %v", err)
				atomic.AddInt64(&busy, 1)
			}
		}(id)
	}
	finishWG.Wait()
	close(stop)
	wg.Wait()

	if busy != 0 {
		t.Fatalf("%d of %d run closes failed under concurrent span writes — a run left 'running' forever", busy, tasks)
	}
	var open int
	_ = db.conn.QueryRow(`SELECT count(*) FROM task_runs WHERE liveness = ?`, RunRunning).Scan(&open)
	if open != 0 {
		t.Fatalf("%d runs still 'running' after every FinishRun returned", open)
	}
}

// A run left 'running' after the task moved on — a crash mid-run, or a close
// that failed — is closed as lost on the next sweep. A run whose claim is
// genuinely still live is left alone.
func TestReapOrphanRuns(t *testing.T) {
	db := testDB(t)

	// (a) task finished by the agent's own `task done`, run never closed
	t1 := newTask(t, db)
	c1, _ := db.ClaimTask("a1")
	r1, _ := db.StartRun(t1.ID, "a1", c1.Attempts, "")
	_ = db.FinishTask(t1.ID, "a1", TaskCompleted, "done", c1.Attempts)

	// (b) live run: task working, same holder, lease in the future
	t2 := newTask(t, db)
	c2, _ := db.ClaimTask("a1")
	r2, _ := db.StartRun(t2.ID, "a1", c2.Attempts, "")

	// (c) the holder died; lease lapsed; someone else reclaimed (attempt 2)
	t3 := newTask(t, db)
	c3, _ := db.ClaimTask("a1")
	r3, _ := db.StartRun(t3.ID, "a1", c3.Attempts, "")
	_, _ = db.conn.Exec(`UPDATE tasks SET lease_until = ? WHERE id = ?`, ts(time.Now().Add(-time.Minute)), t3.ID)
	if _, err := db.ClaimTask("a2"); err != nil {
		t.Fatal(err)
	}

	// Young runs are never reaped, however their task looks: a peer's sweep
	// cannot tell a live run whose agent just typed `task done` from a dead one.
	if young, err := db.ReapOrphanRuns(time.Hour); err != nil {
		t.Fatal(err)
	} else if len(young) != 0 {
		t.Fatalf("reaped %v though every run is seconds old — this is the race that closed a live run as lost", young)
	}

	// Past the age bound, the orphans go.
	ids, err := db.ReapOrphanRuns(0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, id := range ids {
		got[id] = true
	}
	if !got[r1.ID] || !got[r3.ID] || got[r2.ID] {
		t.Fatalf("reaped %v; want r1 %s and r3 %s, not the live r2 %s", ids, r1.ID, r3.ID, r2.ID)
	}
	runs, _ := db.TaskRuns(t1.ID)
	if runs[0].Liveness != RunLost || runs[0].EndedAt.IsZero() || runs[0].Note == "" {
		t.Errorf("orphan should be lost with an end time and a note, got %+v", runs[0])
	}
	live, _ := db.TaskRuns(t2.ID)
	if live[0].Liveness != RunRunning {
		t.Errorf("live run was reaped: %+v", live[0])
	}
}

// A run the sweep marked lost may still report in — the CLI was just slow to
// exit. The runner's report is the fact and replaces the guess.
func TestFinishRunOverwritesALostMarker(t *testing.T) {
	db := testDB(t)
	tk := newTask(t, db)
	c, _ := db.ClaimTask("a1")
	run, _ := db.StartRun(tk.ID, "a1", c.Attempts, "")
	// The agent finished the task itself; a peer's sweep then closed the run.
	_ = db.FinishTask(tk.ID, "a1", TaskCompleted, "done", c.Attempts)
	if ids, _ := db.ReapOrphanRuns(0); len(ids) != 1 {
		t.Fatalf("setup: expected the run to be reaped, got %v", ids)
	}

	_, err := db.FinishRun(run.ID, RunOutcome{Liveness: RunCompleted, Result: "done"})
	if !errors.Is(err, ErrTaskFinished) {
		t.Fatalf("expected ErrTaskFinished (task already completed by the agent), got %v", err)
	}
	runs, _ := db.TaskRuns(tk.ID)
	if runs[0].Liveness != RunCompleted {
		t.Errorf("run liveness = %s, want completed — the runner's report must replace the lost guess", runs[0].Liveness)
	}
	// But a run that genuinely ended cannot be reopened.
	if _, err := db.FinishRun(run.ID, RunOutcome{Liveness: RunFailed}); err == nil {
		t.Error("a completed run must not be re-closed")
	}
}
