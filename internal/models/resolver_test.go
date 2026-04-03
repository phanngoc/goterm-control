package models

import "testing"

func TestResolverBuiltinModels(t *testing.T) {
	r := NewResolver("claude-sonnet-4-6", nil)

	all := r.List()
	if len(all) != 3 {
		t.Fatalf("expected 3 builtin models, got %d", len(all))
	}

	// Default resolution
	m := r.Resolve(100)
	if m == nil || m.ID != "claude-sonnet-4-6" {
		t.Errorf("expected sonnet as default, got %v", m)
	}
}

func TestResolverLookupByAlias(t *testing.T) {
	r := NewResolver("claude-sonnet-4-6", nil)

	tests := []struct {
		input    string
		expected string
	}{
		{"opus", "claude-opus-4-6"},
		{"o4", "claude-opus-4-6"},
		{"sonnet", "claude-sonnet-4-6"},
		{"s4", "claude-sonnet-4-6"},
		{"haiku", "claude-haiku-4-5"},
		{"h4", "claude-haiku-4-5"},
		{"claude-opus-4-6", "claude-opus-4-6"},
	}

	for _, tt := range tests {
		m := r.Lookup(tt.input)
		if m == nil {
			t.Errorf("Lookup(%q) = nil, want %s", tt.input, tt.expected)
			continue
		}
		if m.ID != tt.expected {
			t.Errorf("Lookup(%q) = %s, want %s", tt.input, m.ID, tt.expected)
		}
	}

	// Unknown model
	if m := r.Lookup("gpt-5"); m != nil {
		t.Errorf("expected nil for unknown model, got %s", m.ID)
	}
}

func TestResolverOverride(t *testing.T) {
	r := NewResolver("claude-sonnet-4-6", nil)
	chatID := int64(100)

	// Before override: default
	m := r.Resolve(chatID)
	if m.ID != "claude-sonnet-4-6" {
		t.Errorf("expected default sonnet, got %s", m.ID)
	}

	// Set override by alias
	m, err := r.SetOverride(chatID, "opus")
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "claude-opus-4-6" {
		t.Errorf("expected opus, got %s", m.ID)
	}

	// Resolve should return override
	m = r.Resolve(chatID)
	if m.ID != "claude-opus-4-6" {
		t.Errorf("expected opus override, got %s", m.ID)
	}

	// Other chatID should still get default
	m = r.Resolve(200)
	if m.ID != "claude-sonnet-4-6" {
		t.Errorf("expected sonnet for other chat, got %s", m.ID)
	}

	// Clear override
	r.ClearOverride(chatID)
	m = r.Resolve(chatID)
	if m.ID != "claude-sonnet-4-6" {
		t.Errorf("expected sonnet after clear, got %s", m.ID)
	}
}

func TestResolverCustomModel(t *testing.T) {
	custom := []Model{
		{
			ID:            "deepseek-r1",
			Name:          "DeepSeek R1",
			Provider:      "openrouter",
			API:           APIClaudeCLI,
			Aliases:       []string{"ds", "deepseek"},
			ContextWindow: 128_000,
			MaxTokens:     8192,
		},
	}

	r := NewResolver("deepseek-r1", custom)

	// Should have 4 models (3 builtin + 1 custom)
	if len(r.List()) != 4 {
		t.Fatalf("expected 4 models, got %d", len(r.List()))
	}

	// Default should be custom model
	m := r.Resolve(0)
	if m.ID != "deepseek-r1" {
		t.Errorf("expected deepseek-r1 as default, got %s", m.ID)
	}

	// Lookup by alias
	m = r.Lookup("ds")
	if m == nil || m.ID != "deepseek-r1" {
		t.Errorf("expected deepseek-r1 from alias 'ds', got %v", m)
	}
}

func TestResolverUnknownDefault(t *testing.T) {
	r := NewResolver("nonexistent-model", nil)

	// Should fall back to first builtin
	m := r.Resolve(0)
	if m == nil {
		t.Fatal("expected fallback to first model")
	}
	if m.ID != "claude-opus-4-6" {
		t.Errorf("expected opus as fallback, got %s", m.ID)
	}
}

func TestSetOverrideUnknown(t *testing.T) {
	r := NewResolver("claude-sonnet-4-6", nil)

	_, err := r.SetOverride(100, "gpt-99")
	if err == nil {
		t.Fatal("expected error for unknown model")
	}
}
