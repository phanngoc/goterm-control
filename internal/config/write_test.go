package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A config shaped like the real ones: hand-edited, commented, with keys this
// edit must not touch.
const sampleConfig = `# BomClaw agent 3
agent:
  id: "bomclaw3"
  name: "BomClaw (agent 3)"   # shown in the tray

provider: "claude"

telegram:
  token: ""

claude:
  api_key: ""
  model: "claude-opus-5"
  workspace: "~/goterm-workspace-3"
  system_prompt: |
    Be terse.
    Never say "certainly".

models:
  default: "claude-opus-5"

tasks:
  auto_claim: true   # agent 3 runs unattended
`

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSetModelChangesTwoLinesAndNothingElse(t *testing.T) {
	path := write(t, sampleConfig)
	t.Setenv("TELEGRAM_TOKEN", "x")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-oat01-x")

	if err := SetModel(path, "gpt-6-astra"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	before := strings.Split(sampleConfig, "\n")
	after := strings.Split(string(got), "\n")
	if len(before) != len(after) {
		t.Fatalf("line count changed: %d → %d", len(before), len(after))
	}
	var changed []int
	for i := range before {
		if before[i] != after[i] {
			changed = append(changed, i)
		}
	}
	if len(changed) != 2 {
		t.Fatalf("expected exactly 2 changed lines, got %d: %v", len(changed), changed)
	}
	// Everything hand-maintained survived byte for byte.
	for _, keep := range []string{
		`  name: "BomClaw (agent 3)"   # shown in the tray`,
		`    Never say "certainly".`,
		`  auto_claim: true   # agent 3 runs unattended`,
		`# BomClaw agent 3`,
	} {
		if !strings.Contains(string(got), keep) {
			t.Errorf("the edit destroyed: %s", keep)
		}
	}
	if !strings.Contains(string(got), `provider: "codex"`) {
		t.Error("provider was not switched")
	}
	if !strings.Contains(string(got), `default: "gpt-6-astra"`) {
		t.Error("model was not switched")
	}
}

// The provider is derived, never asked for: the model's API is what actually
// selects the backend, so the two cannot be set to disagree.
func TestProviderFollowsTheModel(t *testing.T) {
	t.Setenv("TELEGRAM_TOKEN", "x")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-oat01-x")
	path := write(t, strings.Replace(sampleConfig, `provider: "claude"`, `provider: "codex"`, 1))

	if err := SetModel(path, "claude-opus-5"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != ProviderClaude {
		t.Fatalf("provider did not follow the model: %q", cfg.Provider)
	}
}

// Writing a config the binary refuses at startup is how an agent disappears
// until somebody reads a log. The check happens before the file is replaced.
func TestAConfigThatWouldNotStartIsNotWritten(t *testing.T) {
	t.Setenv("TELEGRAM_TOKEN", "x")
	path := write(t, sampleConfig)
	original, _ := os.ReadFile(path)

	err := SetModel(path, "no-such-model")
	if err == nil {
		t.Fatal("an unknown model was accepted")
	}
	if !strings.Contains(err.Error(), "unknown model") {
		t.Errorf("error should name the problem: %v", err)
	}
	now, _ := os.ReadFile(path)
	if string(now) != string(original) {
		t.Fatal("the file was modified despite the failure")
	}
}

// A shape this cannot edit safely must be refused, not guessed at.
func TestRefusesAFileItCannotEditSafely(t *testing.T) {
	t.Setenv("TELEGRAM_TOKEN", "x")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-oat01-x")
	two := sampleConfig + "\nprovider: \"codex\"\n"
	path := write(t, two)
	if err := SetModel(path, "gpt-6-astra"); err == nil {
		t.Fatal("two provider lines were edited anyway")
	}
}

func TestKnownModelsOnlyListsBackendsThatExist(t *testing.T) {
	cfg := &Config{Provider: ProviderClaude}
	cfg.Claude.Model = "claude-opus-5"
	cfg.Models.Default = "claude-opus-5"
	choices := cfg.KnownModels()
	if len(choices) == 0 {
		t.Fatal("no models offered at all")
	}
	seen := map[string]bool{}
	for _, c := range choices {
		if c.Provider == "" {
			t.Errorf("%s has no provider key", c.ID)
		}
		seen[c.Provider] = true
	}
	if !seen[ProviderClaude] || !seen[ProviderCodex] {
		t.Errorf("expected both shipped backends to be offered, got %v", seen)
	}
}
