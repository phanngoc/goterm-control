package scheduler

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
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// fixed pins the scheduler's clock so "due" and "missed" are deterministic.
func fixed(s *Scheduler, at time.Time) { s.now = func() time.Time { return at } }

type notes struct {
	mu    sync.Mutex
	lines []string
}

func (n *notes) add(s string) { n.mu.Lock(); n.lines = append(n.lines, s); n.mu.Unlock() }
func (n *notes) all() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.lines...)
}

func TestNextRun(t *testing.T) {
	hcm, _ := time.LoadLocation("Asia/Ho_Chi_Minh")
	// Friday 2026-09-11 09:00 Ho Chi Minh time.
	from := time.Date(2026, 9, 11, 9, 0, 0, 0, hcm)

	next, err := NextRun(coord.ScheduleCron, "0 8 * * 1-5", "Asia/Ho_Chi_Minh", from)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 14, 8, 0, 0, 0, hcm) // Monday 08:00 local
	if !next.Equal(want) {
		t.Errorf("weekday cron from Friday 09:00: got %s, want %s", next.In(hcm), want)
	}
	// Same cron read in UTC is a different instant: 08:00 UTC = 15:00 HCM.
	utcNext, _ := NextRun(coord.ScheduleCron, "0 8 * * 1-5", "UTC", from)
	if utcNext.Equal(next) {
		t.Error("tz must change the instant")
	}
	if _, err := NextRun(coord.ScheduleCron, "0 8 * *", "UTC", from); err == nil {
		t.Error("4-field cron must be rejected")
	}
	if next, _ := NextRun(coord.ScheduleCron, "@daily", "UTC", from); next.IsZero() {
		t.Error("descriptors should parse")
	}

	if next, err := NextRun(coord.ScheduleEvery, "10m", "UTC", from); err != nil || !next.Equal(from.Add(10*time.Minute)) {
		t.Errorf("every 10m: %v %v", next, err)
	}
	if _, err := NextRun(coord.ScheduleEvery, "10s", "UTC", from); err == nil {
		t.Error("every under 1m must be rejected")
	}
	if _, err := NextRun(coord.ScheduleEvery, "soon", "UTC", from); err == nil {
		t.Error("non-duration must be rejected")
	}

	at, err := NextRun(coord.ScheduleAt, "2026-09-12T07:30", "Asia/Ho_Chi_Minh", from)
	if err != nil || !at.Equal(time.Date(2026, 9, 12, 7, 30, 0, 0, hcm)) {
		t.Errorf("at local: %v %v", at, err)
	}
	if past, err := NextRun(coord.ScheduleAt, "2026-09-10T07:30", "Asia/Ho_Chi_Minh", from); err != nil || !past.IsZero() {
		t.Errorf("at in the past must be zero/never: %v %v", past, err)
	}
	if _, err := NextRun(coord.ScheduleEvery, "10m", "Mars/Olympus", from); err == nil {
		t.Error("bad tz must be rejected")
	}
	if LocalZone() == "" {
		t.Error("LocalZone must always name something")
	}
}

func TestCommandScheduleRunsAndRecordsOutput(t *testing.T) {
	db := openDB(t)
	now := time.Now()
	sc, err := db.CreateSchedule(coord.NewSchedule{Name: "echo", CreatedBy: "t", Kind: coord.ScheduleEvery, Spec: "10m", TZ: "UTC",
		PayloadKind: coord.PayloadCommand, Payload: coord.CommandPayload{Cmd: "echo hello $BOMCLAW_SCHEDULE"}, NextRunAt: now.Add(-time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	s := New(db, Config{AgentID: "a1"})
	fixed(s, now)
	s.Tick(context.Background())
	s.Wait()

	runs, _ := db.ScheduleRuns(sc.ID, 10)
	if len(runs) != 1 || runs[0].Status != coord.ScheduleRunOK || strings.TrimSpace(runs[0].Output) != "hello echo" {
		t.Fatalf("runs: %+v", runs)
	}
	got, _ := db.GetSchedule(sc.ID)
	if got.LastStatus != coord.ScheduleRunOK || !got.NextRunAt.Equal(now.Add(10*time.Minute)) {
		t.Errorf("after ok run: status=%s next=%s", got.LastStatus, got.NextRunAt)
	}
	// Not due again until then.
	s.Tick(context.Background())
	s.Wait()
	if runs, _ := db.ScheduleRuns(sc.ID, 10); len(runs) != 1 {
		t.Errorf("fired again before its time: %d runs", len(runs))
	}
}

func TestCommandFailureClimbsLadderAndAlerts(t *testing.T) {
	db := openDB(t)
	now := time.Now()
	sc, _ := db.CreateSchedule(coord.NewSchedule{Name: "broken", CreatedBy: "t", Kind: coord.ScheduleEvery, Spec: "10m", TZ: "UTC",
		PayloadKind: coord.PayloadCommand, Payload: coord.CommandPayload{Cmd: "echo boom >&2; exit 3"}, NextRunAt: now.Add(-time.Second)})
	var n notes
	s := New(db, Config{AgentID: "a1"})
	s.SetNotify(n.add)

	clock := now
	fixed(s, clock)
	s.Tick(context.Background())
	s.Wait()
	got, _ := db.GetSchedule(sc.ID)
	if got.ConsecutiveFailures != 1 || got.NextRunAt.Sub(clock) != 30*time.Second {
		t.Fatalf("first failure: failures=%d next-in=%s", got.ConsecutiveFailures, got.NextRunAt.Sub(clock))
	}
	if len(n.all()) != 0 {
		t.Fatalf("one failure must not alert: %v", n.all())
	}
	runs, _ := db.ScheduleRuns(sc.ID, 10)
	if runs[0].ExitCode != 3 || !strings.Contains(runs[0].Output, "boom") {
		t.Errorf("run: %+v", runs[0])
	}

	// Second failure: due after the 30s backoff; alerts once.
	clock = clock.Add(31 * time.Second)
	fixed(s, clock)
	s.Tick(context.Background())
	s.Wait()
	if lines := n.all(); len(lines) != 1 || !strings.Contains(lines[0], "failed 2 times") {
		t.Fatalf("second failure should alert once: %v", lines)
	}
	// Third failure within the cooldown: no second alert.
	clock = clock.Add(2 * time.Minute)
	fixed(s, clock)
	s.Tick(context.Background())
	s.Wait()
	if len(n.all()) != 1 {
		t.Fatalf("alert cooldown not honoured: %v", n.all())
	}
	// Drive to auto-disable; the disable notice bypasses the cooldown.
	for i := 0; i < coord.DisableAfterFailures; i++ {
		clock = clock.Add(2 * time.Hour)
		fixed(s, clock)
		s.Tick(context.Background())
		s.Wait()
	}
	got, _ = db.GetSchedule(sc.ID)
	if got.Enabled {
		t.Fatalf("should be auto-disabled after %d failures, have %d", coord.DisableAfterFailures, got.ConsecutiveFailures)
	}
	var sawDisable int
	for _, l := range n.all() {
		if strings.Contains(l, "disabled after") {
			sawDisable++
		}
	}
	if sawDisable != 1 {
		t.Errorf("disable notice should go out exactly once, got %d in %v", sawDisable, n.all())
	}
}

func TestCommandTimeoutFails(t *testing.T) {
	db := openDB(t)
	now := time.Now()
	sc, _ := db.CreateSchedule(coord.NewSchedule{Name: "slow", CreatedBy: "t", Kind: coord.ScheduleEvery, Spec: "10m", TZ: "UTC",
		PayloadKind: coord.PayloadCommand, Payload: coord.CommandPayload{Cmd: "sleep 5", TimeoutS: 1}, NextRunAt: now.Add(-time.Second)})
	s := New(db, Config{AgentID: "a1"})
	fixed(s, now)
	start := time.Now()
	s.Tick(context.Background())
	s.Wait()
	if time.Since(start) > 3*time.Second {
		t.Fatalf("timeout not enforced: took %s", time.Since(start))
	}
	runs, _ := db.ScheduleRuns(sc.ID, 10)
	if len(runs) != 1 || runs[0].Status != coord.ScheduleRunFailed || !strings.Contains(runs[0].Output, "timed out") {
		t.Errorf("runs: %+v", runs)
	}
}

// Eight hours of downtime over an hourly schedule: one catch-up run, and the
// next run is computed from now, not from the missed mark.
func TestMissedScheduleCatchesUpOnce(t *testing.T) {
	db := openDB(t)
	now := time.Now()
	sc, _ := db.CreateSchedule(coord.NewSchedule{Name: "hourly", CreatedBy: "t", Kind: coord.ScheduleEvery, Spec: "1h", TZ: "UTC",
		PayloadKind: coord.PayloadCommand, Payload: coord.CommandPayload{Cmd: "true"}, NextRunAt: now.Add(-8 * time.Hour)})
	s := New(db, Config{AgentID: "a1"})
	fixed(s, now)
	s.Tick(context.Background())
	s.Wait()
	s.Tick(context.Background())
	s.Wait()
	runs, _ := db.ScheduleRuns(sc.ID, 10)
	if len(runs) != 1 || runs[0].Status != coord.ScheduleRunOK {
		t.Fatalf("want exactly one catch-up run, got %+v", runs)
	}
	got, _ := db.GetSchedule(sc.ID)
	if !got.NextRunAt.Equal(now.Add(time.Hour)) {
		t.Errorf("next must be now+1h, got %s", got.NextRunAt.Sub(now))
	}
}

func TestMissedScheduleWithSkipMissedReArms(t *testing.T) {
	db := openDB(t)
	now := time.Now()
	sc, _ := db.CreateSchedule(coord.NewSchedule{Name: "skippy", CreatedBy: "t", Kind: coord.ScheduleEvery, Spec: "1h", TZ: "UTC",
		PayloadKind: coord.PayloadCommand, Payload: coord.CommandPayload{Cmd: "echo RAN"}, SkipMissed: true, NextRunAt: now.Add(-8 * time.Hour)})
	s := New(db, Config{AgentID: "a1"})
	fixed(s, now)
	s.Tick(context.Background())
	s.Wait()
	runs, _ := db.ScheduleRuns(sc.ID, 10)
	if len(runs) != 1 || runs[0].Status != coord.ScheduleRunSkipped || strings.Contains(runs[0].Output, "RAN") {
		t.Fatalf("want one skipped run and no execution, got %+v", runs)
	}
	got, _ := db.GetSchedule(sc.ID)
	if !got.NextRunAt.Equal(now.Add(time.Hour)) || got.LastStatus != coord.ScheduleRunSkipped {
		t.Errorf("re-armed: next-in=%s status=%s", got.NextRunAt.Sub(now), got.LastStatus)
	}
	// A schedule found one tick late is NOT missed: it runs.
	late, _ := db.CreateSchedule(coord.NewSchedule{Name: "ontime", CreatedBy: "t", Kind: coord.ScheduleEvery, Spec: "1h", TZ: "UTC",
		PayloadKind: coord.PayloadCommand, Payload: coord.CommandPayload{Cmd: "echo RAN"}, SkipMissed: true, NextRunAt: now.Add(-40 * time.Second)})
	s.Tick(context.Background())
	s.Wait()
	runs, _ = db.ScheduleRuns(late.ID, 10)
	if len(runs) != 1 || runs[0].Status != coord.ScheduleRunOK {
		t.Errorf("40s late with tick 30s is on time, want a real run: %+v", runs)
	}
}

func TestAgentScheduleMaterialisesTaskAndSettles(t *testing.T) {
	db := openDB(t)
	now := time.Now()
	sc, _ := db.CreateSchedule(coord.NewSchedule{Name: "briefing", CreatedBy: "me", OwnerAgent: "a1", Kind: coord.ScheduleCron,
		Spec: "0 8 * * *", TZ: "UTC", PayloadKind: coord.PayloadAgent,
		Payload: coord.AgentPayload{Title: "Morning briefing", Body: "Summarise the inbox."}, NextRunAt: now.Add(-time.Second)})
	var n notes
	var woke []*coord.Task
	s := New(db, Config{AgentID: "a1"})
	s.SetNotify(n.add)
	s.SetWake(func(t *coord.Task) { woke = append(woke, t) })
	fixed(s, now)

	// The other gateway must leave an owned schedule alone.
	other := New(db, Config{AgentID: "a2"})
	fixed(other, now)
	other.Tick(context.Background())
	other.Wait()
	if tasks, _ := db.ListTasks(coord.TaskFilter{}); len(tasks) != 0 {
		t.Fatal("a2 fired a schedule owned by a1")
	}

	s.Tick(context.Background())
	s.Wait()
	tasks, _ := db.ListTasks(coord.TaskFilter{})
	if len(tasks) != 1 {
		t.Fatalf("want one task, got %d", len(tasks))
	}
	task := tasks[0]
	if task.Kind != coord.KindScheduled || task.ScheduleID != sc.ID || task.AssignedTo != "a1" || task.CreatedBy != "schedule:briefing" {
		t.Errorf("task: kind=%s schedule=%s to=%s by=%s", task.Kind, task.ScheduleID, task.AssignedTo, task.CreatedBy)
	}
	if !strings.Contains(task.Body, "Summarise the inbox.") || !strings.Contains(task.Body, "schedule \"briefing\"") {
		t.Errorf("body: %q", task.Body)
	}
	if len(woke) != 1 || woke[0].ID != task.ID {
		t.Errorf("wake should ring once for the task")
	}
	runs, _ := db.ScheduleRuns(sc.ID, 10)
	if len(runs) != 1 || runs[0].Status != coord.ScheduleRunPending || runs[0].TaskID != task.ID {
		t.Fatalf("run should be pending on the task: %+v", runs)
	}

	// Still working: another tick changes nothing.
	s.Tick(context.Background())
	s.Wait()
	if runs, _ := db.ScheduleRuns(sc.ID, 10); len(runs) != 1 || runs[0].Status != coord.ScheduleRunPending {
		t.Fatalf("pending run touched too early: %+v", runs)
	}

	// The agent finishes it; the next tick settles the run and delivers.
	claimed, err := db.ClaimTask("a1")
	if err != nil {
		t.Fatal(err)
	}
	run, _ := db.StartRun(claimed.ID, "a1", claimed.Attempts, "")
	if _, err := db.FinishRun(run.ID, coord.RunOutcome{Liveness: coord.RunCompleted, Result: "3 mails, none urgent"}); err != nil {
		t.Fatal(err)
	}
	s.Tick(context.Background())
	s.Wait()
	runs, _ = db.ScheduleRuns(sc.ID, 10)
	if runs[0].Status != coord.ScheduleRunOK || !strings.Contains(runs[0].Output, "3 mails") {
		t.Errorf("settled: %+v", runs[0])
	}
	got, _ := db.GetSchedule(sc.ID)
	if got.LastStatus != coord.ScheduleRunOK {
		t.Errorf("schedule status %s", got.LastStatus)
	}
	lines := n.all()
	if len(lines) != 1 || !strings.Contains(lines[0], "briefing") || !strings.Contains(lines[0], "3 mails, none urgent") {
		t.Errorf("result should be delivered once: %v", lines)
	}
}

func TestAgentScheduleTaskFailureCountsAndQuietStaysQuiet(t *testing.T) {
	db := openDB(t)
	now := time.Now()
	sc, _ := db.CreateSchedule(coord.NewSchedule{Name: "quiet", CreatedBy: "me", Kind: coord.ScheduleEvery, Spec: "1h", TZ: "UTC",
		PayloadKind: coord.PayloadAgent, Payload: coord.AgentPayload{Title: "Tidy", Quiet: true}, NextRunAt: now.Add(-time.Second)})
	var n notes
	s := New(db, Config{AgentID: "a1"})
	s.SetNotify(n.add)
	fixed(s, now)
	s.Tick(context.Background())
	s.Wait()
	tasks, _ := db.ListTasks(coord.TaskFilter{})
	if len(tasks) != 1 {
		t.Fatal("no task")
	}
	// Canceled counts as a failure for the schedule.
	if err := db.CancelTask(tasks[0].ID, "me"); err != nil {
		t.Fatal(err)
	}
	s.Tick(context.Background())
	s.Wait()
	got, _ := db.GetSchedule(sc.ID)
	if got.ConsecutiveFailures != 1 || got.LastStatus != coord.ScheduleRunFailed {
		t.Errorf("task cancel should count as a failure: failures=%d status=%s", got.ConsecutiveFailures, got.LastStatus)
	}
	if len(n.all()) != 0 {
		t.Errorf("one failure, quiet payload: nothing should be sent, got %v", n.all())
	}
	// A completed quiet task delivers nothing either.
	sc2, _ := db.CreateSchedule(coord.NewSchedule{Name: "quiet2", CreatedBy: "me", Kind: coord.ScheduleEvery, Spec: "1h", TZ: "UTC",
		PayloadKind: coord.PayloadAgent, Payload: coord.AgentPayload{Title: "Tidy2", Quiet: true}, NextRunAt: now.Add(-time.Second)})
	s.Tick(context.Background())
	s.Wait()
	claimed, _ := db.ClaimTask("a1")
	run, _ := db.StartRun(claimed.ID, "a1", claimed.Attempts, "")
	db.FinishRun(run.ID, coord.RunOutcome{Liveness: coord.RunCompleted, Result: "done"})
	s.Tick(context.Background())
	s.Wait()
	if runs, _ := db.ScheduleRuns(sc2.ID, 5); runs[0].Status != coord.ScheduleRunOK {
		t.Errorf("quiet run should settle ok: %+v", runs[0])
	}
	if len(n.all()) != 0 {
		t.Errorf("quiet must not deliver: %v", n.all())
	}
}

// Two schedulers on two handles, both ticking at the same instant, produce
// exactly one run per due schedule.
func TestTwoGatewaysFireOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coord.db")
	a, _ := coord.Open(path)
	defer a.Close()
	b, _ := coord.Open(path)
	defer b.Close()
	now := time.Now()
	for i := 0; i < 5; i++ {
		a.CreateSchedule(coord.NewSchedule{Name: "s" + string(rune('a'+i)), CreatedBy: "t", Kind: coord.ScheduleEvery, Spec: "10m", TZ: "UTC",
			PayloadKind: coord.PayloadCommand, Payload: coord.CommandPayload{Cmd: "true"}, NextRunAt: now.Add(-time.Second)})
	}
	sa, sb := New(a, Config{AgentID: "a1"}), New(b, Config{AgentID: "a2"})
	fixed(sa, now)
	fixed(sb, now)
	var wg sync.WaitGroup
	for _, s := range []*Scheduler{sa, sb} {
		wg.Add(1)
		go func(s *Scheduler) {
			defer wg.Done()
			s.Tick(context.Background())
			s.Wait()
		}(s)
	}
	wg.Wait()
	all, _ := a.ListSchedules()
	for _, sc := range all {
		runs, _ := a.ScheduleRuns(sc.ID, 10)
		if len(runs) != 1 {
			t.Errorf("%s: %d runs, want 1", sc.Name, len(runs))
		}
	}
}
