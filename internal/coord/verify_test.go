package coord

import (
	"strings"
	"testing"
	"time"
)

func verifyDB(t *testing.T) *DB {
	t.Helper()
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw", "bomclaw2", "bomclaw3")
	return db
}

// finishedGoal is a root task with criteria that an agent declared done.
func finishedGoal(t *testing.T, db *DB, by, acceptance string) *Task {
	t.Helper()
	g, err := db.CreateTask(NewTask{
		Title: "viewer v2", Body: "dựng viewer", CreatedBy: "owner", Acceptance: acceptance,
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := db.ClaimTask(by)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.FinishTask(c.ID, by, TaskCompleted, "xong rồi", c.Attempts); err != nil {
		t.Fatal(err)
	}
	g, err = db.GetTask(g.ID)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func peers(t *testing.T, db *DB) []Agent {
	t.Helper()
	list, err := db.ListAgents()
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// A goal with criteria gets read by somebody who did not write it. The whole
// value of the check is the distance between the checker and the work — the
// same model reconsidering its own answer is worth nothing.
func TestAFinishedGoalIsReadByAPeer(t *testing.T) {
	db := verifyDB(t)
	goal := finishedGoal(t, db, "bomclaw", "1) tải dưới 2s; 2) có test")

	pending, err := db.NeedsVerification(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != goal.ID {
		t.Fatalf("the goal is not queued for reading: %+v", pending)
	}

	v, err := db.OpenVerification(pending[0], peers(t, db))
	if err != nil || v == nil {
		t.Fatalf("no verification opened: %+v %v", v, err)
	}
	if v.AssignedTo == "bomclaw" {
		t.Fatal("the agent that did the work was asked to check it — that is the same model\n" +
			"with the same context reconsidering its own answer")
	}
	if v.Kind != KindVerify || v.ParentID != goal.ID || v.ContextID != goal.ContextID {
		t.Errorf("the verification is not attached to its goal: %+v", v)
	}
	if !strings.Contains(v.Body, "tải dưới 2s") {
		t.Error("the checker was not given the criteria it is meant to check")
	}

	// Once opened, it is not opened again.
	again, err := db.NeedsVerification(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("the same goal was queued for a second reading: %+v", again)
	}
}

// A goal with no criteria has nothing to be read against, and a child's bar
// belongs to its parent, which already saw it when it woke.
func TestOnlyGoalsWithCriteriaAreRead(t *testing.T) {
	db := verifyDB(t)
	finishedGoal(t, db, "bomclaw", "")

	pending, err := db.NeedsVerification(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("a goal with no criteria was queued for reading: %+v", pending)
	}
}

// Nobody else to ask is a reason to leave the goal plainly unverified, not to
// stamp it with a check worth nothing.
func TestAGoalIsNotCheckedByTheAgentThatDidIt(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw")
	goal := finishedGoal(t, db, "bomclaw", "phải có test")

	v, err := db.OpenVerification(*goal, peers(t, db))
	if err != nil {
		t.Fatal(err)
	}
	if v != nil {
		t.Fatalf("with one agent on the machine it checked its own work: %+v", v)
	}
}

// The heart of it: a rejection must not touch the goal. Terminal states are
// immutable here — that came from A2A with the state names — so the tree gains
// a new open task instead, which is A2A's own rule for this situation.
func TestARejectionOpensNewWorkAndLeavesTheGoalFinished(t *testing.T) {
	db := verifyDB(t)
	goal := finishedGoal(t, db, "bomclaw", "1) tải dưới 2s; 2) có test")
	v, err := db.OpenVerification(*goal, peers(t, db))
	if err != nil || v == nil {
		t.Fatalf("verification: %+v %v", v, err)
	}

	// The checker reads it and says no.
	c, err := db.ClaimTask(v.AssignedTo)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.FinishTask(c.ID, v.AssignedTo, TaskRejected,
		"tiêu chí 2 chưa đạt: không có test nào cho phần lọc", c.Attempts); err != nil {
		t.Fatal(err)
	}

	rejected, err := db.RejectedVerifications(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rejected) != 1 {
		t.Fatalf("the rejection was not picked up: %+v", rejected)
	}
	follow, err := db.FollowUpRejection(rejected[0], time.Now())
	if err != nil || follow == nil {
		t.Fatalf("no follow-up work: %+v %v", follow, err)
	}

	// The goal is untouched.
	after, err := db.GetTask(goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != TaskCompleted {
		t.Fatalf("the rejection moved the goal out of a terminal state (%s) — nothing else in\n"+
			"this package does that, and the immutability is deliberate", after.State)
	}

	// But the TREE is no longer finished, which is the true statement.
	p, err := db.ContextProgress(goal.ContextID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Done() {
		t.Fatal("the tree still reports itself finished although its goal was rejected")
	}
	if follow.AssignedTo != "bomclaw" {
		t.Errorf("the rework went to %q, not the agent that did the original", follow.AssignedTo)
	}
	if follow.ContextID != goal.ContextID {
		t.Error("the rework landed outside the goal's tree, so its budget does not apply")
	}
	if !strings.Contains(follow.Body, "không có test nào") {
		t.Error("the rework does not carry the checker's words, so it has to guess what was wrong")
	}
	if follow.Acceptance != goal.Acceptance {
		t.Error("the rework lost the criteria it will be judged against")
	}
}

// Three gateways run this sweep. The loser must not open a second copy of the
// same work.
func TestOneRejectionOpensOneFollowUp(t *testing.T) {
	db := verifyDB(t)
	goal := finishedGoal(t, db, "bomclaw", "phải có test")
	v, err := db.OpenVerification(*goal, peers(t, db))
	if err != nil || v == nil {
		t.Fatal(err)
	}
	c, _ := db.ClaimTask(v.AssignedTo)
	if err := db.FinishTask(c.ID, v.AssignedTo, TaskRejected, "thiếu test", c.Attempts); err != nil {
		t.Fatal(err)
	}
	rejected, _ := db.RejectedVerifications(10)

	first, err := db.FollowUpRejection(rejected[0], time.Now())
	if err != nil || first == nil {
		t.Fatalf("first: %+v %v", first, err)
	}
	second, err := db.FollowUpRejection(rejected[0], time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if second != nil {
		t.Fatalf("a second sweep opened a duplicate rework: %s and %s", first.ID, second.ID)
	}
	left, err := db.RejectedVerifications(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("the handled rejection is still queued: %+v", left)
	}
}

// A passed reading changes nothing and opens nothing.
func TestAPassLeavesEverythingAlone(t *testing.T) {
	db := verifyDB(t)
	goal := finishedGoal(t, db, "bomclaw", "phải có test")
	v, err := db.OpenVerification(*goal, peers(t, db))
	if err != nil || v == nil {
		t.Fatal(err)
	}
	c, _ := db.ClaimTask(v.AssignedTo)
	if err := db.FinishTask(c.ID, v.AssignedTo, TaskCompleted, "pass: đã chạy test", c.Attempts); err != nil {
		t.Fatal(err)
	}
	rejected, err := db.RejectedVerifications(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rejected) != 0 {
		t.Fatalf("a pass was treated as a rejection: %+v", rejected)
	}
	p, err := db.ContextProgress(goal.ContextID)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Done() {
		t.Error("a verified goal left the tree looking unfinished")
	}
}

// And the verification itself is never verified, or the system would spend a
// turn checking its own checking, forever.
func TestAVerificationIsNotItselfVerified(t *testing.T) {
	db := verifyDB(t)
	goal := finishedGoal(t, db, "bomclaw", "phải có test")
	v, err := db.OpenVerification(*goal, peers(t, db))
	if err != nil || v == nil {
		t.Fatal(err)
	}
	c, _ := db.ClaimTask(v.AssignedTo)
	if err := db.FinishTask(c.ID, v.AssignedTo, TaskCompleted, "pass", c.Attempts); err != nil {
		t.Fatal(err)
	}
	pending, err := db.NeedsVerification(10)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pending {
		if p.Kind == KindVerify {
			t.Fatalf("a verification was queued to be verified: %s", p.ID)
		}
	}
}
