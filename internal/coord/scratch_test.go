package coord

import (
	"strings"
	"testing"
	"time"
)

func registerTestAgent(t *testing.T, db *DB, id string) {
	t.Helper()
	if err := db.RegisterAgent(Agent{ID: id, Provider: "claude"}); err != nil {
		t.Fatal(err)
	}
}

func TestScratchSetAppendClearAndCap(t *testing.T) {
	db := testDB(t)
	if _, err := db.Scratch("ghost"); err == nil || !strings.Contains(err.Error(), "not started") {
		t.Errorf("unregistered agent: got %v", err)
	}
	if err := db.SetScratch("ghost", "x"); err == nil {
		t.Error("set on unregistered agent must fail")
	}
	registerTestAgent(t, db, "a1")

	if s, err := db.Scratch("a1"); err != nil || s != "" {
		t.Fatalf("fresh agent should have an empty scratch: %q %v", s, err)
	}
	if err := db.SetScratch("a1", "  watch PR #91 until CI is green  "); err != nil {
		t.Fatal(err)
	}
	s, _ := db.Scratch("a1")
	if s != "watch PR #91 until CI is green" {
		t.Errorf("set should trim: %q", s)
	}
	day := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	if err := db.AppendScratch("a1", "ping the vendor on Thursday", day); err != nil {
		t.Fatal(err)
	}
	s, _ = db.Scratch("a1")
	want := "watch PR #91 until CI is green\n- [2026-09-09] ping the vendor on Thursday"
	if s != want {
		t.Errorf("append:\n%q\nwant\n%q", s, want)
	}
	if err := db.AppendScratch("a1", "   ", day); err == nil {
		t.Error("appending nothing must be refused")
	}
	if err := db.SetScratch("a1", strings.Repeat("x", MaxScratch+1)); err == nil || !strings.Contains(err.Error(), "cap") {
		t.Errorf("over the cap: got %v", err)
	}
	if err := db.SetScratch("a1", ""); err != nil {
		t.Fatal(err)
	}
	if s, _ := db.Scratch("a1"); s != "" {
		t.Errorf("clear left %q", s)
	}
}

func TestUpsertSystemScheduleRealignsOnlyWhenChanged(t *testing.T) {
	db := testDB(t)
	now := time.Now()
	n := NewSchedule{Name: "heartbeat:a1", CreatedBy: "a1", OwnerAgent: "a1", Kind: ScheduleEvery, Spec: "30m", TZ: "UTC",
		PayloadKind: PayloadHeartbeat, Payload: HeartbeatPayload{ActiveHours: "08:00-23:00"}, SkipMissed: true, NextRunAt: now.Add(30 * time.Minute)}

	s, changed, err := db.UpsertSystemSchedule(n)
	if err != nil || !changed || !s.System || !s.Enabled {
		t.Fatalf("first upsert: changed=%v system=%v enabled=%v err=%v", changed, s != nil && s.System, s != nil && s.Enabled, err)
	}
	first := s.NextRunAt

	// Same config again: nothing written, clock untouched.
	n.NextRunAt = now.Add(45 * time.Minute)
	s, changed, err = db.UpsertSystemSchedule(n)
	if err != nil || changed || !s.NextRunAt.Equal(first) {
		t.Fatalf("same config: changed=%v next=%v (want %v) err=%v", changed, s.NextRunAt, first, err)
	}

	// Failures piled up and the row auto-disabled: a restart re-arms it.
	for i := 0; i < DisableAfterFailures; i++ {
		db.ScheduleFailed(s.ID, now)
	}
	if got, _ := db.GetSchedule(s.ID); got.Enabled {
		t.Fatal("precondition: should be disabled")
	}
	s, changed, err = db.UpsertSystemSchedule(n)
	if err != nil || !changed || !s.Enabled || s.ConsecutiveFailures != 0 || !s.NextRunAt.Equal(n.NextRunAt) {
		t.Fatalf("re-enable: changed=%v enabled=%v failures=%d next=%v err=%v", changed, s.Enabled, s.ConsecutiveFailures, s.NextRunAt, err)
	}

	// A new cadence re-arms from the caller's next.
	n.Spec = "1h"
	n.NextRunAt = now.Add(time.Hour)
	s, changed, _ = db.UpsertSystemSchedule(n)
	if !changed || s.Spec != "1h" || !s.NextRunAt.Equal(n.NextRunAt) {
		t.Errorf("cadence change: changed=%v spec=%s next=%v", changed, s.Spec, s.NextRunAt)
	}
	// Still one row, still not removable from the CLI.
	all, _ := db.ListSchedules()
	if len(all) != 1 {
		t.Errorf("upsert made %d rows", len(all))
	}
	if err := db.DeleteSchedule(s.ID); err != ErrSystemSchedule {
		t.Errorf("delete: %v", err)
	}
}

func TestOpenScheduledTask(t *testing.T) {
	db := testDB(t)
	s := newSchedule(t, db, "s", time.Now())
	if open, err := db.OpenScheduledTask(s.ID); err != nil || open != nil {
		t.Fatalf("nothing yet: %v %v", open, err)
	}
	task, _ := db.CreateTask(NewTask{CreatedBy: "x", Title: "look", Kind: KindHeartbeat, ScheduleID: s.ID, MaxContinuations: 2})
	if task.MaxContinuations != 2 {
		t.Errorf("MaxContinuations not applied: %d", task.MaxContinuations)
	}
	if open, _ := db.OpenScheduledTask(s.ID); open == nil || open.ID != task.ID {
		t.Fatal("a submitted task is open")
	}
	claimed, _ := db.ClaimTask("a1")
	if open, _ := db.OpenScheduledTask(s.ID); open == nil {
		t.Fatal("a working task is open")
	}
	run, _ := db.StartRun(claimed.ID, "a1", claimed.Attempts, "")
	db.FinishRun(run.ID, RunOutcome{Liveness: RunCompleted, Result: "NO_REPLY"})
	if open, _ := db.OpenScheduledTask(s.ID); open != nil {
		t.Fatal("a completed task is not open")
	}
	// Default continuations when unset.
	plain, _ := db.CreateTask(NewTask{CreatedBy: "x", Title: "t"})
	if plain.MaxContinuations != DefaultMaxContinuations {
		t.Errorf("default continuations: %d", plain.MaxContinuations)
	}
}
