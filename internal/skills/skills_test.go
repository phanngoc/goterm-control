package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, ws, name, body string) {
	t.Helper()
	dir := filepath.Join(ws, Dir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, File), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadReadsNameAndWhenToUseIt(t *testing.T) {
	ws := t.TempDir()
	write(t, ws, "deploy", `---
name: deploy
description: 'Deploy bomclaw. Dùng khi: build xong và cần đẩy lên. KHÔNG dùng để sửa config.'
---

# Deploy

Chi tiết dài dòng ở đây, và nó không được vào prompt.
`)

	got, err := Load(ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d skills", len(got))
	}
	if got[0].Name != "deploy" {
		t.Errorf("name = %q", got[0].Name)
	}
	if !strings.Contains(got[0].Description, "Dùng khi") {
		t.Errorf("description = %q", got[0].Description)
	}
	if !filepath.IsAbs(got[0].Path) {
		t.Errorf("path %q is relative — a task filed under a project runs in the\n"+
			"project's folder, not the workspace, so a relative path opens nothing", got[0].Path)
	}
}

// Most agents have no skills. A gateway that refused to start over a missing
// folder would be broken by the ordinary case.
func TestNoSkillsFolderIsNotAnError(t *testing.T) {
	got, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("a workspace with no skills folder errored: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
	if Index(got) != "" {
		t.Error("an agent with no skills got a heading with nothing under it")
	}
}

// A malformed header must not make a skill disappear. An agent that cannot use
// a skill AND cannot be told why is the worst of the three outcomes.
func TestABrokenHeaderStillLeavesTheSkillVisible(t *testing.T) {
	ws := t.TempDir()
	write(t, ws, "half-written", "no frontmatter at all\n")
	write(t, ws, "bad-yaml", "---\nname: [unclosed\n---\nbody\n")

	got, err := Load(ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d skills, want both — a skill hidden for reasons nobody can\n"+
			"see cannot be fixed by the person looking at it", len(got))
	}
	for _, s := range got {
		if s.Name == "" {
			t.Errorf("a skill came back nameless: %+v", s)
		}
	}
	if !strings.Contains(Index(got), "no description") {
		t.Error("the index does not say that nobody said when to use these")
	}
}

func TestOnlyFoldersWithASkillFileCount(t *testing.T) {
	ws := t.TempDir()
	write(t, ws, "real", "---\nname: real\ndescription: d\n---\n")
	if err := os.MkdirAll(filepath.Join(ws, Dir, "empty-dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(ws, Dir, ".hidden"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, Dir, "README.md"), []byte("notes"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Load(ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "real" {
		t.Fatalf("got %+v", got)
	}
}

// The description is paid on every single turn. A skill that pastes its manual
// into the header would tax every prompt this agent ever runs.
func TestALongDescriptionIsCut(t *testing.T) {
	ws := t.TempDir()
	write(t, ws, "verbose", "---\nname: verbose\ndescription: '"+strings.Repeat("dài ", 600)+"'\n---\n")
	got, err := Load(ws)
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(got[0].Description)); n > MaxDescriptionRunes+1 {
		t.Errorf("description is %d runes; every turn pays for it", n)
	}
}

func TestIndexNamesTheFileToOpen(t *testing.T) {
	ws := t.TempDir()
	write(t, ws, "deploy", "---\nname: deploy\ndescription: 'khi cần đẩy bản mới'\n---\n")
	got, _ := Load(ws)
	idx := Index(got)
	if !strings.Contains(idx, "deploy") || !strings.Contains(idx, "khi cần đẩy bản mới") {
		t.Errorf("index says neither the name nor when to use it:\n%s", idx)
	}
	if !strings.Contains(idx, got[0].Path) {
		t.Errorf("index does not say which file to open — the whole design is that\n"+
			"the body is read on demand:\n%s", idx)
	}
}

func TestSkillsComeBackInAStableOrder(t *testing.T) {
	ws := t.TempDir()
	for _, n := range []string{"zeta", "alpha", "mid"} {
		write(t, ws, n, "---\nname: "+n+"\ndescription: d\n---\n")
	}
	got, _ := Load(ws)
	if got[0].Name != "alpha" || got[2].Name != "zeta" {
		t.Fatalf("unsorted: %+v — the prompt would change between turns for no reason,\n"+
			"which costs a prompt-cache hit every time", got)
	}
}
