package taskrunner

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ngocp/goterm-control/internal/chat"
	"github.com/ngocp/goterm-control/internal/coord"
	"github.com/ngocp/goterm-control/internal/session"
)

// stubLLM records what it was asked and replies with a canned answer.
type stubLLM struct {
	mu       sync.Mutex
	prompts  []string
	sessions []string // sess.GetSessionID() at call time — "" means a fresh session
	reply    string
	err      error
	delay    time.Duration
	// hook runs mid-call, standing in for the agent typing `bomclaw task …`
	// or the CLI calling tools.
	hook func(ctx context.Context, sess *session.Session, cb chat.StreamCallbacks)
}

func (s *stubLLM) Name() string { return "stub" }

func (s *stubLLM) SendMessage(ctx context.Context, sess *session.Session, model,
	userText, memory string, cb chat.StreamCallbacks) error {
	s.mu.Lock()
	s.prompts = append(s.prompts, userText)
	s.sessions = append(s.sessions, sess.GetSessionID())
	reply, err, delay, hook := s.reply, s.err, s.delay, s.hook
	s.mu.Unlock()

	// The real CLIs announce the session id in their first stream event, so
	// even a run that later times out knows which session it was in.
	if sess.GetSessionID() == "" {
		sess.SetSessionID("stub-session")
	}
	if hook != nil {
		hook(ctx, sess, cb)
	}
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if cb.OnText != nil && reply != "" {
		cb.OnText(reply)
	}
	sess.AddTokens(50, 7)
	return err
}

func (s *stubLLM) seenSessions() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.sessions...)
}

func (s *stubLLM) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.prompts...)
}

func testDB(t *testing.T) *coord.DB {
	t.Helper()
	db, err := coord.Open(filepath.Join(t.TempDir(), "coord.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func newRunner(db *coord.DB, llm chat.Client) *Runner {
	return New(db, llm, nil, Config{
		AgentID:  "a2",
		Model:    "m",
		Interval: 10 * time.Millisecond,
		Timeout:  5 * time.Second,
	})
}

func TestRunnerClaimsAndCompletes(t *testing.T) {
	db := testDB(t)
	created, err := db.CreateTask(coord.NewTask{
		CreatedBy: "a1", AssignedTo: "a2",
		Title: "Check the log", Body: "Look for auth errors",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	llm := &stubLLM{reply: "No auth errors in the last hour."}
	r := newRunner(db, llm)
	if !r.claimAndRun(context.Background()) {
		t.Fatal("runner found no work for a task addressed to it")
	}

	task, err := db.GetTask(created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if task.State != coord.TaskCompleted {
		t.Errorf("state = %q, want completed", task.State)
	}
	if task.Result != "No auth errors in the last hour." {
		t.Errorf("result = %q — the model's reply is the deliverable", task.Result)
	}
	if task.ClaimedBy != "a2" {
		t.Errorf("claimed_by = %q", task.ClaimedBy)
	}

	prompts := llm.seen()
	if len(prompts) != 1 {
		t.Fatalf("model called %d times, want 1", len(prompts))
	}
	for _, want := range []string{"Check the log", "Look for auth errors", created.ID, "a1"} {
		if !strings.Contains(prompts[0], want) {
			t.Errorf("prompt is missing %q:\n%s", want, prompts[0])
		}
	}
	// The agent must know nobody is waiting to answer questions.
	if !strings.Contains(prompts[0], "do not ask follow-up questions") {
		t.Error("prompt does not tell the agent there is nobody to answer questions")
	}
}

// A run that breaks is retried; only when every attempt has broken does the
// task fail — and then with the reason, not stuck in `working`.
func TestRunnerRetriesFailuresThenExhausts(t *testing.T) {
	db := testDB(t)
	created, _ := db.CreateTask(coord.NewTask{CreatedBy: "a1", Title: "Break"})

	llm := &stubLLM{reply: "partial work", err: errors.New("codex error: 400")}
	r := newRunner(db, llm)

	r.claimAndRun(context.Background())
	task, _ := db.GetTask(created.ID)
	if task.State != coord.TaskSubmitted || task.Attempts != 1 {
		t.Fatalf("after one broken run: state=%s attempts=%d, want submitted/1 (retry)", task.State, task.Attempts)
	}

	r.claimAndRun(context.Background())
	r.claimAndRun(context.Background())
	task, _ = db.GetTask(created.ID)
	if task.State != coord.TaskFailed || task.FailReason != coord.FailExhausted {
		t.Errorf("after three: state=%s reason=%q, want failed/exhausted", task.State, task.FailReason)
	}
	if !strings.Contains(task.Result, "codex error: 400") {
		t.Errorf("result must carry the error, got %q", task.Result)
	}
	if !strings.Contains(task.Result, "partial work") {
		t.Errorf("partial output must be kept, got %q", task.Result)
	}
	runs, _ := db.TaskRuns(created.ID)
	if len(runs) != 3 || runs[0].Liveness != coord.RunFailed {
		t.Errorf("ledger should show 3 failed runs, got %+v", runs)
	}
	// Nothing left to claim: no more `working` limbo.
	if r.claimAndRun(context.Background()) {
		t.Error("an exhausted task was claimed again")
	}
}

func TestRunnerIgnoresTasksForOtherAgents(t *testing.T) {
	db := testDB(t)
	db.CreateTask(coord.NewTask{CreatedBy: "a1", AssignedTo: "a3", Title: "not yours"})

	llm := &stubLLM{reply: "hi"}
	if newRunner(db, llm).claimAndRun(context.Background()) {
		t.Error("runner claimed a task addressed to another agent")
	}
	if len(llm.seen()) != 0 {
		t.Error("model was called for someone else's task")
	}
}

func TestRunnerDrainsTheQueue(t *testing.T) {
	db := testDB(t)
	for i := 0; i < 3; i++ {
		db.CreateTask(coord.NewTask{CreatedBy: "a1", Title: "job"})
	}

	llm := &stubLLM{reply: "done"}
	r := newRunner(db, llm)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		done, _ := db.ListTasks(coord.TaskFilter{State: coord.TaskCompleted})
		if len(done) == 3 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	r.Wait()

	done, _ := db.ListTasks(coord.TaskFilter{State: coord.TaskCompleted})
	if len(done) != 3 {
		t.Errorf("completed %d of 3 queued tasks", len(done))
	}
}

func TestStaleRunnerDoesNotOverwriteTheNewOwner(t *testing.T) {
	db := testDB(t)
	created, _ := db.CreateTask(coord.NewTask{CreatedBy: "a1", Title: "slow job"})

	// a2 claims and starts working.
	slow := &stubLLM{reply: "a2 answer", delay: 300 * time.Millisecond}
	r := newRunner(db, slow)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		r.claimAndRun(context.Background())
	}()

	// Meanwhile the lease lapses and a3 takes the work and finishes first.
	time.Sleep(50 * time.Millisecond)
	db.Conn().Exec(`UPDATE tasks SET lease_until = ? WHERE id = ?`,
		time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano), created.ID)
	fresh, err := db.ClaimTask("a3")
	if err != nil {
		t.Fatalf("a3 could not reclaim: %v", err)
	}
	if err := db.FinishTask(fresh.ID, "a3", coord.TaskCompleted, "a3 answer", fresh.Attempts); err != nil {
		t.Fatalf("a3 could not finish: %v", err)
	}

	wg.Wait()

	task, _ := db.GetTask(created.ID)
	if task.Result != "a3 answer" {
		t.Errorf("result = %q — the stale runner overwrote the new owner's work", task.Result)
	}
	if task.ClaimedBy != "a3" {
		t.Errorf("claimed_by = %q, want a3", task.ClaimedBy)
	}
}

func TestNilRunnerIsSafe(t *testing.T) {
	// A disabled runner is expressed as nil; every entry point must tolerate it
	// so callers do not have to branch.
	var r *Runner
	r.Poke()
	r.Start(context.Background())
	r.Wait()

	if New(nil, &stubLLM{}, nil, Config{Interval: time.Second}) != nil {
		t.Error("New must return nil without a database")
	}
	if New(testDB(t), &stubLLM{}, nil, Config{Interval: 0}) != nil {
		t.Error("New must return nil when polling is disabled")
	}
}

// The whole point of P0: a run that hits its cap having written progress is a
// continuation. The task returns to the queue pinned to this agent, carrying
// the checkpoint and the CLI session, and the next run resumes that session.
func TestRunThatTimesOutWithProgressContinuesAndResumes(t *testing.T) {
	db := testDB(t)
	created, _ := db.CreateTask(coord.NewTask{CreatedBy: "a1", Title: "Summarise five sources"})

	llm := &stubLLM{
		reply: "working through source 3…",
		delay: 2 * time.Second, // longer than the run cap below
		hook: func(_ context.Context, _ *session.Session, _ chat.StreamCallbacks) {
			// The agent writes progress before time runs out.
			tk, _ := db.GetTask(created.ID)
			_ = db.SetCheckpoint(created.ID, "a2", tk.Attempts, "sources 1-2 done, on 3")
		},
	}
	r := New(db, llm, nil, Config{AgentID: "a2", Model: "m", Interval: 10 * time.Millisecond, Timeout: 150 * time.Millisecond})

	r.claimAndRun(context.Background())

	task, _ := db.GetTask(created.ID)
	if task.State != coord.TaskSubmitted {
		t.Fatalf("state = %s, want submitted (call me back)", task.State)
	}
	if task.Continuations != 1 || task.Attempts != 1 {
		t.Errorf("continuations=%d attempts=%d — progress must not spend an attempt", task.Continuations, task.Attempts)
	}
	if task.AssignedTo != "a2" {
		t.Errorf("assigned_to = %q, want a2 (the session lives in a2's config dir)", task.AssignedTo)
	}
	ref := coord.ParseSessionRef(task.SessionRef)
	if ref.Provider != "stub" || ref.SessionID != "stub-session" {
		t.Errorf("session ref = %+v, want stub/stub-session", ref)
	}
	runs, _ := db.TaskRuns(created.ID)
	if len(runs) != 1 || runs[0].Liveness != coord.RunTimedOut {
		t.Fatalf("ledger = %+v, want one timed_out run", runs)
	}

	// Run 2: quick, done. It must be handed the SAME session and told where run 1 stopped.
	llm.mu.Lock()
	llm.delay, llm.hook, llm.reply = 0, nil, "all five summarised"
	llm.mu.Unlock()
	r.claimAndRun(context.Background())

	task, _ = db.GetTask(created.ID)
	if task.State != coord.TaskCompleted || task.Result != "all five summarised" {
		t.Fatalf("run 2: state=%s result=%q", task.State, task.Result)
	}
	sessions := llm.seenSessions()
	if len(sessions) != 2 || sessions[0] != "" || sessions[1] != "stub-session" {
		t.Errorf("sessions handed to the CLI = %v; run 2 must resume run 1's session", sessions)
	}
	p := llm.seen()[1]
	for _, want := range []string{"continuing a task", "run 2", "sources 1-2 done, on 3", "has been resumed"} {
		if !strings.Contains(p, want) {
			t.Errorf("run 2 prompt missing %q:\n%s", want, p)
		}
	}
}

// Without progress, a timeout is just a spent attempt — but the session is
// still recorded, so the retry can resume it.
func TestRunThatTimesOutSilentlySpendsAnAttempt(t *testing.T) {
	db := testDB(t)
	created, _ := db.CreateTask(coord.NewTask{CreatedBy: "a1", Title: "hang"})
	llm := &stubLLM{delay: time.Second}
	r := New(db, llm, nil, Config{AgentID: "a2", Model: "m", Interval: 10 * time.Millisecond, Timeout: 100 * time.Millisecond})

	r.claimAndRun(context.Background())
	task, _ := db.GetTask(created.ID)
	if task.State != coord.TaskSubmitted || task.Attempts != 1 || task.Continuations != 0 {
		t.Errorf("state=%s attempts=%d continuations=%d, want submitted/1/0", task.State, task.Attempts, task.Continuations)
	}
}

// The agent's own `bomclaw task done` inside the run is the authoritative
// completion, whatever the reply text looks like.
func TestAgentMarkingDoneInsideTheRunCompletesIt(t *testing.T) {
	db := testDB(t)
	created, _ := db.CreateTask(coord.NewTask{CreatedBy: "a1", Title: "quick"})
	llm := &stubLLM{
		reply: "I'll get to that next.", // would otherwise read as plan-ish
		hook: func(_ context.Context, _ *session.Session, _ chat.StreamCallbacks) {
			tk, _ := db.GetTask(created.ID)
			_ = db.FinishTask(created.ID, "a2", coord.TaskCompleted, "deliverable via task done", tk.Attempts)
		},
	}
	newRunner(db, llm).claimAndRun(context.Background())

	task, _ := db.GetTask(created.ID)
	if task.State != coord.TaskCompleted || task.Result != "deliverable via task done" {
		t.Fatalf("state=%s result=%q", task.State, task.Result)
	}
	runs, _ := db.TaskRuns(created.ID)
	if len(runs) != 1 || runs[0].Liveness != coord.RunCompleted {
		t.Errorf("ledger = %+v, want one completed run", runs)
	}
}

// `bomclaw task block` inside the run parks the task; the run closes as blocked.
func TestAgentBlockingInsideTheRun(t *testing.T) {
	db := testDB(t)
	created, _ := db.CreateTask(coord.NewTask{CreatedBy: "a1", Title: "needs a number"})
	llm := &stubLLM{
		reply: "I need the budget figure.",
		hook: func(_ context.Context, _ *session.Session, _ chat.StreamCallbacks) {
			tk, _ := db.GetTask(created.ID)
			_ = db.BlockTask(created.ID, "a2", tk.Attempts, coord.BlockedOnHuman, "budget figure?")
		},
	}
	newRunner(db, llm).claimAndRun(context.Background())

	task, _ := db.GetTask(created.ID)
	if task.State != coord.TaskBlocked || task.BlockedOn != coord.BlockedOnHuman {
		t.Fatalf("state=%s blocked_on=%q", task.State, task.BlockedOn)
	}
	runs, _ := db.TaskRuns(created.ID)
	if len(runs) != 1 || runs[0].Liveness != coord.RunBlocked {
		t.Errorf("ledger = %+v, want one blocked run", runs)
	}
	// A person answers; the task is claimable again.
	if err := db.UnblockTask(created.ID, "human", "it is 500"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ClaimTask("a2"); err != nil {
		t.Errorf("unblocked task not claimable: %v", err)
	}
}

// A reply that is a plan with unfinished TodoWrite items and no progress note
// is not a deliverable; call the agent back.
func TestPlanOnlyReplyIsContinuedNotAccepted(t *testing.T) {
	db := testDB(t)
	created, _ := db.CreateTask(coord.NewTask{CreatedBy: "a1", Title: "do the thing"})
	llm := &stubLLM{
		reply: "Here is my plan: 1) … 2) …",
		hook: func(_ context.Context, _ *session.Session, cb chat.StreamCallbacks) {
			cb.OnToolCall("TodoWrite", `{"todos":[{"content":"a","status":"completed"},{"content":"b","status":"pending"}]}`)
		},
	}
	newRunner(db, llm).claimAndRun(context.Background())

	task, _ := db.GetTask(created.ID)
	if task.State != coord.TaskSubmitted || task.Continuations != 1 {
		t.Fatalf("state=%s continuations=%d, want submitted/1", task.State, task.Continuations)
	}
	runs, _ := db.TaskRuns(created.ID)
	if runs[0].Liveness != coord.RunPlanOnly {
		t.Errorf("liveness = %s, want plan_only", runs[0].Liveness)
	}
}

func TestEmptyReplyIsRecordedAsEmpty(t *testing.T) {
	db := testDB(t)
	created, _ := db.CreateTask(coord.NewTask{CreatedBy: "a1", Title: "say something"})
	newRunner(db, &stubLLM{reply: ""}).claimAndRun(context.Background())
	runs, _ := db.TaskRuns(created.ID)
	if len(runs) != 1 || runs[0].Liveness != coord.RunEmpty {
		t.Errorf("ledger = %+v, want one empty run", runs)
	}
}

// While a run is in flight it is visible — this is what lets the tray hold the
// Mac awake and the dashboard show it. Afterwards it is gone.
func TestLiveRunsAreVisibleWhileRunning(t *testing.T) {
	db := testDB(t)
	created, _ := db.CreateTask(coord.NewTask{CreatedBy: "a1", Title: "visible work"})
	release := make(chan struct{})
	llm := &stubLLM{reply: "ok", hook: func(ctx context.Context, sess *session.Session, cb chat.StreamCallbacks) {
		cb.OnToolCall("Bash", `{"command":"ls"}`)
		<-release
	}}
	r := newRunner(db, llm)

	var events []Event
	var evMu sync.Mutex
	r.SetEventListener(func(e Event) { evMu.Lock(); events = append(events, e); evMu.Unlock() })

	done := make(chan struct{})
	go func() { r.claimAndRun(context.Background()); close(done) }()

	deadline := time.Now().Add(2 * time.Second)
	for len(r.Live()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	live := r.Live()
	if len(live) != 1 {
		t.Fatalf("live runs = %d, want 1", len(live))
	}
	info := live[0].RunInfo()
	if !info.Running || info.CurrentTask != "visible work" || live[0].ID != "task_"+created.ID {
		t.Errorf("live run info = %+v id=%s", info, live[0].ID)
	}
	if info.LastTool != "Bash" || info.ToolCount != 1 {
		t.Errorf("tool activity not reflected: %+v", info)
	}

	close(release)
	<-done
	if len(r.Live()) != 0 {
		t.Error("run still listed as live after it finished")
	}
	deadline = time.Now().Add(time.Second)
	for {
		evMu.Lock()
		n := len(events)
		evMu.Unlock()
		if n == 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	evMu.Lock()
	defer evMu.Unlock()
	if len(events) != 2 || events[0].Phase != "started" || events[1].Phase != "finished" || events[0].TaskID != created.ID {
		t.Errorf("events = %+v, want started then finished for %s", events, created.ID)
	}
}

// The sweep is what turns "stuck in working forever" into a visible failure,
// and frees tasks addressed to an agent that is gone.
func TestSweepReapsAndRelaxes(t *testing.T) {
	db := testDB(t)
	// Exhausted: three claims that never reported back.
	stuck, _ := db.CreateTask(coord.NewTask{CreatedBy: "a1", Title: "stuck"})
	for i := 0; i < 3; i++ {
		db.ClaimTask("a9")
		db.Conn().Exec(`UPDATE tasks SET lease_until = ? WHERE id = ?`,
			time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano), stuck.ID)
	}
	// Addressed to a dead agent.
	db.RegisterAgent(coord.Agent{ID: "dead"})
	db.Conn().Exec(`UPDATE agents SET last_seen_at = ? WHERE id = 'dead'`, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano))
	orphan, _ := db.CreateTask(coord.NewTask{CreatedBy: "a1", AssignedTo: "dead", Title: "orphan"})

	newRunner(db, &stubLLM{}).sweep()

	s1, _ := db.GetTask(stuck.ID)
	if s1.State != coord.TaskFailed || s1.FailReason != coord.FailExhausted {
		t.Errorf("stuck task: state=%s reason=%q", s1.State, s1.FailReason)
	}
	s2, _ := db.GetTask(orphan.ID)
	if s2.AssignedTo != "" {
		t.Errorf("orphan still assigned to %q", s2.AssignedTo)
	}
}

func TestHasPendingTodos(t *testing.T) {
	if !hasPendingTodos(`{"todos":[{"status":"completed"},{"status":"in_progress"}]}`) {
		t.Error("in_progress is pending")
	}
	if hasPendingTodos(`{"todos":[{"status":"completed"}]}`) {
		t.Error("all completed is not pending")
	}
	if hasPendingTodos(`not json`) || hasPendingTodos(`{}`) {
		t.Error("garbage must not read as pending")
	}
}
