package config

import "testing"

// A package that uses config without importing a provider would otherwise find
// an empty registry and reject every valid config. The blank imports in this
// package are what stop that; this test is what notices if they are removed.
func TestBackendsAreRegisteredWhereverConfigIs(t *testing.T) {
	cfg := &Config{Provider: ProviderClaude}
	cfg.Claude.APIKey = "x"
	cfg.Claude.Model = "claude-opus-5"
	cfg.Telegram.Token = "t"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("a valid claude config was rejected — is the registry empty? %v", err)
	}
	cfg2 := &Config{Provider: ProviderCodex}
	cfg2.Claude.Model = "gpt-6-astra"
	cfg2.Telegram.Token = "t"
	if err := cfg2.Validate(); err != nil {
		t.Fatalf("a valid codex config was rejected: %v", err)
	}
}
