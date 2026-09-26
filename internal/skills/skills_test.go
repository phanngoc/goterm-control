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

// hermes-agent groups its skills as skills/<category>/<name>/SKILL.md, and an
// agent running on that backend writes into this same folder. Without reading
// that shape, everything the backend's own self-improvement loop produced would
// be invisible to the hub, to the roster, and to the agent's own index.
func TestASkillNestedUnderACategoryIsFound(t *testing.T) {
	ws := t.TempDir()
	dir := filepath.Join(ws, Dir, "devops", "deploy")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, File),
		[]byte("---\nname: deploy\ndescription: 'khi cần đẩy bản mới'\n---\nchi tiết\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	write(t, ws, "browser", "---\nname: browser\ndescription: 'trình duyệt'\n---\n")

	got, err := Load(ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d skills, want the flat one and the nested one: %+v", len(got), got)
	}
	var nested *Skill
	for i := range got {
		if got[i].Name == "deploy" {
			nested = &got[i]
		}
	}
	if nested == nil {
		t.Fatal("the nested skill was not found")
	}
	if nested.Category != "devops" {
		t.Errorf("category = %q, want devops", nested.Category)
	}
	if !strings.Contains(Index(got), "deploy") {
		t.Error("a nested skill never reaches the agent's own prompt")
	}
}

// One level, and only where the level above held no skill of its own. Deeper
// than that is somebody's notes folder, and walking it would turn every stray
// SKILL.md on disk into something the agent believes it can do.
func TestNestingStopsAtOneLevel(t *testing.T) {
	ws := t.TempDir()
	deep := filepath.Join(ws, Dir, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deep, File),
		[]byte("---\nname: buried\ndescription: d\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, _ := Load(ws)
	for _, s := range got {
		if s.Name == "buried" {
			t.Fatal("walked deeper than one level")
		}
	}
}

// A skill that has its own SKILL.md is a skill, not a category — even when it
// also ships folders of its own (references/, scripts/, which hermes skills do).
func TestAFolderWithBothIsASkillNotACategory(t *testing.T) {
	ws := t.TempDir()
	write(t, ws, "research", "---\nname: research\ndescription: 'tra cứu'\n---\n")
	inner := filepath.Join(ws, Dir, "research", "references", "notes")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inner, File),
		[]byte("---\nname: stray\ndescription: d\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, _ := Load(ws)
	if len(got) != 1 || got[0].Name != "research" {
		t.Fatalf("a skill's own support folder was read as a category: %+v", got)
	}
}

// A bundled skill whose frontmatter does not parse is a skill no agent is ever
// told about: the index is built from name and description alone.
func TestEveryBundledSkillLoadsWithADescription(t *testing.T) {
	ws := t.TempDir()
	if _, err := EnsureDefaults(ws); err != nil {
		t.Fatal(err)
	}
	list, err := Load(ws)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, s := range list {
		got[s.Name] = s.Description
	}
	for _, want := range []string{"artifacts", "browser", "channels", "preview"} {
		if strings.TrimSpace(got[want]) == "" {
			t.Errorf("bundled skill %q missing or has no description (loaded: %v)", want, got)
		}
	}
	if !strings.Contains(got["preview"], "Preview button") {
		t.Errorf("preview description = %q", got["preview"])
	}
}
