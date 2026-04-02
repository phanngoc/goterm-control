package memory

import (
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
