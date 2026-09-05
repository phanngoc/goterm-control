package claude

import (
	"slices"
	"strings"
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
}

// The system prompt must go on EVERY turn. --append-system-prompt applies to
// the invocation, not the stored session: a session created with it and then
// resumed without it stops following the prompt entirely (measured against the
// CLI). Sending it only on the first message gave the agent its operating
// instructions for exactly one message per session.
func TestBuildArgsResendsSystemPromptOnResume(t *testing.T) {
	resumed := buildArgs("m", "sess-1", false, "you are a test")

	i := slices.Index(resumed, "--append-system-prompt")
	if i < 0 {
		t.Fatal("a resumed turn must carry the system prompt — the CLI does not remember it")
	}
	if resumed[i+1] != "you are a test" {
		t.Errorf("resumed prompt = %q, want the system prompt", resumed[i+1])
	}
	if !slices.Contains(resumed, "--resume") {
		t.Error("--resume missing")
	}
	// Once, not twice: a stacked flag would double the prefix every turn.
	if n := strings.Count(strings.Join(resumed, " "), "--append-system-prompt"); n != 1 {
		t.Errorf("--append-system-prompt appears %d times, want exactly 1", n)
	}
}

func TestBuildArgsOmitsEmptySystemPrompt(t *testing.T) {
	for _, isNew := range []bool{true, false} {
		args := buildArgs("m", "s", isNew, "")
		if slices.Contains(args, "--append-system-prompt") {
			t.Errorf("isNew=%v: an empty prompt must not be passed as a flag", isNew)
		}
	}
}

func TestBuildArgsFirstTurnCarriesPromptAndDoesNotResume(t *testing.T) {
	first := buildArgs("m", "s", true, "you are a test")
	i := slices.Index(first, "--append-system-prompt")
	if i < 0 || first[i+1] != "you are a test" {
		t.Fatalf("first turn should carry the system prompt, got %v", first)
	}
	if slices.Contains(first, "--resume") {
		t.Error("a new session must not resume")
	}
}
