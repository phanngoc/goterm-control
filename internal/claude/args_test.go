package claude

import (
	"slices"
	"testing"
)

// The CLI subprocess must not inherit the operator's own interactive toolkit.
// ~/.claude/skills is shared with their hand-driven sessions, and some of it
// competes for this agent's work: a headless-browser skill advertising "open
// in browser" wins those requests over `bomclaw browser`, so the agent drives
// a logged-out browser and reports the site as inaccessible.
func TestBuildArgsIsolatesSkillsAndMCP(t *testing.T) {
	args := buildArgs("claude-opus-4-8", "sess-1", true, "system")

	for _, want := range []string{"--disable-slash-commands", "--strict-mcp-config"} {
		if !slices.Contains(args, want) {
			t.Errorf("%s missing — the subprocess would load the operator's %s", want, want)
		}
	}
}

// Isolation must hold on resumed turns too, not just the first one: every
// turn spawns a fresh subprocess, so a flag dropped on resume is a flag that
// only works once.
func TestBuildArgsIsolationSurvivesResume(t *testing.T) {
	resumed := buildArgs("claude-opus-4-8", "sess-1", false, "system")

	if !slices.Contains(resumed, "--disable-slash-commands") {
		t.Error("--disable-slash-commands missing on a resumed turn")
	}
	if !slices.Contains(resumed, "--resume") {
		t.Error("--resume missing on a resumed turn")
	}
	if slices.Contains(resumed, "--append-system-prompt") {
		t.Error("resumed turns must not re-send the system prompt (the CLI already has it)")
	}
}

func TestBuildArgsSystemPromptOnFirstTurnOnly(t *testing.T) {
	first := buildArgs("m", "s", true, "you are a test")
	i := slices.Index(first, "--append-system-prompt")
	if i < 0 || first[i+1] != "you are a test" {
		t.Fatalf("first turn should carry the system prompt, got %v", first)
	}
	if slices.Contains(first, "--resume") {
		t.Error("a new session must not resume")
	}
}
