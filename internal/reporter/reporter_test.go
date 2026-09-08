package reporter

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ngocp/goterm-control/internal/coord"
)

func openDB(t *testing.T) *coord.DB {
	t.Helper()
	db, err := coord.Open(filepath.Join(t.TempDir(), "coord.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// delegate creates work "a2" handed to "a1", then finishes it as a1.
func delegate(t *testing.T, db *coord.DB, title, state, result string) *coord.Task {
	t.Helper()
	task, err := db.CreateTask(coord.NewTask{CreatedBy: "a2", AssignedTo: "a1", Title: title})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	c, err := db.ClaimTask("a1")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := db.FinishTask(c.ID, "a1", state, result, c.Attempts); err != nil {
		t.Fatalf("finish: %v", err)
	}
	return task
}

// collector stands in for Bot.Notify.
type collector struct {
	mu    sync.Mutex
	lines []string
}

func (c *collector) add(s string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, s)
}

func (c *collector) all() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.lines...)
}

func TestTickDeliversAFinishedHandOff(t *testing.T) {
	db := openDB(t)
	delegate(t, db, "crawl listings", coord.TaskCompleted, "62 jobs found")

	var got collector
	r := New(db, Config{AgentID: "a2"})
	r.SetNotify(got.add)
	r.Tick()

	lines := got.all()
	if len(lines) != 1 {
		t.Fatalf("want one delivery, got %d: %v", len(lines), lines)
	}
	for _, want := range []string{"crawl listings", "62 jobs found", "a1", "completed"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("delivery missing %q:\n%s", want, lines[0])
		}
	}
}

// The marker is what stops a result being announced on every tick forever.
func TestASecondTickSaysNothingNew(t *testing.T) {
	db := openDB(t)
	delegate(t, db, "crawl listings", coord.TaskCompleted, "62 jobs")

	var got collector
	r := New(db, Config{AgentID: "a2"})
	r.SetNotify(got.add)
	r.Tick()
	r.Tick()
	r.Tick()

	if n := len(got.all()); n != 1 {
		t.Errorf("reported %d times; the marker should make it exactly once", n)
	}
}

// Two gateways open the same file and run the same query in the same tick. The
// compare-and-set is the only thing standing between that and the user being
// told twice — so it is worth testing with real concurrency rather than by
// inspection.
func TestTwoGatewaysDeliverOnce(t *testing.T) {
	db := openDB(t)
	const n = 12
	for i := range n {
		delegate(t, db, "job "+string(rune('a'+i)), coord.TaskCompleted, "ok")
	}

	var got collector
	a := New(db, Config{AgentID: "a2"})
	a.SetNotify(got.add)
	b := New(db, Config{AgentID: "a2"})
	b.SetNotify(got.add)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); a.Tick() }()
	go func() { defer wg.Done(); b.Tick() }()
	wg.Wait()

	if len(got.all()) != n {
		t.Errorf("delivered %d lines for %d tasks — a duplicate or a dropped report", len(got.all()), n)
	}
}

// A gateway with no Telegram must not consume the report: nothing was
// delivered, so the marker has to stay unset for a gateway that can deliver.
func TestWithoutADeliveryPathTheReportIsNotConsumed(t *testing.T) {
	db := openDB(t)
	task := delegate(t, db, "crawl listings", coord.TaskCompleted, "62 jobs")

	silent := New(db, Config{AgentID: "a2"}) // no SetNotify
	silent.Tick()

	var got collector
	loud := New(db, Config{AgentID: "a2"})
	loud.SetNotify(got.add)
	loud.Tick()

	if len(got.all()) != 1 {
		t.Fatalf("a gateway that cannot deliver must leave the report for one that can; got %v", got.all())
	}
	after, err := db.GetTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ReportedAt == "" {
		t.Error("the delivering gateway should have marked it")
	}
}

func TestStartDeliversAndStopsWithTheContext(t *testing.T) {
	db := openDB(t)
	delegate(t, db, "crawl listings", coord.TaskCompleted, "62 jobs")

	var got collector
	r := New(db, Config{AgentID: "a2", Tick: time.Hour}) // only the immediate tick fires
	r.SetNotify(got.add)

	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)

	deadline := time.Now().Add(3 * time.Second)
	for len(got.all()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	r.Wait()

	if len(got.all()) != 1 {
		t.Errorf("the first tick should be immediate, so a result waiting at startup is not held for a full interval; got %v", got.all())
	}
}

// A nil Reporter is what a gateway without a coordination database has. Every
// exported method has to tolerate it, because main.go calls them unconditionally.
func TestNilReporterIsInert(t *testing.T) {
	var r *Reporter
	r.SetNotify(func(string) { t.Error("nil reporter must not deliver") })
	r.Poke()
	r.Start(context.Background())
	r.Wait()
	r.Tick()
}

func TestFormatSaysWhatHappenedFirst(t *testing.T) {
	cases := []struct {
		name  string
		task  coord.Task
		want  []string
		avoid string
	}{
		{
			name: "completed",
			task: coord.Task{State: coord.TaskCompleted, Title: "crawl listings", Result: "62 jobs", ClaimedBy: "a1"},
			want: []string{"✅", "crawl listings", "completed", "a1", "62 jobs"},
		},
		{
			name: "failed carries the reason the system gave up",
			task: coord.Task{State: coord.TaskFailed, Title: "broken", ClaimedBy: "a1", FailReason: coord.FailExhausted},
			want: []string{"⛔", "failed", coord.FailExhausted},
		},
		{
			name: "finished with nothing to say",
			task: coord.Task{State: coord.TaskCompleted, Title: "quiet job", ClaimedBy: "a1"},
			want: []string{"finished without a result"},
		},
		// Nobody ever claimed it, so there is no agent to name — and "· " with
		// an empty name reads like a bug.
		{
			name:  "never claimed",
			task:  coord.Task{State: coord.TaskRejected, Title: "nobody took it"},
			want:  []string{"⚠️", "rejected"},
			avoid: "· ",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Format(&c.task)
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in:\n%s", w, got)
				}
			}
			if c.avoid != "" && strings.Contains(got, c.avoid) {
				t.Errorf("should not contain %q:\n%s", c.avoid, got)
			}
		})
	}
}

// Telegram rejects a message over 4096 characters outright, so a long result
// has to arrive trimmed rather than not at all.
func TestFormatTrimsALongResult(t *testing.T) {
	long := strings.Repeat("x", maxResultChars*2)
	got := Format(&coord.Task{State: coord.TaskCompleted, Title: "big", Result: long, ClaimedBy: "a1"})

	if len([]rune(got)) > maxResultChars+200 {
		t.Errorf("delivery is %d runes; too close to Telegram's limit", len([]rune(got)))
	}
	if !strings.Contains(got, "…") {
		t.Error("a trimmed result should say so")
	}
}
