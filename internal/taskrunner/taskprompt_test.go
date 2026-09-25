package taskrunner

import (
	"strings"
	"testing"
	"time"

	"github.com/ngocp/goterm-control/internal/coord"
	"github.com/ngocp/goterm-control/internal/skills"
)

// The prompt is one Sprintf with a dozen arguments, and every one of them is
// the task's own id inside a command the agent is about to run. Miscount them
// and the agent is told to file its work under some other task, or reads
// "%!s(MISSING)" and improvises — neither of which shows up until an agent has
// already acted on it.
func TestTaskPromptCarriesTheRightIDInEveryCommand(t *testing.T) {
	task := &coord.Task{
		ID: "t_real", Title: "visualize 3D", Body: "làm viewer",
		Depth: 1, ContextID: "ctx_1",
	}
	p := taskPrompt(task, 8*time.Minute, false, nil, nil, nil, "", "", false)

	if strings.Contains(p, "%!") || strings.Contains(p, "MISSING") || strings.Contains(p, "EXTRA") {
		t.Fatalf("the prompt has a formatting error in it:\n%s", p)
	}

	// Every command the prompt hands the agent has to name THIS task.
	for _, cmd := range []string{
		"bomclaw task progress --id t_real",
		"bomclaw task sub --parent t_real",
		"bomclaw task block --id t_real --on children",
		"bomclaw task done --id t_real",
		"bomclaw task block --id t_real --on human",
		"bomclaw artifact put --task t_real",
	} {
		if !strings.Contains(p, cmd) {
			t.Errorf("prompt never says %q — the agent cannot do that thing to its own task", cmd)
		}
	}
}

// Output filed as an artifact is the difference between work that can be found
// later and a path in a sentence. The prompt has to ask for it, because the
// command existing was not enough: a whole 3D viewer was handed over as a
// localhost URL in prose, and the task's own output panel was empty.
func TestTaskPromptAsksForTheOutputToBeFiled(t *testing.T) {
	p := taskPrompt(&coord.Task{ID: "t_real", Title: "x"}, time.Minute, false, nil, nil, nil, "", "", false)
	if !strings.Contains(p, "artifact put") {
		t.Fatal("nothing tells the agent to file what it produced")
	}
	if !strings.Contains(p, "--kind link") {
		t.Error("no way offered for output that is a URL rather than a file — which is what\n" +
			"a running viewer is, and prose is where it ended up last time")
	}
}

// A peer named in the prompt is a peer the agent may write to, and a message
// about this work has to be filed against it or the exchange is lost.
func TestTaskPromptAsksForMessagesToBeFiledUnderTheTask(t *testing.T) {
	peers := []coord.Agent{{ID: "bomclaw3", Provider: "opencode", Online: true}}
	p := taskPrompt(&coord.Task{ID: "t_real", Title: "x"}, time.Minute, false, nil, nil, peers, "", "", false)
	if !strings.Contains(p, "bomclaw msg --to <agent> --task t_real") {
		t.Fatalf("the agent is told who its peers are and not how to write to them about this task:\n%s", p)
	}
}

// An agent choosing who to hand a piece of work to used to get three names,
// three backend labels, and the sentence "different backends, so different
// strengths" — which named no strength. It chose by guessing, and did: a 3D
// task went to two peers on no basis beyond there being two peers.
func TestTaskPromptSaysWhatEachPeerIsGoodAt(t *testing.T) {
	ws := t.TempDir()
	if err := skills.Install(ws, "geospatial",
		[]byte("---\nname: geospatial\ndescription: 'Dữ liệu bản đồ và GeoJSON.'\n---\nthân\n")); err != nil {
		t.Fatal(err)
	}
	peers := []coord.Agent{{
		ID: "bomclaw3", Provider: "opencode", Model: "muse", Online: true, Workspace: ws,
	}}
	p := taskPrompt(&coord.Task{ID: "t_real", Title: "x"}, time.Minute, false, nil, nil, peers, "", "", false)

	if !strings.Contains(p, "bomclaw3") {
		t.Fatal("the peer is not named at all")
	}
	if !strings.Contains(p, "geospatial") || !strings.Contains(p, "Dữ liệu bản đồ") {
		t.Fatalf("the peer is named with no idea what it is for:\n%s", p)
	}
	if strings.Contains(p, "different strengths and different costs") {
		t.Error("still carries the sentence that promised strengths and named none")
	}
}

// An agent that does not know the folder is shared will not look in it for a
// peer's work, and will describe a path in a message instead of just leaving
// the file there.
func TestTaskPromptSaysTheFolderIsSharedWhenItIs(t *testing.T) {
	task := &coord.Task{ID: "t_real", Title: "dựng landing page", ContextID: "ctx_1"}

	p := taskPrompt(task, time.Minute, false, nil, nil, nil, "/shared/runs/ctx_1", "/shared/runs/ctx_1", true)
	if !strings.Contains(p, "/shared/runs/ctx_1") {
		t.Fatalf("the agent is never told where it is standing:\n%s", p)
	}
	if !strings.Contains(p, "shared by every task in this context") {
		t.Errorf("the folder is shared and the prompt does not say so:\n%s", p)
	}

	// A project folder is not the context's scratch and must not be described
	// as something that ages out.
	p = taskPrompt(task, time.Minute, false, nil, nil, nil, "/projects/trading", "/shared/runs/ctx_1", false)
	if !strings.Contains(p, "this project's folder") {
		t.Errorf("a project run does not name its project folder:\n%s", p)
	}
	if strings.Contains(p, "shared by every task in this context") {
		t.Error("a project folder was described as context scratch")
	}

	// No workspace at all (the agent's own) must not invent a line about one.
	p = taskPrompt(task, time.Minute, false, nil, nil, nil, "", "", false)
	if strings.Contains(p, "Working directory:") {
		t.Errorf("claimed a working directory there is none of:\n%s", p)
	}
}

// A goal stands in its project while its children stand in scratch, so it has
// to be told where they are. Without this line it looks around the project
// folder, sees none of their work, and concludes they did nothing.
func TestAGoalIsToldWhereItsChildrenWork(t *testing.T) {
	task := &coord.Task{ID: "t_goal", Title: "backtest", ContextID: "ctx_1"}
	p := taskPrompt(task, time.Minute, false, nil, nil, nil,
		"/projects/trading", "/shared/runs/ctx_1", false)

	if !strings.Contains(p, "/projects/trading") {
		t.Fatalf("the goal is not told where to assemble:\n%s", p)
	}
	if !strings.Contains(p, "/shared/runs/ctx_1") {
		t.Fatalf("the goal is not told where its children work, so it will find nothing:\n%s", p)
	}

	// A child is in that scratch itself and must not be sent looking elsewhere.
	p = taskPrompt(task, time.Minute, false, nil, nil, nil,
		"/shared/runs/ctx_1", "/shared/runs/ctx_1", true)
	if strings.Contains(p, "assemble the deliverable here") {
		t.Errorf("a child was told to assemble the deliverable in scratch:\n%s", p)
	}
	if !strings.Contains(p, "shared by every task in this context") {
		t.Errorf("a child was not told the folder is shared:\n%s", p)
	}
}

// The bar was stored, printed in two places, and never shown to the one agent
// whose work is measured against it — only to its parent, about its children.
// Nobody was asked for criteria, so nobody wrote any: not one task in the first
// eighty-nine had them.
func TestAcceptanceReachesTheAgentRunningTheTask(t *testing.T) {
	task := &coord.Task{
		ID: "t_goal", Title: "viewer v2", ContextID: "ctx_1",
		Acceptance: "1) tải dưới 2s; 2) lọc theo quận; 3) có test",
	}
	p := taskPrompt(task, time.Minute, false, nil, nil, nil, "/w", "/w", true)

	if !strings.Contains(p, "lọc theo quận") {
		t.Fatalf("the agent cannot see the bar its work is judged against:\n%s", p)
	}
	if !strings.Contains(p, "answer each of them in turn") {
		t.Errorf("the criteria are shown but never asked for back:\n%s", p)
	}

	// A task with no criteria must not grow a paragraph about criteria.
	plain := taskPrompt(&coord.Task{ID: "t_x", Title: "việc nhỏ"}, time.Minute, false, nil, nil, nil, "", "", false)
	if strings.Contains(plain, "acceptance criteria") {
		t.Errorf("a task with no bar was lectured about one:\n%s", plain)
	}
}

// A goal that arrived without a bar: the first thing the agent doing it is
// asked for is what done means — in the same run, before the work.
func TestAGoalWithNoCriteriaIsAskedToWriteThem(t *testing.T) {
	goal := &coord.Task{ID: "t_goal", Title: "việc lớn", ContextID: "ctx_1"}
	p := taskPrompt(goal, time.Minute, false, nil, nil, nil, "/projects/p", "/scratch", false)
	if !strings.Contains(p, "task accept") {
		t.Fatalf("a goal with no definition of done was not asked for one:\n%s", p)
	}

	// A child is not a goal: its bar is its parent's problem.
	child := &coord.Task{ID: "t_child", ParentID: "t_goal", Title: "mảnh"}
	if p := taskPrompt(child, time.Minute, false, nil, nil, nil, "/scratch", "/scratch", true); strings.Contains(p, "task accept") {
		t.Error("a sub-task was asked to invent its own bar")
	}
	// Neither is a verification: it is measuring, not being measured.
	v := &coord.Task{ID: "t_v", Kind: coord.KindVerify, Title: "Verify: x"}
	if p := taskPrompt(v, time.Minute, false, nil, nil, nil, "/projects/p", "/scratch", false); strings.Contains(p, "task accept") {
		t.Error("a verification was asked to write criteria for itself")
	}
	// And one that already has a bar is shown it, not asked for another.
	with := &coord.Task{ID: "t_g2", Title: "việc", Acceptance: "1) chạy được"}
	p = taskPrompt(with, time.Minute, false, nil, nil, nil, "/projects/p", "/scratch", false)
	if strings.Contains(p, "task accept") || !strings.Contains(p, "chạy được") {
		t.Error("a goal that already has criteria was asked for them again")
	}
}
