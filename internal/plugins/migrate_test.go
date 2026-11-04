package plugins

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ngocp/goterm-control/internal/skills"
)

// The skills package spells these out because it cannot import this one.
func TestSkillsAgreesOnWherePluginsLive(t *testing.T) {
	if skills.PluginsDir != Dir || skills.ManifestFile != File {
		t.Fatalf("skills seeds into %s/%s, plugins reads %s/%s",
			skills.PluginsDir, skills.ManifestFile, Dir, File)
	}
}

func TestMigrateMovesSkillsWholeAndKeepsTheOldPath(t *testing.T) {
	ws := t.TempDir()
	// An agent's own skill, with a file beside it and an edit nobody shipped.
	skill(t, filepath.Join(ws, skills.Dir), "game-build", "Use when building a game.")
	writeFile(t, filepath.Join(ws, skills.Dir, "game-build", "template.html"), "<html>", 0o644)
	// A bundled default the agent revised.
	writeFile(t, filepath.Join(ws, skills.Dir, "browser", "SKILL.md"),
		"---\nname: browser\ndescription: my revised browser notes\n---\nlearned something\n", 0o644)
	// A category, the layout hermes-agent writes.
	skill(t, filepath.Join(ws, skills.Dir, "ops"), "deploy", "Use when shipping.")
	// A name already taken by a plugin.
	skill(t, filepath.Join(ws, skills.Dir), "forge", "Old forge notes.")
	plugin(t, filepath.Join(ws, Dir), "forge", "description: the real forge")

	before := Load(ws, "").Prompt()
	moved, skipped, err := MigrateSkills(ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 3 || len(skipped) != 1 || !strings.Contains(skipped[0], "forge") {
		t.Fatalf("moved %+v skipped %v", moved, skipped)
	}

	for _, name := range []string{"game-build", "browser", "deploy"} {
		if _, err := os.Stat(filepath.Join(ws, Dir, name, File)); err != nil {
			t.Errorf("%s has no manifest: %v", name, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(ws, Dir, "game-build", "template.html")); string(b) != "<html>" {
		t.Error("a file shipped beside SKILL.md was not moved with it")
	}
	if b, _ := os.ReadFile(filepath.Join(ws, Dir, "browser", "SKILL.md")); !strings.Contains(string(b), "learned something") {
		t.Error("the agent's revision of a default was lost")
	}
	if m := mustFind(t, Load(ws, ""), "browser"); !contains(m.Provides, "browser") {
		t.Errorf("a migrated default did not get its bundled manifest: %+v", m.Manifest)
	}

	k := Load(ws, "")
	for _, name := range []string{"game-build", "browser", "deploy"} {
		p := mustFind(t, k, name)
		if p.Kind != KindPlugin || p.Status != Active || len(p.SkillList) != 1 {
			t.Errorf("%s: kind=%s status=%s skills=%d", name, p.Kind, p.Status, len(p.SkillList))
		}
	}
	// Same skills in front of the agent, only at their new paths.
	after := k.Prompt()
	for _, want := range []string{"game-build", "my revised browser notes", "deploy", "Use when shipping."} {
		if !strings.Contains(before, want) || !strings.Contains(after, want) {
			t.Errorf("%q: before=%v after=%v", want, strings.Contains(before, want), strings.Contains(after, want))
		}
	}
	if strings.Contains(after, "Your plugins") {
		t.Errorf("skill-only plugins grew a plugins section:\n%s", after)
	}

	// The old path still opens, for threads that were told it.
	if runtime.GOOS != "windows" {
		if b, err := os.ReadFile(filepath.Join(ws, skills.Dir, "game-build", "SKILL.md")); err != nil || !strings.Contains(string(b), "building a game") {
			t.Errorf("old path no longer readable: %v", err)
		}
	}

	// Idempotent: the symlinks left behind are not skills to move again.
	again, skipped2, err := MigrateSkills(ws)
	if err != nil || len(again) != 0 || len(skipped2) != 1 {
		t.Fatalf("second run moved %+v skipped %v err %v", again, skipped2, err)
	}
}

// Lookups by name find a skill after it moved — the hub, the roster and
// skills.Read all go through skills.All.
func TestSkillsLookupFindsSkillsInsidePlugins(t *testing.T) {
	ws := t.TempDir()
	skill(t, filepath.Join(ws, skills.Dir), "notes", "Use for notes.")
	if _, _, err := MigrateSkills(ws); err != nil {
		t.Fatal(err)
	}
	body, err := skills.Read(ws, "notes")
	if err != nil || !strings.Contains(string(body), "Use for notes.") {
		t.Fatalf("read after migration: %v", err)
	}
	if err := skills.Install(ws, "notes", []byte("---\nname: notes\ndescription: edited\n---\n")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(ws, Dir, "notes", "SKILL.md")); !strings.Contains(string(b), "edited") {
		t.Fatal("an edit went somewhere other than the plugin")
	}
	roster := skills.Roster([]skills.Peer{{ID: "peer", Provider: "codex", Workspace: ws, Online: true}})
	if !strings.Contains(roster, "notes") {
		t.Fatalf("the roster lost a migrated skill:\n%s", roster)
	}
}

func TestDefaultsAreSeededAsPlugins(t *testing.T) {
	ws := t.TempDir()
	names, err := skills.EnsureDefaults(ws)
	if err != nil || len(names) == 0 {
		t.Fatalf("seeded %v: %v", names, err)
	}
	k := Load(ws, "")
	for _, n := range names {
		p := mustFind(t, k, n)
		if p.Kind != KindPlugin || p.Status != Active || len(p.SkillList) != 1 || len(p.Provides) == 0 {
			t.Errorf("%s: %+v", n, p)
		}
	}
	// Seeding again writes nothing.
	if again, _ := skills.EnsureDefaults(ws); len(again) != 0 {
		t.Fatalf("re-seeded %v", again)
	}
}
