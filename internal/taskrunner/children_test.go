package taskrunner

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ngocp/goterm-control/internal/chat"
	"github.com/ngocp/goterm-control/internal/coord"
	"github.com/ngocp/goterm-control/internal/session"
)

// Flow A of the design (§7): the parent fans out, blocks on children, and is
// called back with their results in its prompt once the last one finishes.
func TestParentIsWokenWithChildResults(t *testing.T) {
	db := testDB(t)
	root, _ := db.CreateTask(coord.NewTask{CreatedBy: "human", Title: "Digest 2 sources", AssignedTo: "a2"})

	var woke []coord.WokenParent
	var mu sync.Mutex
	llm := &stubLLM{}
	r := newRunner(db, llm)
	r.SetWakeListener(func(w coord.WokenParent) { mu.Lock(); woke = append(woke, w); mu.Unlock() })

	// Run 1: the agent splits the work and parks the parent.
	llm.hook = func(ctx context.Context, sess *session.Session, cb chat.StreamCallbacks) {
		if _, err := db.CreateSubTask(root.ID, "a2", coord.NewTask{Title: "Source A", Body: "Read source A end to end and summarise what it says about the scanner failure."}); err != nil {
			t.Errorf("sub A: %v", err)
		}
		if _, err := db.CreateSubTask(root.ID, "a2", coord.NewTask{Title: "Source B", Body: "Read source B end to end and summarise what it says about the scanner failure."}); err != nil {
			t.Errorf("sub B: %v", err)
		}
		if err := db.BlockTask(root.ID, "a2", 1, coord.BlockedOnChildren, "waiting on A and B"); err != nil {
			t.Errorf("block: %v", err)
		}
	}
	if !r.claimAndRun(context.Background()) {
		t.Fatal("parent not claimed")
	}
	if p, _ := db.GetTask(root.ID); p.State != coord.TaskBlocked {
		t.Fatalf("parent should be blocked, is %s", p.State)
	}
	if !strings.Contains(llm.seen()[0], "bomclaw task sub --parent "+root.ID) {
		t.Error("prompt should teach the agent how to split work")
	}

	// The children are claimable (oldest first) and complete; the second one's
	// finish wakes the parent through the runner's own hook, no sweep needed.
	llm.hook = nil
	llm.reply = "Source summary."
	for i := 0; i < 2; i++ {
		if !r.claimAndRun(context.Background()) {
			t.Fatalf("child %d not claimed", i+1)
		}
	}
	p, _ := db.GetTask(root.ID)
	if p.State != coord.TaskSubmitted || p.AssignedTo != "a2" {
		t.Fatalf("parent after children: state=%s assigned=%s", p.State, p.AssignedTo)
	}
	mu.Lock()
	n := len(woke)
	mu.Unlock()
	if n != 1 || woke[0].TaskID != root.ID {
		t.Errorf("wake listener: %+v", woke)
	}

	// Run 2 of the parent: resumed, with both results in the prompt.
	llm.reply = "Digest: A and B say the same thing."
	if !r.claimAndRun(context.Background()) {
		t.Fatal("parent not claimed for run 2")
	}
	prompts := llm.seen()
	last := prompts[len(prompts)-1]
	for _, want := range []string{"## Your child tasks", "[completed] Source A", "[completed] Source B", "Source summary.", "Children finished (2)", "waiting on A and B"} {
		if !strings.Contains(last, want) {
			t.Errorf("parent's run-2 prompt missing %q:\n%s", want, last)
		}
	}
	if sessions := llm.seenSessions(); sessions[len(sessions)-1] != "stub-session" {
		t.Errorf("parent's run 2 should resume run 1's session, got %q", sessions[len(sessions)-1])
	}
	final, _ := db.GetTask(root.ID)
	if final.State != coord.TaskCompleted || final.Result != "Digest: A and B say the same thing." {
		t.Errorf("final: %s %q", final.State, final.Result)
	}
}

// A message a peer attaches to a task is in the task's next prompt and is then
// marked read; unrelated mail stays in the inbox for the chat.
func TestTaskMailIsInThePromptAndMarkedRead(t *testing.T) {
	db := testDB(t)
	task, _ := db.CreateTask(coord.NewTask{CreatedBy: "a1", Title: "Review the PR", AssignedTo: "a2"})
	db.SendMessage("a1", "a2", task.ID, "Skip the Windows job, it is known-flaky.")
	db.SendMessage("a1", "a2", "", "Unrelated: lunch?")

	llm := &stubLLM{reply: "Reviewed."}
	r := newRunner(db, llm)
	r.claimAndRun(context.Background())

	prompt := llm.seen()[0]
	if !strings.Contains(prompt, "## Messages about this task") || !strings.Contains(prompt, "known-flaky") {
		t.Errorf("task mail missing from prompt:\n%s", prompt)
	}
	if strings.Contains(prompt, "lunch") {
		t.Error("general mail must not be pulled into a task")
	}
	unread, _ := db.Inbox("a2", true, 10)
	if len(unread) != 1 || !strings.Contains(unread[0].Body, "lunch") {
		t.Errorf("only the unrelated message should stay unread: %+v", unread)
	}
}

// P3: with concurrency 2, two queued tasks run at the same time; with 1 they
// run one after the other.
func TestConcurrencyRunsTasksSideBySide(t *testing.T) {
	for _, tc := range []struct {
		concurrency int
		wantPeak    int32
	}{{1, 1}, {2, 2}} {
		db := testDB(t)
		for i := 0; i < 3; i++ {
			db.CreateTask(coord.NewTask{CreatedBy: "a1", Title: "job", AssignedTo: "a2"})
		}
		var inFlight, peak int32
		llm := &stubLLM{reply: "done", hook: func(ctx context.Context, _ *session.Session, _ chat.StreamCallbacks) {
			n := atomic.AddInt32(&inFlight, 1)
			for {
				old := atomic.LoadInt32(&peak)
				if n <= old || atomic.CompareAndSwapInt32(&peak, old, n) {
					break
				}
			}
			time.Sleep(60 * time.Millisecond)
			atomic.AddInt32(&inFlight, -1)
		}}
		r := New(db, llm, nil, Config{AgentID: "a2", Model: "m", Interval: 10 * time.Millisecond, Timeout: 5 * time.Second, Concurrency: tc.concurrency})
		ctx, cancel := context.WithCancel(context.Background())
		r.Start(ctx)
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			done, _ := db.ListTasks(coord.TaskFilter{State: coord.TaskCompleted})
			if len(done) == 3 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
		r.Wait()
		done, _ := db.ListTasks(coord.TaskFilter{State: coord.TaskCompleted})
		if len(done) != 3 {
			t.Fatalf("concurrency %d: %d of 3 tasks completed", tc.concurrency, len(done))
		}
		if got := atomic.LoadInt32(&peak); got != tc.wantPeak {
			t.Errorf("concurrency %d: peak in-flight %d, want %d", tc.concurrency, got, tc.wantPeak)
		}
		if live := r.Live(); len(live) != 0 {
			t.Errorf("after Wait nothing should be live, got %d", len(live))
		}
	}
}
