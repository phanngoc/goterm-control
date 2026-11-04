package coord

import (
	"math"
	"testing"
	"time"
)

func llmRun(t *testing.T, db *DB, id, agent, trace string, in, out int, u Usage) {
	t.Helper()
	now := time.Now()
	if err := db.InsertRun(&Run{ID: id, TraceID: trace, DottedOrder: DottedOrderSegment(now, id),
		AgentID: agent, Name: "claude", RunType: RunTypeLLM, StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := db.EndRun(id, now, "", "", in, out); err != nil {
		t.Fatal(err)
	}
	if err := db.SetRunUsage(id, u); err != nil {
		t.Fatal(err)
	}
}

// A fixed judge grades everyone's work — except its own, which a peer reads.
// One ruler is what makes scores comparable across agents.
func TestTheJudgeGradesEveryoneButItself(t *testing.T) {
	db := verifyDB(t)
	if err := db.SetJudge("bomclaw3"); err != nil {
		t.Fatal(err)
	}
	g := finishedGoal(t, db, "bomclaw", "có test")
	v, err := db.OpenVerification(*g, peers(t, db))
	if err != nil || v == nil || v.AssignedTo != "bomclaw3" {
		t.Fatalf("judge not chosen: %+v %v", v, err)
	}
	// Close that reading first, or bomclaw3's next claim would take it.
	c, err := db.ClaimTask("bomclaw3")
	if err != nil || c.ID != v.ID {
		t.Fatalf("judge claimed %+v %v", c, err)
	}
	if err := db.SetScore(v.ID, 9); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishTask(v.ID, "bomclaw3", TaskCompleted, "pass", c.Attempts); err != nil {
		t.Fatal(err)
	}
	own := finishedGoal(t, db, "bomclaw3", "có test")
	if own.ClaimedBy != "bomclaw3" {
		t.Fatalf("setup: goal carried by %q", own.ClaimedBy)
	}
	v2, err := db.OpenVerification(*own, peers(t, db))
	if err != nil || v2 == nil || v2.AssignedTo == "bomclaw3" || v2.AssignedTo == "" {
		t.Fatalf("the judge was asked to grade its own work: %+v %v", v2, err)
	}
	if err := db.SetScore(v.ID, 11); err == nil {
		t.Error("a score of 11 was accepted")
	}
	if err := db.SetScore(g.ID, 7); err == nil {
		t.Error("a goal that is not a verification was scored")
	}
}

func TestEconomicsAddsUpBurnOutcomesAndGrades(t *testing.T) {
	db := verifyDB(t)
	if err := db.SetJudge("bomclaw3"); err != nil {
		t.Fatal(err)
	}
	// bomclaw finishes a goal; its run cost $1.50 and is task work.
	g := finishedGoal(t, db, "bomclaw", "có test")
	if _, err := db.StartRun(g.ID, "bomclaw", 1, "tr_task"); err != nil {
		t.Fatal(err)
	}
	llmRun(t, db, "r1", "bomclaw", "tr_task", 100, 2000, Usage{CacheRead: 50000, CacheWrite: 3000, CostUSD: 1.5})
	// …and chats for $0.25.
	llmRun(t, db, "r2", "bomclaw", "tr_chat", 10, 300, Usage{CacheRead: 9000, CostUSD: 0.25})

	// The judge reads it and passes it with an 8.
	v, err := db.OpenVerification(*g, peers(t, db))
	if err != nil {
		t.Fatal(err)
	}
	c, err := db.ClaimTask("bomclaw3")
	if err != nil || c.ID != v.ID {
		t.Fatalf("judge claimed %+v %v", c, err)
	}
	if err := db.SetScore(v.ID, 8); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishTask(v.ID, "bomclaw3", TaskCompleted, "pass", c.Attempts); err != nil {
		t.Fatal(err)
	}

	e, err := db.EconomicsSince(time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var me, judge *AgentEconomics
	for i := range e.Agents {
		switch e.Agents[i].AgentID {
		case "bomclaw":
			me = &e.Agents[i]
		case "bomclaw3":
			judge = &e.Agents[i]
		}
	}
	if me == nil || judge == nil {
		t.Fatalf("agents missing: %+v", e.Agents)
	}
	if me.Turns != 2 || math.Abs(me.CostUSD-1.75) > 1e-9 || math.Abs(me.TaskCostUSD-1.5) > 1e-9 {
		t.Errorf("burn: %+v", me)
	}
	if me.Tokens() != 100+2000+50000+3000+10+300+9000 {
		t.Errorf("tokens = %d", me.Tokens())
	}
	if me.Completed != 1 || me.CompletionRate() != 1 || math.Abs(me.CostPerCompleted()-1.5) > 1e-9 {
		t.Errorf("outcomes: %+v", me)
	}
	if me.Verified != 1 || me.Passed != 1 || me.MeanScore != 8 {
		t.Errorf("grade: %+v", me)
	}
	if judge.Judged != 1 || judge.Completed != 0 {
		t.Errorf("a verification counted as the judge's own work: %+v", judge)
	}
	if e.Judge != "bomclaw3" {
		t.Errorf("judge = %q", e.Judge)
	}
}

func runAt(t *testing.T, db *DB, id, agent, trace string, at time.Time, cost float64) {
	t.Helper()
	if err := db.InsertRun(&Run{ID: id, TraceID: trace, DottedOrder: DottedOrderSegment(at, id),
		AgentID: agent, Name: "claude", RunType: RunTypeLLM, StartedAt: at}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetRunUsage(id, Usage{CostUSD: cost}); err != nil {
		t.Fatal(err)
	}
}

// The four ways the first version misled, each pinned: a handover moved cost
// and credit apart, the two halves used different windows, open work leaked
// into $ per finished task, and a task nobody could claim again counted as
// merely open.
func TestEconomicsCreditsATaskWholeToWhoeverFinishedIt(t *testing.T) {
	db := verifyDB(t)
	since := time.Now().Add(-time.Hour)

	// bomclaw tries and fails (its run started BEFORE the window), bomclaw2
	// finishes. The task's whole $3 belongs to the finished task.
	task, err := db.CreateTask(NewTask{Title: "handover", CreatedBy: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.StartRun(task.ID, "bomclaw", 1, "tr_a"); err != nil {
		t.Fatal(err)
	}
	db.conn.Exec(`UPDATE task_runs SET liveness = 'failed', started_at = ? WHERE trace_id = 'tr_a'`, ts(time.Now()))
	runAt(t, db, "ra", "bomclaw", "tr_a", since.Add(-time.Hour), 2.0)
	if _, err := db.StartRun(task.ID, "bomclaw2", 2, "tr_b"); err != nil {
		t.Fatal(err)
	}
	runAt(t, db, "rb", "bomclaw2", "tr_b", time.Now(), 1.0)
	db.conn.Exec(`UPDATE tasks SET state = ?, claimed_by = 'bomclaw2', updated_at = ? WHERE id = ?`, TaskCompleted, ts(time.Now()), task.ID)

	// bomclaw2 also has an open task that cost $5 so far: not a finished task.
	open, _ := db.CreateTask(NewTask{Title: "open", CreatedBy: "owner", AssignedTo: "bomclaw2"})
	db.StartRun(open.ID, "bomclaw2", 1, "tr_o")
	runAt(t, db, "ro", "bomclaw2", "tr_o", time.Now(), 5.0)

	// bomclaw3 has a task with every attempt used: stuck, counted as a miss.
	stuck, _ := db.CreateTask(NewTask{Title: "stuck", CreatedBy: "owner", AssignedTo: "bomclaw3"})
	db.conn.Exec(`UPDATE tasks SET attempts = max_attempts WHERE id = ?`, stuck.ID)

	e, err := db.EconomicsSince(since)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]AgentEconomics{}
	for _, a := range e.Agents {
		got[a.AgentID] = a
	}
	two := got["bomclaw2"]
	if two.Completed != 1 || math.Abs(two.DoneCostUSD-3.0) > 1e-9 || math.Abs(two.CostPerCompleted()-3.0) > 1e-9 {
		t.Errorf("finished task not costed whole (handover + pre-window run): %+v", two)
	}
	if two.Open != 1 {
		t.Errorf("open task: %+v", two)
	}
	if got["bomclaw"].FailedAttempts != 1 || got["bomclaw"].Completed != 0 {
		t.Errorf("the failed attempt before the handover vanished: %+v", got["bomclaw"])
	}
	if s := got["bomclaw3"]; s.Stuck != 1 || s.Open != 0 || s.CompletionRate() != 0 {
		t.Errorf("an unclaimable task counted as open: %+v", s)
	}
}
