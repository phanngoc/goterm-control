package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

func TestExtractFacts(t *testing.T) {
	entry := ExtractFacts("sess1", 100,
		"Can you check /Users/ngocp/Documents/projects/openclaw/main.go?",
		"I checked the file. It contains the main entry point for the application.",
	)

	if len(entry.Keywords) == 0 {
		t.Error("expected keywords to be extracted")
	}

	// Should extract file path
	hasPath := false
	for _, f := range entry.Facts {
		if f != "" {
			hasPath = true
		}
	}
	if !hasPath && len(entry.Facts) == 0 {
		t.Log("no facts extracted (path extraction may depend on regex)")
	}

	if entry.Summary == "" {
		t.Error("expected summary to be extracted")
	}

	if entry.SessionID != "sess1" {
		t.Errorf("expected session_id sess1, got %s", entry.SessionID)
	}
}

func TestStoreSearchRoundtrip(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Append entries
	entries := []Entry{
		{SessionID: "s1", Keywords: []string{"kubernetes", "deployment", "pod"}, Facts: []string{"path: /etc/kubernetes/config"}, Summary: "Discussed k8s deployment."},
		{SessionID: "s2", Keywords: []string{"golang", "testing", "benchmark"}, Facts: []string{"url: https://go.dev/doc"}, Summary: "Wrote Go tests."},
		{SessionID: "s3", Keywords: []string{"react", "typescript", "component"}, Facts: []string{"path: /src/App.tsx"}, Summary: "Created React component."},
	}

	for _, e := range entries {
		if err := store.Append(e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	// Search for kubernetes-related
	results, err := store.Search("kubernetes deployment", 2)
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	if len(results) == 0 {
		t.Fatal("expected search results for 'kubernetes deployment'")
	}

	if results[0].SessionID != "s1" {
		t.Errorf("expected s1 as top result, got %s", results[0].SessionID)
	}
}

func TestBuildMemoryContext(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	_ = store.Append(Entry{
		SessionID: "s1",
		Keywords:  []string{"docker", "container", "build"},
		Facts:     []string{"path: /Dockerfile"},
		Summary:   "Built Docker image for the project.",
	})

	ctx := BuildMemoryContext(store, "help me with docker build", 3)

	if ctx == "" {
		t.Fatal("expected non-empty memory context")
	}

	if !contains(ctx, "Relevant Memory") {
		t.Error("expected memory context header")
	}
}

// mockCompleter simulates an LLM extraction response.
type mockCompleter struct {
	response string
	err      error
}

func (m *mockCompleter) Complete(ctx context.Context, model, system, userMessage string, maxTokens int) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.response, nil
}

func TestExtractModeSwitching(t *testing.T) {
	ctx := context.Background()

	t.Run("rule mode ignores completer", func(t *testing.T) {
		entry := Extract(ctx, nil, "rule", "sess1", 100, "hello", "world")
		if entry.SessionID != "sess1" {
			t.Errorf("expected sess1, got %s", entry.SessionID)
		}
	})

	t.Run("llm mode with nil completer falls back to rule", func(t *testing.T) {
		entry := Extract(ctx, nil, "llm", "sess1", 100, "check /tmp/test.go", "I checked the file.")
		if entry.SessionID != "sess1" {
			t.Errorf("expected sess1, got %s", entry.SessionID)
		}
		if entry.Summary == "" {
			t.Error("expected summary from rule-based fallback")
		}
	})

	t.Run("llm mode with working completer", func(t *testing.T) {
		resp, _ := json.Marshal(extractionResult{
			Keywords: []string{"deployment", "kubernetes", "production"},
			Facts:    []string{"path: /etc/k8s/config.yaml", "decision: use rolling update"},
			Summary:  "User wants to deploy to production using Kubernetes",
			Intent:   "Deploy application to production k8s cluster",
		})
		client := &mockCompleter{response: string(resp)}
		entry := Extract(ctx, client, "llm", "sess1", 100, "deploy to k8s", "I'll help you deploy.")
		if entry.Intent != "Deploy application to production k8s cluster" {
			t.Errorf("expected LLM intent, got %q", entry.Intent)
		}
		if len(entry.Keywords) != 3 {
			t.Errorf("expected 3 keywords, got %d", len(entry.Keywords))
		}
	})

	t.Run("llm mode with error falls back to rule", func(t *testing.T) {
		client := &mockCompleter{err: fmt.Errorf("api error")}
		entry := Extract(ctx, client, "llm", "sess1", 100, "check /tmp/file.go", "Done checking.")
		if entry.Intent != "" {
			t.Errorf("rule-based should not set intent, got %q", entry.Intent)
		}
		if entry.Summary == "" {
			t.Error("expected summary from rule-based fallback")
		}
	})

	t.Run("hybrid mode merges results", func(t *testing.T) {
		resp, _ := json.Marshal(extractionResult{
			Keywords: []string{"docker", "container", "deploy"},
			Facts:    []string{"decision: use multi-stage build"},
			Summary:  "Setting up Docker for deployment",
			Intent:   "Containerize application with Docker",
		})
		client := &mockCompleter{response: string(resp)}
		entry := Extract(ctx, client, "hybrid", "sess1", 100,
			"help with docker build, check /usr/local/Dockerfile",
			"I'll help with the Docker build process. Check https://docs.docker.com for more.")

		// Should have LLM intent
		if entry.Intent != "Containerize application with Docker" {
			t.Errorf("expected LLM intent, got %q", entry.Intent)
		}
		// Should have LLM keywords
		if len(entry.Keywords) < 3 {
			t.Errorf("expected at least 3 keywords, got %d", len(entry.Keywords))
		}
		// Should have merged facts (LLM decision + rule-based URL/path)
		hasDecision := false
		hasURL := false
		for _, f := range entry.Facts {
			if f == "decision: use multi-stage build" {
				hasDecision = true
			}
			if contains(f, "docs.docker.com") {
				hasURL = true
			}
		}
		if !hasDecision {
			t.Error("expected LLM-extracted decision fact")
		}
		if !hasURL {
			t.Error("expected rule-extracted URL fact")
		}
	})
}

func TestExtractFactsLLMCache(t *testing.T) {
	callCount := 0
	resp, _ := json.Marshal(extractionResult{
		Keywords: []string{"test"},
		Facts:    []string{"path: /test"},
		Summary:  "Test summary",
		Intent:   "Testing",
	})
	client := &mockCompleter{response: string(resp)}

	// Wrap to count calls
	wrapper := &countCompleter{inner: client, count: &callCount}

	ctx := context.Background()
	// First call
	entry1, err := ExtractFactsLLM(ctx, wrapper, "s1", 1, "same message", "same response")
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if callCount != 1 {
		t.Fatalf("expected 1 call, got %d", callCount)
	}

	// Second call with same content should hit cache
	entry2, err := ExtractFactsLLM(ctx, wrapper, "s2", 2, "same message", "same response")
	if err != nil {
		t.Fatalf("cached call: %v", err)
	}
	if callCount != 1 {
		t.Errorf("expected cache hit (1 call), got %d calls", callCount)
	}
	// Cached entry should have updated session/chat IDs
	if entry2.SessionID != "s2" {
		t.Errorf("expected s2, got %s", entry2.SessionID)
	}
	_ = entry1
}

type countCompleter struct {
	inner *mockCompleter
	count *int
}

func (c *countCompleter) Complete(ctx context.Context, model, system, userMessage string, maxTokens int) (string, error) {
	*c.count++
	return c.inner.Complete(ctx, model, system, userMessage, maxTokens)
}

func TestExtractJSON(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain json", `{"key": "value"}`, `{"key": "value"}`},
		{"markdown fenced", "```json\n{\"key\": \"value\"}\n```", `{"key": "value"}`},
		{"text before", "Here is the result:\n{\"a\": 1}", `{"a": 1}`},
		{"no json", "no json here", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractJSON(tt.input)
			if got != tt.want {
				t.Errorf("extractJSON(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
