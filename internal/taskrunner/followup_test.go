package taskrunner

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ngocp/goterm-control/internal/coord"
)

// A run's conversation lands in task_log, and a line the owner types while a
// task is open goes into its next run's prompt — once.
func TestRunLogsItsSessionAndReadsTheOwnersFollowUps(t *testing.T) {
	db := testDB(t)
	created, _ := db.CreateTask(coord.NewTask{CreatedBy: "a1", AssignedTo: "a2", Title: "scout"})
	if _, err := db.AddFollowUp(created.ID, "owner", "chỉ lấy video Nhật"); err != nil {
		t.Fatal(err)
	}
	llm := &stubLLM{reply: "Đã lọc video Nhật."}
	r := newRunner(db, llm)
	if !r.claimAndRun(context.Background()) {
		t.Fatal("no work")
	}
	if p := llm.seen(); len(p) != 1 || !strings.Contains(p[0], "chỉ lấy video Nhật") {
		t.Fatalf("the follow-up did not reach the run's prompt: %v", p)
	}
	fs, _ := db.FollowUps(created.ID)
	if len(fs) != 1 || fs[0].State != coord.FollowUpDelivered {
		t.Fatalf("follow-up: %+v", fs)
	}
	entries, _ := db.TaskLog(created.ID, 0, 0)
	kinds := []string{}
	for _, e := range entries {
		kinds = append(kinds, e.Kind+":"+e.Label)
	}
	got := strings.Join(kinds, ",")
	for _, want := range []string{"user:", "status:start", "prompt:", "text:", "status:end:completed"} {
		if !strings.Contains(got, want) {
			t.Errorf("log is missing %q: %s", want, got)
		}
	}
}

// On a finished task, the agent that ran it answers in the same CLI session
// and the task stays finished.
func TestFollowUpOnFinishedTaskResumesItsSession(t *testing.T) {
	db := testDB(t)
	created, _ := db.CreateTask(coord.NewTask{CreatedBy: "a1", AssignedTo: "a2", Title: "scout"})
	llm := &stubLLM{reply: "3 ý."}
	r := newRunner(db, llm)
	if !r.claimAndRun(context.Background()) {
		t.Fatal("no work")
	}
	if _, err := db.AddFollowUp(created.ID, "owner", "ý 2 rõ hơn?"); err != nil {
		t.Fatal(err)
	}
	llm.mu.Lock()
	llm.reply = "Ý 2: Mướp chê bánh ngọt vì gen Tas1r2."
	llm.mu.Unlock()

	if !r.claimFollowUpAndStart(context.Background()) {
		t.Fatal("the follow-up was not picked up")
	}
	waitRuns(t, r)

	sessions := llm.seenSessions()
	if len(sessions) != 2 || sessions[1] != "stub-session" {
		t.Fatalf("follow-up did not resume the task's session: %v", sessions)
	}
	if p := llm.seen()[1]; !strings.Contains(p, "ý 2 rõ hơn?") || !strings.Contains(p, "follow-up turn") {
		t.Fatalf("prompt: %s", p)
	}
	task, _ := db.GetTask(created.ID)
	if task.State != coord.TaskCompleted || task.Result != "3 ý." {
		t.Fatalf("a follow-up moved the task: %s %q", task.State, task.Result)
	}
	fs, _ := db.FollowUps(created.ID)
	if fs[0].State != coord.FollowUpAnswered {
		t.Fatalf("follow-up state %s", fs[0].State)
	}
	entries, _ := db.TaskLog(created.ID, 0, 0)
	last := entries[len(entries)-1]
	if last.Kind != coord.LogStatus || last.Label != "end:answered" {
		t.Fatalf("last entry %+v", last)
	}
	var answered bool
	for _, e := range entries {
		if e.Kind == coord.LogText && strings.Contains(e.Content, "Tas1r2") && e.RunID == fs[0].ID {
			answered = true
		}
	}
	if !answered {
		t.Fatal("the answer is not in the session log")
	}
}

// waitRuns waits for goroutine-started runs (claim*AndStart) to finish.
func waitRuns(t *testing.T, r *Runner) {
	t.Helper()
	done := make(chan struct{})
	go func() { r.runs.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("follow-up turn did not finish")
	}
}
