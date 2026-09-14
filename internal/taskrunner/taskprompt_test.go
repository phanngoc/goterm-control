package taskrunner

import (
	"strings"
	"testing"
	"time"

	"github.com/ngocp/goterm-control/internal/coord"
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
	p := taskPrompt(task, 8*time.Minute, false, nil, nil, nil)

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
	p := taskPrompt(&coord.Task{ID: "t_real", Title: "x"}, time.Minute, false, nil, nil, nil)
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
	p := taskPrompt(&coord.Task{ID: "t_real", Title: "x"}, time.Minute, false, nil, nil, peers)
	if !strings.Contains(p, "bomclaw msg --to <agent> --task t_real") {
		t.Fatalf("the agent is told who its peers are and not how to write to them about this task:\n%s", p)
	}
}
