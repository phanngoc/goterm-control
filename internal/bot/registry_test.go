package bot

import (
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
