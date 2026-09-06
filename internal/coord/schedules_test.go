package coord

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ngocp/goterm-control/internal/storage"
)

func newSchedule(t *testing.T, db *DB, name string, next time.Time) *Schedule {
	t.Helper()
	s, err := db.CreateSchedule(NewSchedule{
		Name: name, CreatedBy: "test", Kind: ScheduleEvery, Spec: "10m", TZ: "UTC",
		PayloadKind: PayloadCommand, Payload: CommandPayload{Cmd: "true"}, NextRunAt: next,
	})
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	return s
}

func TestCreateScheduleValidatesShape(t *testing.T) {
	db := testDB(t)
	now := time.Now()
	cases := []struct {
		name string
		n    NewSchedule
		want string
	}{
		{"no name", NewSchedule{Kind: ScheduleEvery, Spec: "1m", TZ: "UTC", PayloadKind: PayloadCommand, Payload: CommandPayload{Cmd: "x"}, NextRunAt: now}, "name is required"},
		{"bad kind", NewSchedule{Name: "a", Kind: "weekly", Spec: "1m", TZ: "UTC", PayloadKind: PayloadCommand, Payload: CommandPayload{Cmd: "x"}, NextRunAt: now}, "must be at, every or cron"},
		{"no tz", NewSchedule{Name: "a", Kind: ScheduleEvery, Spec: "1m", PayloadKind: PayloadCommand, Payload: CommandPayload{Cmd: "x"}, NextRunAt: now}, "tz is required"},
		{"no next", NewSchedule{Name: "a", Kind: ScheduleEvery, Spec: "1m", TZ: "UTC", PayloadKind: PayloadCommand, Payload: CommandPayload{Cmd: "x"}}, "no first run time"},
		{"agent without title", NewSchedule{Name: "a", Kind: ScheduleEvery, Spec: "1m", TZ: "UTC", PayloadKind: PayloadAgent, Payload: AgentPayload{}, NextRunAt: now}, "needs a title"},
		{"command without cmd", NewSchedule{Name: "a", Kind: ScheduleEvery, Spec: "1m", TZ: "UTC", PayloadKind: PayloadCommand, Payload: CommandPayload{}, NextRunAt: now}, "needs cmd"},
		{"bad payload kind", NewSchedule{Name: "a", Kind: ScheduleEvery, Spec: "1m", TZ: "UTC", PayloadKind: "webhook", Payload: `{}`, NextRunAt: now}, "must be agent or command"},
	}
	for _, c := range cases {
		_, err := db.CreateSchedule(c.n)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want error containing %q", c.name, err, c.want)
		}
	}
	newSchedule(t, db, "dup", now)
	if _, err := db.CreateSchedule(NewSchedule{Name: "dup", CreatedBy: "t", Kind: ScheduleEvery, Spec: "1m", TZ: "UTC",
		PayloadKind: PayloadCommand, Payload: CommandPayload{Cmd: "x"}, NextRunAt: now}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("duplicate name: got %v", err)
	}
}

// The heart of P1: two gateways hold two handles to one file, both see the
// schedule due, both try to claim it — exactly one wins.
func TestClaimScheduleIsExclusiveAcrossHandles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coord.db")
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	now := time.Now()
	s := newSchedule(t, a, "shared", now.Add(-time.Minute))

	dueA, _ := a.DueSchedules(now)
	dueB, _ := b.DueSchedules(now)
	if len(dueA) != 1 || len(dueB) != 1 {
		t.Fatalf("both handles should see one due schedule, got %d and %d", len(dueA), len(dueB))
	}
	next := now.Add(10 * time.Minute)
	wonA, err := a.ClaimSchedule(s.ID, dueA[0].NextRaw(), now, next)
	if err != nil {
		t.Fatal(err)
	}
	wonB, err := b.ClaimSchedule(s.ID, dueB[0].NextRaw(), now, next)
	if err != nil {
		t.Fatal(err)
	}
	if wonA == wonB {
		t.Fatalf("exactly one claim must win: a=%v b=%v", wonA, wonB)
	}
	got, _ := a.GetSchedule(s.ID)
	if !got.NextRunAt.Equal(next) || got.LastRunAt.IsZero() {
		t.Errorf("winner should have moved the clock: next=%v last=%v", got.NextRunAt, got.LastRunAt)
	}
	if due, _ := b.DueSchedules(now); len(due) != 0 {
		t.Errorf("claimed schedule must no longer be due, got %d", len(due))
	}
}

func TestClaimScheduleRefusesDisabledAndStaleToken(t *testing.T) {
	db := testDB(t)
	now := time.Now()
	s := newSchedule(t, db, "s", now.Add(-time.Second))
	if won, _ := db.ClaimSchedule(s.ID, "2000-01-01T00:00:00Z", now, now.Add(time.Hour)); won {
		t.Error("a stale token must not claim")
	}
	if err := db.SetScheduleEnabled(s.ID, false, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if won, _ := db.ClaimSchedule(s.ID, s.NextRaw(), now, now.Add(time.Hour)); won {
		t.Error("a disabled schedule must not claim")
	}
}

// A one-shot: claim stores Never, success switches it off, failure keeps it
// retrying up the ladder.
func TestOneShotLifecycle(t *testing.T) {
	db := testDB(t)
	now := time.Now()
	s, err := db.CreateSchedule(NewSchedule{Name: "once", CreatedBy: "t", Kind: ScheduleAt, Spec: "x", TZ: "UTC",
		PayloadKind: PayloadCommand, Payload: CommandPayload{Cmd: "true"}, NextRunAt: now.Add(-time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if won, _ := db.ClaimSchedule(s.ID, s.NextRaw(), now, time.Time{}); !won {
		t.Fatal("claim should win")
	}
	got, _ := db.GetSchedule(s.ID)
	if !got.Done() || !got.Enabled {
		t.Fatalf("after claim: done=%v enabled=%v, want done and still enabled", got.Done(), got.Enabled)
	}
	// Failure: retried at now+30s, still enabled.
	updated, disabled, err := db.ScheduleFailed(s.ID, now)
	if err != nil || disabled {
		t.Fatalf("first failure: err=%v disabled=%v", err, disabled)
	}
	if !updated.Enabled || updated.ConsecutiveFailures != 1 || updated.NextRunAt.Sub(now) != 30*time.Second {
		t.Errorf("after one failure: enabled=%v failures=%d next-in=%s", updated.Enabled, updated.ConsecutiveFailures, updated.NextRunAt.Sub(now))
	}
	// Success: off for good.
	if err := db.ScheduleSucceeded(s.ID, ScheduleRunOK, now); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetSchedule(s.ID)
	if got.Enabled || got.ConsecutiveFailures != 0 || got.LastStatus != ScheduleRunOK {
		t.Errorf("after success: enabled=%v failures=%d status=%s", got.Enabled, got.ConsecutiveFailures, got.LastStatus)
	}
}

func TestFailureLadderAndAutoDisable(t *testing.T) {
	db := testDB(t)
	now := time.Now()
	s := newSchedule(t, db, "flaky", now)

	wantBackoff := []time.Duration{30 * time.Second, time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour, time.Hour}
	for i, want := range wantBackoff {
		updated, disabled, err := db.ScheduleFailed(s.ID, now)
		if err != nil {
			t.Fatal(err)
		}
		if disabled {
			t.Fatalf("failure %d must not disable", i+1)
		}
		if got := updated.NextRunAt.Sub(now); got != want {
			t.Errorf("failure %d: next in %s, want %s", i+1, got, want)
		}
		if updated.ConsecutiveFailures != i+1 {
			t.Errorf("failure %d: count %d", i+1, updated.ConsecutiveFailures)
		}
	}
	// Recovery resets the streak.
	if err := db.ScheduleSucceeded(s.ID, ScheduleRunOK, now); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.GetSchedule(s.ID); got.ConsecutiveFailures != 0 || !got.Enabled {
		t.Fatalf("success should reset: %+v", got)
	}
	// Ten in a row switches it off; the disable is reported exactly once.
	var disabledAt int
	for i := 1; i <= DisableAfterFailures+1; i++ {
		updated, disabled, err := db.ScheduleFailed(s.ID, now)
		if err != nil {
			t.Fatal(err)
		}
		if disabled {
			if disabledAt != 0 {
				t.Fatalf("disable reported twice (at %d and %d)", disabledAt, i)
			}
			disabledAt = i
		}
		if i >= DisableAfterFailures && updated.Enabled {
			t.Errorf("failure %d: still enabled", i)
		}
		if i < DisableAfterFailures && !updated.Enabled {
			t.Errorf("failure %d: disabled too early", i)
		}
	}
	if disabledAt != DisableAfterFailures {
		t.Errorf("disable reported at failure %d, want %d", disabledAt, DisableAfterFailures)
	}
}

func TestEnableTakesFreshClockAndResetsStreak(t *testing.T) {
	db := testDB(t)
	now := time.Now()
	s := newSchedule(t, db, "paused", now.Add(-time.Hour))
	for i := 0; i < 3; i++ {
		db.ScheduleFailed(s.ID, now)
	}
	db.SetScheduleEnabled(s.ID, false, time.Time{})
	if err := db.SetScheduleEnabled(s.ID, true, time.Time{}); err == nil {
		t.Error("enabling without a next time must be refused")
	}
	next := now.Add(time.Hour)
	if err := db.SetScheduleEnabled(s.ID, true, next); err != nil {
		t.Fatal(err)
	}
	got, _ := db.GetSchedule(s.ID)
	if !got.Enabled || got.ConsecutiveFailures != 0 || !got.NextRunAt.Equal(next) {
		t.Errorf("after enable: %+v", got)
	}
}

func TestSettleScheduleRunIsCompareAndSet(t *testing.T) {
	db := testDB(t)
	now := time.Now()
	s := newSchedule(t, db, "agentic", now)
	run, err := db.RecordScheduleRun(ScheduleRun{ScheduleID: s.ID, TaskID: "t_1", StartedAt: now, Status: ScheduleRunPending})
	if err != nil {
		t.Fatal(err)
	}
	pending, _ := db.PendingScheduleRuns()
	if len(pending) != 1 || pending[0].ID != run.ID {
		t.Fatalf("pending: %+v", pending)
	}
	won1, _ := db.SettleScheduleRun(run.ID, ScheduleRunOK, "done", now)
	won2, _ := db.SettleScheduleRun(run.ID, ScheduleRunFailed, "again", now)
	if !won1 || won2 {
		t.Fatalf("first settle wins, second loses: %v %v", won1, won2)
	}
	runs, _ := db.ScheduleRuns(s.ID, 10)
	if len(runs) != 1 || runs[0].Status != ScheduleRunOK || runs[0].Output != "done" || runs[0].EndedAt.IsZero() {
		t.Errorf("settled run: %+v", runs[0])
	}
	if pending, _ := db.PendingScheduleRuns(); len(pending) != 0 {
		t.Errorf("nothing should be pending, got %d", len(pending))
	}
}

func TestRecordScheduleRunCutsOutputKeepingHeadAndTail(t *testing.T) {
	db := testDB(t)
	s := newSchedule(t, db, "chatty", time.Now())
	big := strings.Repeat("H", 6000) + strings.Repeat("T", 6000)
	run, err := db.RecordScheduleRun(ScheduleRun{ScheduleID: s.ID, Status: ScheduleRunOK, Output: big})
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Output) > MaxScheduleOutput+64 {
		t.Errorf("output not cut: %d bytes", len(run.Output))
	}
	if !strings.HasPrefix(run.Output, "HHHH") || !strings.HasSuffix(run.Output, "TTTT") || !strings.Contains(run.Output, "bytes cut") {
		t.Errorf("cut should keep head and tail with a marker")
	}
}

func TestDeleteScheduleRefusesSystemRows(t *testing.T) {
	db := testDB(t)
	now := time.Now()
	sys, err := db.CreateSchedule(NewSchedule{Name: "heartbeat:x", CreatedBy: "gateway", Kind: ScheduleEvery, Spec: "30m", TZ: "UTC",
		PayloadKind: PayloadAgent, Payload: AgentPayload{Title: "hb"}, System: true, NextRunAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteSchedule(sys.ID); err != ErrSystemSchedule {
		t.Errorf("system delete: got %v", err)
	}
	user := newSchedule(t, db, "mine", now)
	db.RecordScheduleRun(ScheduleRun{ScheduleID: user.ID, Status: ScheduleRunOK})
	if err := db.DeleteSchedule(user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetSchedule(user.ID); err == nil {
		t.Error("deleted schedule still readable")
	}
	if runs, _ := db.ScheduleRuns(user.ID, 10); len(runs) != 0 {
		t.Error("runs should go with the schedule")
	}
	if found, err := db.FindSchedule("heartbeat:x"); err != nil || found.ID != sys.ID {
		t.Errorf("find by name: %v %v", found, err)
	}
}

func TestFireScheduleMovesClockOnlyWhenEnabled(t *testing.T) {
	db := testDB(t)
	now := time.Now()
	s := newSchedule(t, db, "later", now.Add(time.Hour))
	if due, _ := db.DueSchedules(now); len(due) != 0 {
		t.Fatal("should not be due yet")
	}
	if err := db.FireSchedule(s.ID, now); err != nil {
		t.Fatal(err)
	}
	if due, _ := db.DueSchedules(now); len(due) != 1 {
		t.Fatal("run-now should make it due")
	}
	db.SetScheduleEnabled(s.ID, false, time.Time{})
	if err := db.FireSchedule(s.ID, now); err == nil {
		t.Error("run-now on a disabled schedule must be refused")
	}
}

// A v3 database (P0) must gain the schedule tables and agents.scratch.
func TestMigrateV3DatabaseGainsSchedules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coord.db")
	raw, err := sql.Open("sqlite", storage.DSN(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL) STRICT`,
		`INSERT INTO meta VALUES ('schema_version', '3')`,
		`CREATE TABLE agents (
			id TEXT PRIMARY KEY, display_name TEXT NOT NULL DEFAULT '', provider TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '', ws_addr TEXT NOT NULL DEFAULT '', workspace TEXT NOT NULL DEFAULT '',
			started_at TEXT NOT NULL, last_seen_at TEXT NOT NULL
		) STRICT`,
		`INSERT INTO agents (id, started_at, last_seen_at) VALUES ('bomclaw', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	raw.Close()

	db, err := Open(path)
	if err != nil {
		t.Fatalf("open v3 db: %v", err)
	}
	defer db.Close()
	var version string
	db.conn.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&version)
	if version != "4" {
		t.Errorf("schema_version %s, want 4", version)
	}
	var n int
	db.conn.QueryRow(`SELECT count(*) FROM pragma_table_info('agents') WHERE name='scratch'`).Scan(&n)
	if n != 1 {
		t.Error("agents.scratch missing after migration")
	}
	if _, err := db.ListSchedules(); err != nil {
		t.Errorf("schedules table: %v", err)
	}
	if agents, err := db.ListAgents(); err != nil || len(agents) != 1 {
		t.Errorf("existing agents survive: %v %d", err, len(agents))
	}
}
