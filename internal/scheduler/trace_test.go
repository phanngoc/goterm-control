package scheduler

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ngocp/goterm-control/internal/coord"
	"github.com/ngocp/goterm-control/internal/trace"
)

// A shell command fired at three in the morning used to leave nothing but
// schedule_runs.output, cut at 8KB: no waterfall, nothing to filter in the
// Traces tab, and no way to line it up against whatever ran before it.
func TestACommandScheduleLeavesATrace(t *testing.T) {
	db := openDB(t)
	rec := trace.New(db, "bomclaw")
	defer rec.Close()

	s := New(db, Config{AgentID: "bomclaw", CommandTimeout: 10 * time.Second})
	s.SetRecorder(rec)

	sc, err := db.CreateSchedule(coord.NewSchedule{
		Name: "nightly", CreatedBy: "bomclaw", Kind: coord.ScheduleEvery, Spec: "1h", TZ: "UTC",
		PayloadKind: coord.PayloadCommand, Payload: coord.CommandPayload{Cmd: cmdEchoRAN},
		NextRunAt: time.Now().Add(-time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}

	s.fireCommand(context.Background(), sc, time.Now())
	rec.Close() // spans are written asynchronously

	traces, err := db.ListTraces(coord.TraceFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(traces) != 1 {
		t.Fatalf("got %d traces, want one for the command that ran", len(traces))
	}
	tr := traces[0]
	if tr.RunType != coord.RunTypeCommand {
		t.Errorf("run_type = %q, want %q", tr.RunType, coord.RunTypeCommand)
	}
	if !strings.Contains(tr.Inputs, "RAN") {
		t.Errorf("the trace does not say what was run: %q", tr.Inputs)
	}
	if !strings.Contains(tr.Outputs, "RAN") {
		t.Errorf("the trace does not carry the output: %q", tr.Outputs)
	}
	if !strings.Contains(tr.Tags, sc.ID) {
		t.Errorf("the trace is not tagged with its schedule, so it cannot be found\n"+
			"from the schedule that caused it: %q", tr.Tags)
	}
	if tr.Status != "success" {
		t.Errorf("status = %q", tr.Status)
	}
}

// "It failed" and "it exited 2" are different amounts of help at 03:00.
func TestAFailedCommandCarriesItsExitCode(t *testing.T) {
	db := openDB(t)
	rec := trace.New(db, "bomclaw")

	s := New(db, Config{AgentID: "bomclaw", CommandTimeout: 10 * time.Second})
	s.SetRecorder(rec)

	sc, err := db.CreateSchedule(coord.NewSchedule{
		Name: "breaks", CreatedBy: "bomclaw", Kind: coord.ScheduleEvery, Spec: "1h", TZ: "UTC",
		PayloadKind: coord.PayloadCommand, Payload: coord.CommandPayload{Cmd: cmdFailWithStderr},
		NextRunAt: time.Now().Add(-time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	s.fireCommand(context.Background(), sc, time.Now())
	rec.Close()

	traces, err := db.ListTraces(coord.TraceFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(traces) != 1 {
		t.Fatalf("got %d traces", len(traces))
	}
	if traces[0].Status != "error" {
		t.Errorf("status = %q, want error", traces[0].Status)
	}
	if !strings.Contains(traces[0].Error, "3") {
		t.Errorf("error = %q — the exit code is the whole diagnosis", traces[0].Error)
	}
}

// A scheduler with no recorder is a supported configuration, and this path runs
// unattended at night. It must not panic.
func TestACommandScheduleWithoutARecorderStillRuns(t *testing.T) {
	db := openDB(t)
	s := New(db, Config{AgentID: "bomclaw", CommandTimeout: 10 * time.Second})

	sc, err := db.CreateSchedule(coord.NewSchedule{
		Name: "quiet", CreatedBy: "bomclaw", Kind: coord.ScheduleEvery, Spec: "1h", TZ: "UTC",
		PayloadKind: coord.PayloadCommand, Payload: coord.CommandPayload{Cmd: cmdSucceed},
		NextRunAt: time.Now().Add(-time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	s.fireCommand(context.Background(), sc, time.Now())

	runs, err := db.ScheduleRuns(sc.ID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Status != coord.ScheduleRunOK {
		t.Fatalf("the command did not run: %+v", runs)
	}
}

// An `agent` schedule produces an ordinary task, and that task's run opens a
// `task` trace of its own. A span here as well would be the same firing
// recorded twice, in two places that disagree the first time one changes.
func TestAnAgentScheduleOpensNoTraceOfItsOwn(t *testing.T) {
	db := openDB(t)
	rec := trace.New(db, "bomclaw")

	s := New(db, Config{AgentID: "bomclaw"})
	s.SetRecorder(rec)

	sc, err := db.CreateSchedule(coord.NewSchedule{
		Name: "daily-report", CreatedBy: "bomclaw", Kind: coord.ScheduleEvery, Spec: "1h", TZ: "UTC",
		PayloadKind: coord.PayloadAgent,
		Payload:     coord.AgentPayload{Title: "báo cáo", Body: "tổng hợp hôm nay"},
		NextRunAt:   time.Now().Add(-time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	s.fireAgent(sc, time.Now())
	rec.Close()

	traces, err := db.ListTraces(coord.TraceFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(traces) != 0 {
		t.Fatalf("an agent schedule opened %d trace(s) before the task even ran —\n"+
			"the task's own run will open one, and two records of one firing disagree\n"+
			"the first time either changes: %+v", len(traces), traces)
	}
}
