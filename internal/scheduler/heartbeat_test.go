package scheduler

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ngocp/goterm-control/internal/coord"
)

func TestParseActiveHours(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Ho_Chi_Minh")
	at := func(h, m int) time.Time { return time.Date(2026, 9, 9, h, m, 0, 0, loc) }

	always, err := ParseActiveHours("")
	if err != nil || !always.contains(at(3, 0)) {
		t.Errorf("empty = always: %v", err)
	}
	day, err := ParseActiveHours("08:00-23:00")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		h, m int
		in   bool
	}{{7, 59, false}, {8, 0, true}, {12, 0, true}, {22, 59, true}, {23, 0, false}, {2, 0, false}} {
		if got := day.contains(at(c.h, c.m)); got != c.in {
			t.Errorf("08:00-23:00 at %02d:%02d: %v", c.h, c.m, got)
		}
	}
	night, _ := ParseActiveHours("22:00-06:00")
	for _, c := range []struct {
		h, m int
		in   bool
	}{{21, 59, false}, {22, 0, true}, {23, 30, true}, {0, 0, true}, {5, 59, true}, {6, 0, false}, {12, 0, false}} {
		if got := night.contains(at(c.h, c.m)); got != c.in {
			t.Errorf("22:00-06:00 at %02d:%02d: %v", c.h, c.m, got)
		}
	}
	for _, bad := range []string{"8-23", "08:00", "08:00-08:00", "25:00-26:00", "08:00-23:00-01:00"} {
		if _, err := ParseActiveHours(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
	// The zone matters: 10:00 UTC is 17:00 in Ho Chi Minh — inside 15:00-18:00 there, outside in UTC.
	win, _ := ParseActiveHours("15:00-18:00")
	utc10 := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	if win.contains(utc10) || !win.contains(utc10.In(loc)) {
		t.Error("contains must look at the wall clock of the location it is given")
	}
}

func TestIsNoReply(t *testing.T) {
	for _, yes := range []string{"NO_REPLY", "  no_reply ", "`NO_REPLY`", "**NO_REPLY**", "NO_REPLY — nothing to do", ""} {
		if !isNoReply(yes) {
			t.Errorf("%q should be no-reply", yes)
		}
	}
	for _, no := range []string{"CI is red on PR #91", "No reply from the vendor yet — chasing", "Done: NO_REPLY was wrong"} {
		if isNoReply(no) {
			t.Errorf("%q should be delivered", no)
		}
	}
}

// heartbeatDB is a database with agent a1 registered and its heartbeat row due.
func heartbeatDB(t *testing.T, now time.Time, hours string) (*coord.DB, *coord.Schedule) {
	t.Helper()
	db := openDB(t)
	if err := db.RegisterAgent(coord.Agent{ID: "a1", Provider: "claude"}); err != nil {
		t.Fatal(err)
	}
	sc, _, err := db.UpsertSystemSchedule(coord.NewSchedule{
		Name: HeartbeatName("a1"), CreatedBy: "a1", OwnerAgent: "a1", Kind: coord.ScheduleEvery, Spec: "30m", TZ: "UTC",
		PayloadKind: coord.PayloadHeartbeat, Payload: coord.HeartbeatPayload{ActiveHours: hours}, SkipMissed: true,
		NextRunAt: now.Add(-time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	return db, sc
}

func fireDue(db *coord.DB, sc *coord.Schedule, now time.Time) {
	_ = db.FireSchedule(sc.ID, now.Add(-time.Second))
}

func TestHeartbeatSkipRulesCostNothing(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC) // noon UTC
	db, sc := heartbeatDB(t, now, "08:00-23:00")
	var n notes
	busy := false
	s := New(db, Config{AgentID: "a1"})
	s.SetNotify(n.add)
	s.SetBusy(func() bool { return busy })
	fixed(s, now)

	tasksCount := func() int { ts, _ := db.ListTasks(coord.TaskFilter{}); return len(ts) }
	expectSkip := func(step string) {
		t.Helper()
		s.Tick(context.Background())
		s.Wait()
		got, _ := db.GetSchedule(sc.ID)
		if tasksCount() != 0 || got.LastStatus != coord.ScheduleRunSkipped {
			t.Fatalf("%s: tasks=%d status=%s — a skip must create nothing", step, tasksCount(), got.LastStatus)
		}
		if runs, _ := db.ScheduleRuns(sc.ID, 10); len(runs) != 0 {
			t.Fatalf("%s: a skip must not record a run, got %d", step, len(runs))
		}
		if !got.NextRunAt.Equal(now.Add(30 * time.Minute)) {
			t.Fatalf("%s: re-armed for %s, want now+30m", step, got.NextRunAt.Sub(now))
		}
		fireDue(db, sc, now)
	}

	// 1. Empty scratch.
	expectSkip("empty scratch")

	// 2. Outside active hours (03:00 UTC), even with notes.
	db.SetScratch("a1", "watch PR #91")
	night := time.Date(2026, 9, 9, 3, 0, 0, 0, time.UTC)
	fixed(s, night)
	s.Tick(context.Background())
	s.Wait()
	if tasksCount() != 0 {
		t.Fatal("outside active hours must not create a task")
	}
	fixed(s, now)
	fireDue(db, sc, now)

	// 3. Busy agent.
	busy = true
	expectSkip("busy")
	busy = false

	// 4. Everything clear: a task is created, once, with the scratch in it.
	s.Tick(context.Background())
	s.Wait()
	tasks, _ := db.ListTasks(coord.TaskFilter{})
	if len(tasks) != 1 {
		t.Fatalf("want one heartbeat task, got %d", len(tasks))
	}
	task := tasks[0]
	if task.Kind != coord.KindHeartbeat || task.AssignedTo != "a1" || task.ScheduleID != sc.ID || task.MaxContinuations != heartbeatMaxContinuations {
		t.Errorf("task: kind=%s to=%s schedule=%s maxcont=%d", task.Kind, task.AssignedTo, task.ScheduleID, task.MaxContinuations)
	}
	if !strings.Contains(task.Body, "watch PR #91") || !strings.Contains(task.Body, noReply) || !strings.Contains(task.Body, "heartbeat scratch --set") {
		t.Errorf("body should carry the scratch and the rules:\n%s", task.Body)
	}
	runs, _ := db.ScheduleRuns(sc.ID, 10)
	if len(runs) != 1 || runs[0].Status != coord.ScheduleRunPending {
		t.Fatalf("a real look records a pending run: %+v", runs)
	}

	// 5. The task is still open on the next look: skip, do not stack.
	fireDue(db, sc, now)
	s.Tick(context.Background())
	s.Wait()
	if tasksCount() != 1 {
		t.Fatal("a second heartbeat task was stacked on an open one")
	}
	if got, _ := db.GetSchedule(sc.ID); got.LastStatus != coord.ScheduleRunSkipped {
		t.Errorf("open task should read as a skip, got %s", got.LastStatus)
	}
	if len(n.all()) != 0 {
		t.Errorf("nothing should have been delivered yet: %v", n.all())
	}
}

func TestHeartbeatNoReplyIsSilentAndFindingsAreDelivered(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	db, sc := heartbeatDB(t, now, "")
	db.SetScratch("a1", "watch PR #91")
	var n notes
	s := New(db, Config{AgentID: "a1"})
	s.SetNotify(n.add)
	fixed(s, now)

	finish := func(result string) {
		t.Helper()
		claimed, err := db.ClaimTask("a1")
		if err != nil {
			t.Fatal(err)
		}
		run, _ := db.StartRun(claimed.ID, "a1", claimed.Attempts, "")
		if _, err := db.FinishRun(run.ID, coord.RunOutcome{Liveness: coord.RunCompleted, Result: result}); err != nil {
			t.Fatal(err)
		}
		s.Tick(context.Background())
		s.Wait()
	}

	// Look 1: nothing to do.
	s.Tick(context.Background())
	s.Wait()
	finish("`NO_REPLY`")
	if len(n.all()) != 0 {
		t.Fatalf("NO_REPLY must reach nobody: %v", n.all())
	}
	runs, _ := db.ScheduleRuns(sc.ID, 10)
	if len(runs) != 1 || runs[0].Status != coord.ScheduleRunOK {
		t.Fatalf("the run still settles ok: %+v", runs)
	}
	if got, _ := db.GetSchedule(sc.ID); got.LastStatus != coord.ScheduleRunOK || got.ConsecutiveFailures != 0 {
		t.Errorf("schedule after NO_REPLY: %s %d", got.LastStatus, got.ConsecutiveFailures)
	}

	// Look 2: a finding.
	fireDue(db, sc, now)
	s.Tick(context.Background())
	s.Wait()
	finish("PR #91: CI is red on the Windows job — the scheduler tests need the unix build tag.")
	lines := n.all()
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "💓 a1") || !strings.Contains(lines[0], "CI is red") {
		t.Fatalf("finding should be delivered once with the heartbeat prefix: %v", lines)
	}

	// Look 3: the task fails — the ladder applies like any schedule.
	fireDue(db, sc, now)
	s.Tick(context.Background())
	s.Wait()
	tasks, _ := db.ListTasks(coord.TaskFilter{State: coord.TaskSubmitted})
	if len(tasks) != 1 {
		t.Fatalf("expected a fresh heartbeat task, got %d", len(tasks))
	}
	db.CancelTask(tasks[0].ID, "human")
	s.Tick(context.Background())
	s.Wait()
	if got, _ := db.GetSchedule(sc.ID); got.ConsecutiveFailures != 1 || got.LastStatus != coord.ScheduleRunFailed {
		t.Errorf("a failed heartbeat task counts: %s %d", got.LastStatus, got.ConsecutiveFailures)
	}
}

func TestEnsureHeartbeatFollowsConfig(t *testing.T) {
	db := openDB(t)
	db.RegisterAgent(coord.Agent{ID: "a1"})

	// Off with no row: nothing appears.
	if err := EnsureHeartbeat(db, "a1", HeartbeatConfig{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if all, _ := db.ListSchedules(); len(all) != 0 {
		t.Fatal("disabled heartbeat must not create a row")
	}
	// Bad config is refused, not half-applied.
	if err := EnsureHeartbeat(db, "a1", HeartbeatConfig{Enabled: true, Every: "30m", ActiveHours: "9-5"}); err == nil {
		t.Error("bad active_hours must be refused")
	}
	if err := EnsureHeartbeat(db, "a1", HeartbeatConfig{Enabled: true, Every: "10s"}); err == nil {
		t.Error("every under 1m must be refused")
	}
	if all, _ := db.ListSchedules(); len(all) != 0 {
		t.Fatal("refused config must not leave a row")
	}
	// On: one system row owned by a1, skip_missed, tz filled in.
	if err := EnsureHeartbeat(db, "a1", HeartbeatConfig{Enabled: true, Every: "30m", ActiveHours: "08:00-23:00"}); err != nil {
		t.Fatal(err)
	}
	sc, err := db.FindSchedule(HeartbeatName("a1"))
	if err != nil || !sc.System || !sc.Enabled || sc.OwnerAgent != "a1" || !sc.SkipMissed || sc.TZ == "" || sc.PayloadKind != coord.PayloadHeartbeat {
		t.Fatalf("row: %+v err=%v", sc, err)
	}
	if !strings.Contains(string(sc.Payload), "08:00-23:00") {
		t.Errorf("payload: %s", sc.Payload)
	}
	// Off again: the row stays, disabled.
	if err := EnsureHeartbeat(db, "a1", HeartbeatConfig{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	sc, _ = db.FindSchedule(HeartbeatName("a1"))
	if sc.Enabled {
		t.Error("turning heartbeat off in config must disable the row")
	}
	// The other agent's row is untouched by a1's config.
	db.RegisterAgent(coord.Agent{ID: "a2"})
	EnsureHeartbeat(db, "a2", HeartbeatConfig{Enabled: true, Every: "1h"})
	all, _ := db.ListSchedules()
	if len(all) != 2 {
		t.Errorf("one row per agent, got %d", len(all))
	}
}
