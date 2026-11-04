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
