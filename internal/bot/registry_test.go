package bot

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ngocp/goterm-control/internal/config"
	"github.com/ngocp/goterm-control/internal/models"
)

func cfgFor(provider, model string) *config.Config {
	c := &config.Config{Provider: provider}
	c.Claude.Model = model
	c.Claude.SystemPrompt = "be brief"
	c.Claude.Workspace = "/tmp/ws"
	return c
}

// TestTheRunningConfigStillResolvesToClaude is the characterization test the
// registry needed: this is what the machine runs today, and a refactor that
// quietly swapped the backend under it would look exactly like a refactor that
// worked.
func TestTheRunningConfigStillResolvesToClaude(t *testing.T) {
	c := NewChatClientWithPool(cfgFor(config.ProviderClaude, "claude-opus-5"), nil, nil)
	if got := c.Name(); got != "claude" {
		t.Fatalf("the claude config resolved to %q", got)
	}
}

// And agent 2, which runs codex.
func TestTheCodexConfigStillResolvesToCodex(t *testing.T) {
	c := NewChatClientWithPool(cfgFor(config.ProviderCodex, "gpt-6-astra"), nil, nil)
	if got := c.Name(); got != "codex" {
		t.Fatalf("the codex config resolved to %q", got)
	}
}

// TestAnUnknownBackendIsLoudNotClaude: the old code's else branch made every
// unrecognised provider run claude, so an agent could report one backend and
// run another. Failing to start is the better outcome.
func TestAnUnknownBackendIsLoudNotClaude(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("an unknown model api fell back to a backend instead of failing")
		}
		if !strings.Contains(strings.ToLower(err(r)), "no backend") {
			t.Fatalf("panic should say what is missing, got: %v", r)
		}
	}()
	c := cfgFor("hermes", "hermes-1")
	c.Models.Custom = []models.Model{{ID: "hermes-1", Name: "Hermes", API: "hermes-cli"}}
	NewChatClientWithPool(c, nil, nil)
}

func err(v any) string {
	if e, ok := v.(error); ok {
		return e.Error()
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// TestPoolsAreBuiltPerProvider: the single-pool version dropped every account
// belonging to another backend without a word, which on a machine running
// three agents on different CLIs means half the logins in one file are ignored
// and nothing says so.
func TestPoolsAreBuiltPerProvider(t *testing.T) {
	c := cfgFor(config.ProviderClaude, "claude-opus-5")
	c.Accounts.Pool = []config.AccountConfig{
		{Name: "default", ConfigDir: "~/.claude"},
		{Name: "tam", ConfigDir: "~/.claude-tam"},
		{Name: "codex-main", Provider: config.ProviderCodex, ConfigDir: "~/.codex"},
	}
	pools, err := PoolsFromConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	claudePool := pools[config.ProviderClaude]
	if claudePool == nil || len(claudePool.Names()) != 2 {
		t.Fatalf("claude pool should hold the two claude logins, got %v", claudePool.Names())
	}
	codexPool := pools[config.ProviderCodex]
	if codexPool == nil || len(codexPool.Names()) != 1 {
		t.Fatalf("the codex account was dropped: %v", pools)
	}
	// And the old single-pool entry point still answers with this agent's own.
	one, err := PoolFromConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(one.Names()) != 2 {
		t.Fatalf("PoolFromConfig should return this agent's provider pool, got %v", one.Names())
	}
}

// TestAThirdBackendNeedsNoChangeHere is what #125 was for. opencode is
// registered by its own package and reached through the registry; if this test
// ever needs a branch added to internal/bot to pass, the registry has stopped
// doing its job.
func TestAThirdBackendNeedsNoChangeHere(t *testing.T) {
	c := cfgFor(config.ProviderOpenCode, "oc-sonnet")
	c.Models.Custom = []models.Model{{
		ID: "oc-sonnet", Name: "opencode", API: models.APIOpenCodeCLI,
	}}
	client := NewChatClientWithPool(c, nil, nil)
	if got := client.Name(); got != "opencode" {
		t.Fatalf("the opencode config resolved to %q", got)
	}
}

// TestOpenCodeDoesNotTitleWithClaude is the bug this replaced: both call sites
// ended in "anything else is claude", so an agent moved onto opencode still
// shelled out to the claude CLI to name its sessions — spending claude's quota
// on an agent deliberately taken off claude, and failing outright on a machine
// where claude is not installed.
func TestOpenCodeDoesNotTitleWithClaude(t *testing.T) {
	c := cfgFor(config.ProviderOpenCode, "oc-model")
	c.Models.Custom = []models.Model{{ID: "oc-model", Name: "oc", API: models.APIOpenCodeCLI}}
	p, _ := TitleBackend(c, models.NewResolver(c.Claude.Model, c.Models.Custom))
	if p != nil {
		t.Fatalf("opencode has no titler registered, so the answer must be none — got %T", p)
	}
	// And nil is safe: titler.Title no-ops on it rather than crashing a turn.
}

func TestClaudeAndCodexStillTitle(t *testing.T) {
	for _, tc := range []struct{ provider, model string }{
		{config.ProviderClaude, "claude-opus-5"},
		{config.ProviderCodex, "gpt-6-astra"},
	} {
		c := cfgFor(tc.provider, tc.model)
		p, model := TitleBackend(c, models.NewResolver(tc.model, nil))
		if p == nil {
			t.Errorf("%s lost its titler", tc.provider)
		}
		if model == "" {
			t.Errorf("%s has no title model", tc.provider)
		}
	}
}

// A direct Anthropic key is cheaper and faster than a CLI subprocess for eight
// words — but only when the agent is on claude at all.
func TestADirectKeyIsUsedOnlyForClaude(t *testing.T) {
	c := cfgFor(config.ProviderClaude, "claude-opus-5")
	c.Claude.APIKey = "sk-ant-api03-xxx"
	p, _ := TitleBackend(c, models.NewResolver("claude-opus-5", nil))
	if p == nil {
		t.Fatal("a direct key produced no titler")
	}

	// The same key on a codex agent must not drag titling back to Anthropic.
	c2 := cfgFor(config.ProviderCodex, "gpt-6-astra")
	c2.Claude.APIKey = "sk-ant-api03-xxx"
	p2, _ := TitleBackend(c2, models.NewResolver("gpt-6-astra", nil))
	if p2 == nil {
		t.Fatal("codex lost its titler")
	}
	if fmt.Sprintf("%T", p2) == fmt.Sprintf("%T", p) {
		t.Errorf("a codex agent titled through the Anthropic API: %T", p2)
	}
}
