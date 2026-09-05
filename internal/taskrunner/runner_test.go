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
	mu      sync.Mutex
	prompts []string
	reply   string
	err     error
	delay   time.Duration
}

func (s *stubLLM) Name() string { return "stub" }

func (s *stubLLM) SendMessage(ctx context.Context, sess *session.Session, model,
	userText, memory string, cb chat.StreamCallbacks) error {
	s.mu.Lock()
	s.prompts = append(s.prompts, userText)
	reply, err, delay := s.reply, s.err, s.delay
	s.mu.Unlock()

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
	sess.SetSessionID("stub-session")
	sess.AddTokens(50, 7)
	return err
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

func TestRunnerRecordsFailure(t *testing.T) {
	db := testDB(t)
	created, _ := db.CreateTask(coord.NewTask{CreatedBy: "a1", Title: "Break"})

	llm := &stubLLM{reply: "partial work", err: errors.New("codex error: 400")}
	newRunner(db, llm).claimAndRun(context.Background())

	task, _ := db.GetTask(created.ID)
	if task.State != coord.TaskFailed {
		t.Errorf("state = %q, want failed", task.State)
	}
	if !strings.Contains(task.Result, "codex error: 400") {
		t.Errorf("result must carry the error, got %q", task.Result)
	}
	if !strings.Contains(task.Result, "partial work") {
		t.Errorf("partial output must be kept, got %q", task.Result)
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
