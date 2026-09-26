package gateway

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func filesCall(t *testing.T, deps Deps, params map[string]any) (map[string]any, error) {
	t.Helper()
	raw, _ := json.Marshal(params)
	out, err := handleProjectFiles(deps, raw)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	return m, nil
}

func TestASaveOverAFileAnAgentChangedIsRefused(t *testing.T) {
	deps, p := projectServeDeps(t)
	opened, err := filesCall(t, deps, map[string]any{"channel_id": p.ID, "path": "board/index.html", "read": true})
	if err != nil {
		t.Fatal(err)
	}
	base, _ := opened["mtime"].(string)
	if base == "" {
		t.Fatal("a read did not say when the file was last changed")
	}

	// An agent writes the file while the editor has it open.
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(p.Workspace, "board", "index.html"), []byte("agent's"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = filesCall(t, deps, map[string]any{"channel_id": p.ID, "path": "board/index.html", "body": "mine", "base_mtime": base})
	if err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("save over a changed file = %v, want a conflict", err)
	}
	if b, _ := os.ReadFile(filepath.Join(p.Workspace, "board", "index.html")); string(b) != "agent's" {
		t.Fatalf("the agent's version was overwritten: %q", b)
	}

	// Choosing to overwrite is a save without the base.
	saved, err := filesCall(t, deps, map[string]any{"channel_id": p.ID, "path": "board/index.html", "body": "mine"})
	if err != nil {
		t.Fatal(err)
	}
	if saved["mtime"] == "" || saved["body"] != "mine" {
		t.Fatalf("save answered %v", saved)
	}
}

func TestFileTreeOperations(t *testing.T) {
	deps, p := projectServeDeps(t)
	steps := []map[string]any{
		{"op": "mkdir", "path": "notes"},
		{"path": "notes/a.md", "body": "# a"},
		{"op": "rename", "path": "notes/a.md", "to": "notes/b.md"},
	}
	for _, s := range steps {
		s["channel_id"] = p.ID
		if _, err := filesCall(t, deps, s); err != nil {
			t.Fatalf("%v: %v", s, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(p.Workspace, "notes", "b.md")); string(b) != "# a" {
		t.Fatalf("renamed file reads %q", b)
	}
	if _, err := filesCall(t, deps, map[string]any{"channel_id": p.ID, "op": "delete", "path": "notes"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(p.Workspace, "notes")); !os.IsNotExist(err) {
		t.Fatal("deleted folder is still there")
	}
	if _, err := filesCall(t, deps, map[string]any{"channel_id": p.ID, "op": "chmod", "path": "board"}); err == nil {
		t.Fatal("an unknown op was not refused")
	}
}

func TestIndexAndSearchThroughTheMethod(t *testing.T) {
	deps, p := projectServeDeps(t)
	idx, err := filesCall(t, deps, map[string]any{"channel_id": p.ID, "op": "index"})
	if err != nil {
		t.Fatal(err)
	}
	if paths, _ := idx["paths"].([]any); len(paths) == 0 {
		t.Fatalf("index = %v", idx)
	}
	res, err := filesCall(t, deps, map[string]any{"channel_id": p.ID, "op": "search", "query": "zzz-nowhere"})
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := res["matches"].([]any); !ok || len(m) != 0 {
		t.Fatalf("no matches should be an empty list, got %v", res["matches"])
	}
}
