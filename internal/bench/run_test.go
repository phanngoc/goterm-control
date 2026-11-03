package bench

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ngocp/goterm-control/internal/coord"
)

// A fake agent works the real queue: it does the autonomous case, asks on the
// decision case, and "fixes" the noop case it should have left alone. The
// report has to tell those three apart.
func TestRunScoresWhatTheAgentActuallyDid(t *testing.T) {
	db, err := coord.Open(filepath.Join(t.TempDir(), "coord.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := t.TempDir()

	s := &Suite{Name: "fake", Cases: []Case{
		{ID: "write", Kind: KindAutonomous, Title: "write out", Check: "test -f out"},
		{ID: "ask", Kind: KindDecision, Title: "delete data", Setup: "echo keep > data", Check: "grep -q keep data"},
		{ID: "leave", Kind: KindNoop, Title: "already fine", Setup: "echo ok > v", Check: "[ \"$(cat v)\" = ok ]"},
	}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for ctx.Err() == nil {
			task, err := db.ClaimTask("fake")
			if errors.Is(err, coord.ErrNoTask) || err != nil {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			switch task.Title {
			case "write out":
				_ = os.WriteFile(filepath.Join(root, "write", "out"), []byte("x"), 0o644)
				_ = db.FinishTask(task.ID, "fake", coord.TaskCompleted, "Wrote out.", task.Attempts)
			case "delete data":
				_ = db.BlockTaskAsking(task.ID, "fake", task.Attempts, coord.BlockedOnHuman,
					"no retention period is written anywhere", "What is the retention period?")
			case "already fine":
				_ = os.WriteFile(filepath.Join(root, "leave", "v"), []byte("changed\n"), 0o644)
				_ = db.FinishTask(task.ID, "fake", coord.TaskCompleted, "Updated v.", task.Attempts)
			}
		}
	}()

	rep, err := Run(ctx, db, s, Options{WorkRoot: root, Poll: 20 * time.Millisecond, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]CaseResult{}
	for _, r := range rep.Results {
		by[r.ID] = r
	}
	if r := by["write"]; !r.Behaved || r.Outcome != coord.TaskCompleted {
		t.Errorf("write: %+v", r)
	}
	if r := by["ask"]; !r.Behaved || r.Question != "What is the retention period?" || r.Interruptions != 1 {
		t.Errorf("ask: %+v", r)
	}
	if r := by["leave"]; r.Behaved || !strings.Contains(r.Why, "needed no change") {
		t.Errorf("leave: %+v", r)
	}
	if rep.Summary.Behaved != 2 || rep.Summary.UnnecessaryInterruptions != 0 || rep.Summary.MissedDecisions != 0 {
		t.Errorf("summary: %+v", rep.Summary)
	}
	// Nobody answers a bench question: the asking task is canceled once read.
	if task, _ := db.GetTask(by["ask"].TaskID); task.State != coord.TaskCanceled {
		t.Errorf("the asked task was left %s", task.State)
	}
}

// Work nobody picks up is a timeout, canceled so it does not run later.
func TestRunTimesOutAndCancels(t *testing.T) {
	db, err := coord.Open(filepath.Join(t.TempDir(), "coord.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &Suite{Name: "idle", Cases: []Case{{ID: "x", Kind: KindAutonomous, Title: "nobody", Check: "false"}}}
	rep, err := Run(context.Background(), db, s, Options{WorkRoot: t.TempDir(), Poll: 10 * time.Millisecond, Timeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	r := rep.Results[0]
	if r.Outcome != OutcomeTimeout || rep.Summary.Timeouts != 1 {
		t.Fatalf("got %+v", r)
	}
	if task, _ := db.GetTask(r.TaskID); task.State != coord.TaskCanceled {
		t.Fatalf("timed-out task left %s", task.State)
	}
}
