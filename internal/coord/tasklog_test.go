package coord

import (
	"errors"
	"strings"
	"testing"
)

func TestTaskLogReadsAfterCursor(t *testing.T) {
	db := testDB(t)
	task, err := db.CreateTask(NewTask{CreatedBy: "owner", Title: "scout"})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{LogPrompt, LogText, LogTool} {
		if err := db.AppendTaskLog(TaskLogEntry{TaskID: task.ID, Kind: k, Content: k}); err != nil {
			t.Fatal(err)
		}
	}
	all, err := db.TaskLog(task.ID, 0, 0)
	if err != nil || len(all) != 3 {
		t.Fatalf("got %d entries (%v), want 3", len(all), err)
	}
	rest, _ := db.TaskLog(task.ID, all[0].ID, 0)
	if len(rest) != 2 || rest[0].Kind != LogText {
		t.Fatalf("after the first entry: %+v", rest)
	}
	// A huge tool output is cut, not refused.
	if err := db.AppendTaskLog(TaskLogEntry{TaskID: task.ID, Kind: LogToolResult, Content: strings.Repeat("ờ", 20000)}); err != nil {
		t.Fatal(err)
	}
	last, _ := db.TaskLog(task.ID, rest[1].ID, 0)
	if len(last) != 1 || len(last[0].Content) > maxLogContent+64 || !strings.Contains(last[0].Content, "bytes more") {
		t.Fatalf("not clipped: %d bytes", len(last[0].Content))
	}
}

// A follow-up on an open task waits for its next run, and that run takes it
// exactly once.
func TestFollowUpOnOpenTaskGoesIntoTheNextRun(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw")
	task, _ := db.CreateTask(NewTask{CreatedBy: "owner", AssignedTo: "bomclaw", Title: "scout"})

	f, err := db.AddFollowUp(task.ID, "owner", "  sao đây  ", false)
	if err != nil {
		t.Fatal(err)
	}
	if f.Body != "sao đây" || f.State != FollowUpQueued {
		t.Fatalf("follow-up: %+v", f)
	}
	// It is in the session at once, before anyone picked it up.
	log, _ := db.TaskLog(task.ID, 0, 0)
	if len(log) != 1 || log[0].Kind != LogUser {
		t.Fatalf("log: %+v", log)
	}
	// Not a finished task, so no follow-up turn claims it.
	if _, _, err := db.ClaimFollowUp("bomclaw", nil); !errors.Is(err, ErrNoTask) {
		t.Fatalf("an open task's follow-up was claimed for a turn of its own: %v", err)
	}
	got, err := db.TakeFollowUps(task.ID, "bomclaw")
	if err != nil || len(got) != 1 {
		t.Fatalf("take: %v %+v", err, got)
	}
	again, _ := db.TakeFollowUps(task.ID, "bomclaw")
	if len(again) != 0 {
		t.Fatalf("delivered twice: %+v", again)
	}
}

// On a finished task, the agent that ran it answers — one turn at a time.
func TestFollowUpOnFinishedTaskIsClaimedByItsAgent(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw", "bomclaw2")
	task, _ := db.CreateTask(NewTask{CreatedBy: "owner", AssignedTo: "bomclaw", Title: "scout"})
	claimed, err := db.ClaimTask("bomclaw")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.FinishTask(task.ID, "bomclaw", TaskCompleted, "3 ý", claimed.Attempts); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddFollowUp(task.ID, "owner", "ý 2 rõ hơn?", false); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddFollowUp(task.ID, "owner", "và ý 3?", false); err != nil {
		t.Fatal(err)
	}

	if _, _, err := db.ClaimFollowUp("bomclaw2", nil); !errors.Is(err, ErrNoTask) {
		t.Fatalf("another agent claimed it: %v", err)
	}
	busy := func(string) bool { return true }
	if _, _, err := db.ClaimFollowUp("bomclaw", busy); !errors.Is(err, ErrNoTask) {
		t.Fatalf("claimed while the session was busy: %v", err)
	}
	f, tk, err := db.ClaimFollowUp("bomclaw", nil)
	if err != nil || f.Body != "ý 2 rõ hơn?" || tk.ID != task.ID {
		t.Fatalf("claim: %v %+v", err, f)
	}
	// The second waits while the first is being answered.
	if _, _, err := db.ClaimFollowUp("bomclaw", nil); !errors.Is(err, ErrNoTask) {
		t.Fatalf("two turns at once on one session: %v", err)
	}
	if err := db.FinishFollowUp(f.ID, FollowUpAnswered); err != nil {
		t.Fatal(err)
	}
	f2, _, err := db.ClaimFollowUp("bomclaw", nil)
	if err != nil || f2.Body != "và ý 3?" {
		t.Fatalf("second: %v %+v", err, f2)
	}
}

func TestFollowUpRefusedOnTaskNobodyRan(t *testing.T) {
	db := testDB(t)
	task, _ := db.CreateTask(NewTask{CreatedBy: "owner", Title: "x"})
	if err := db.CancelTask(task.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddFollowUp(task.ID, "owner", "hello", false); !errors.Is(err, ErrFollowUpRefused) {
		t.Fatalf("want refused, got %v", err)
	}
	if _, err := db.AddFollowUp(task.ID, "owner", "   ", false); err == nil {
		t.Fatal("an empty follow-up was accepted")
	}
}

// A writer that starts before its task exists keeps what it saw and writes it,
// in order, the moment the task is named.
func TestTaskLogWriterBindsLate(t *testing.T) {
	db := testDB(t)
	task, _ := db.CreateTask(NewTask{CreatedBy: "owner", Title: "x"})
	w := NewTaskLogWriter(db, "", "", "bomclaw")
	w.User("owner", "làm đi")
	w.Tool("Bash(ls)", "")
	if got, _ := db.TaskLog(task.ID, 0, 0); len(got) != 0 {
		t.Fatalf("wrote before bind: %+v", got)
	}
	w.Bind(task.ID, "run1")
	w.Text("xong")
	w.Close()
	got, _ := db.TaskLog(task.ID, 0, 0)
	if len(got) != 3 || got[0].Kind != LogUser || got[0].AgentID != "owner" || got[1].Kind != LogTool || got[2].Kind != LogText || got[1].RunID != "run1" {
		t.Fatalf("log: %+v", got)
	}
}
