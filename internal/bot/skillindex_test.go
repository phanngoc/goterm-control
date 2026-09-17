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

	got := SkillIndex(ws)
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
	if got := SkillIndex(t.TempDir()); got != "" {
		t.Errorf("got %q", got)
	}
	if got := SkillIndex(""); got != "" {
		t.Errorf("got %q for an agent with no workspace", got)
	}
}

// The index is a function, not a string captured at startup: attaching a skill
// has to take effect on the next turn.
func TestSkillIndexIsReReadEachTime(t *testing.T) {
	ws := t.TempDir()
	if SkillIndex(ws) != "" {
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
	if !strings.Contains(SkillIndex(ws), "new-one") {
		t.Fatal("a skill added after startup is invisible until a restart —\n" +
			"a toolkit you can only change by restarting the gateway is not one anybody edits")
	}
}
