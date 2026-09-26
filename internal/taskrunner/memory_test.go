package taskrunner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ngocp/goterm-control/internal/coord"
	"github.com/ngocp/goterm-control/internal/memory"
)

func memoryFixture(t *testing.T) *memory.Manager {
	t.Helper()
	dir := t.TempDir()
	m := memory.NewManager(memory.Config{
		Enabled: true, Dir: dir, MaxFileChars: 20000, MaxTotalChars: 60000,
	})
	if err := m.Bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "MEMORY.md"),
		[]byte("# MEMORY.md\n\n- Chủ dùng tiếng Việt.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	return m
}

// The whole point of the change: a task run is the lane that does the heaviest
// work and it was the one lane that could not see what the agent knows.
func TestATaskRunIsGivenTheAgentsMemory(t *testing.T) {
	db := testDB(t)
	llm := &stubLLM{reply: "done"}
	r := New(db, llm, nil, Config{
		AgentID: "a2", Model: "m", Interval: 10 * time.Millisecond,
		Timeout: 5 * time.Second, Memory: memoryFixture(t),
	})
	if _, err := db.CreateTask(coord.NewTask{
		CreatedBy: "a1", AssignedTo: "a2", Title: "đọc log", Body: "tìm lỗi auth",
	}); err != nil {
		t.Fatal(err)
	}

	if !r.claimAndRun(context.Background()) {
		t.Fatal("no task was claimed")
	}
	llm.mu.Lock()
	defer llm.mu.Unlock()
	if len(llm.memories) != 1 {
		t.Fatalf("%d calls", len(llm.memories))
	}
	if !strings.Contains(llm.memories[0], "Chủ dùng tiếng Việt") {
		t.Fatalf("the run never saw MEMORY.md:\n%q", llm.memories[0])
	}
	// The policy header is the half that lets the agent WRITE: it names the
	// daily-note path. Without it an agent can read its memory and has no idea
	// it is allowed to add to it — which is why agent 3 had zero notes.
	if !strings.Contains(llm.memories[0], "memory") {
		t.Error("the run was given no note path, so it can read memory and never add to it")
	}
}

// Same rule the chat lane has always had. A resumed session already carries
// this, and injecting it again spends context on what the CLI already holds.
func TestAResumedTaskRunIsNotGivenItAgain(t *testing.T) {
	db := testDB(t)
	llm := &stubLLM{reply: "done"}
	r := New(db, llm, nil, Config{
		AgentID: "a2", Model: "m", Interval: 10 * time.Millisecond,
		Timeout: 5 * time.Second, Memory: memoryFixture(t),
	})
	task, err := db.CreateTask(coord.NewTask{
		CreatedBy: "a1", AssignedTo: "a2", Title: "việc dài", Body: "nhiều run",
	})
	if err != nil {
		t.Fatal(err)
	}
	// A session this agent's own backend already owns — what a task looks like
	// on its second run.
	claimed, err := db.ClaimTask("a2")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionRef(task.ID, "a2", claimed.Attempts, coord.SessionRef{
		Provider: llm.Name(), SessionID: "stub-session",
	}); err != nil {
		t.Fatal(err)
	}
	// Put it back so the runner claims it for itself.
	if _, err := db.Conn().Exec(
		`UPDATE tasks SET state = 'submitted', lease_until = '2000-01-01T00:00:00.000000000Z' WHERE id = ?`,
		task.ID); err != nil {
		t.Fatal(err)
	}

	if !r.claimAndRun(context.Background()) {
		t.Fatal("no task was claimed")
	}
	llm.mu.Lock()
	defer llm.mu.Unlock()
	if llm.memories[0] != "" {
		t.Fatalf("a resumed run was handed the memory block again (%d chars);\n"+
			"the CLI already has it, so this is context spent on nothing", len(llm.memories[0]))
	}
}

// Memory off is a supported configuration, and so is a runner built before one
// exists. Neither may panic on the hottest path in the system.
func TestATaskRunWithoutMemoryStillRuns(t *testing.T) {
	db := testDB(t)
	llm := &stubLLM{reply: "done"}
	r := newRunner(db, llm) // Config.Memory is nil
	if _, err := db.CreateTask(coord.NewTask{
		CreatedBy: "a1", AssignedTo: "a2", Title: "x", Body: "y",
	}); err != nil {
		t.Fatal(err)
	}
	if !r.claimAndRun(context.Background()) {
		t.Fatal("no task was claimed")
	}
	llm.mu.Lock()
	defer llm.mu.Unlock()
	if llm.memories[0] != "" {
		t.Errorf("got %q from a nil manager", llm.memories[0])
	}
}
