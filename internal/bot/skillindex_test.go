package bot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ngocp/goterm-control/internal/skills"
)

// All three backends go through one factory, so one wiring test covers claude,
// codex and opencode — which matters, because a skills mechanism that works on
// one of them is a mechanism two thirds of this machine's agents do not have.
func TestSkillIndexReadsTheAgentsWorkspace(t *testing.T) {
	ws := t.TempDir()
	dir := filepath.Join(ws, skills.Dir, "deploy")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, skills.File),
		[]byte("---\nname: deploy\ndescription: 'khi cần đẩy bản mới lên'\n---\nchi tiết\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := SkillIndex(ws, "")
	if !strings.Contains(got, "deploy") || !strings.Contains(got, "khi cần đẩy bản mới") {
		t.Fatalf("index does not carry the skill:\n%s", got)
	}
	if strings.Contains(got, "chi tiết") {
		t.Error("the body went into the prompt; only the description should — the body is\n" +
			"read on demand, which is the whole reason a toolkit of any size stays cheap")
	}
}

// An agent with no skills must add nothing at all, not an empty heading, and a
// broken folder must not take its turn down.
func TestSkillIndexIsSilentWhenThereIsNothingToSay(t *testing.T) {
	if got := SkillIndex(t.TempDir(), ""); got != "" {
		t.Errorf("got %q", got)
	}
	if got := SkillIndex("", ""); got != "" {
		t.Errorf("got %q for an agent with no workspace", got)
	}
}

// The index is a function, not a string captured at startup: attaching a skill
// has to take effect on the next turn.
func TestSkillIndexIsReReadEachTime(t *testing.T) {
	ws := t.TempDir()
	if SkillIndex(ws, "") != "" {
		t.Fatal("expected an empty index to start")
	}
	dir := filepath.Join(ws, skills.Dir, "new-one")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, skills.File),
		[]byte("---\nname: new-one\ndescription: d\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(SkillIndex(ws, ""), "new-one") {
		t.Fatal("a skill added after startup is invisible until a restart —\n" +
			"a toolkit you can only change by restarting the gateway is not one anybody edits")
	}
}

// Two roots, and they are different kinds of knowledge. An agent's skills are
// about this machine and follow it everywhere; a repo's are about that repo —
// how it deploys, what its migrations trip over — and must not travel into a
// turn on something else.
func TestATurnSeesTheRepoItIsWorkingIn(t *testing.T) {
	agent, repo := t.TempDir(), t.TempDir()
	writeSkill(t, filepath.Join(agent, skills.Dir, "browser"),
		"---\nname: browser\ndescription: 'của agent'\n---\n")
	writeSkill(t, filepath.Join(repo, skills.ProjectDir, "deploy"),
		"---\nname: deploy\ndescription: 'của repo này'\n---\n")

	// Inside the repo: both.
	both := SkillIndex(agent, repo)
	if !strings.Contains(both, "browser") || !strings.Contains(both, "deploy") {
		t.Fatalf("a turn in the repo does not see both sets:\n%s", both)
	}

	// Somewhere else: the repo's skills stay behind, or the agent is handed a
	// deploy procedure for something it is not touching.
	elsewhere := SkillIndex(agent, t.TempDir())
	if strings.Contains(elsewhere, "deploy") {
		t.Errorf("a repo's skills followed the agent out of the repo:\n%s", elsewhere)
	}
	if !strings.Contains(elsewhere, "browser") {
		t.Errorf("the agent lost its own skills:\n%s", elsewhere)
	}
}

// The project's way of doing a thing beats the general one — the only reason to
// have two roots at all.
func TestTheRepoWinsANameCollision(t *testing.T) {
	agent, repo := t.TempDir(), t.TempDir()
	writeSkill(t, filepath.Join(agent, skills.Dir, "review"),
		"---\nname: review\ndescription: 'bản chung'\n---\n")
	writeSkill(t, filepath.Join(repo, skills.ProjectDir, "review"),
		"---\nname: review\ndescription: 'bản của repo'\n---\n")

	got := SkillIndex(agent, repo)
	if !strings.Contains(got, "bản của repo") {
		t.Fatalf("the agent's general version won:\n%s", got)
	}
	if strings.Contains(got, "bản chung") {
		t.Errorf("both versions are listed — the agent sees one skill twice:\n%s", got)
	}
}

func writeSkill(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, skills.File), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
