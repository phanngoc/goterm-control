package plugins

import (
	"path/filepath"
	"strings"
	"testing"
)

// A plugin pinned to a project loads for a turn inside that project — in a
// subfolder too — and for no other turn: its skills stay out of every other
// room's prompt.
func TestAPinnedPluginLoadsOnlyInItsProject(t *testing.T) {
	ws, film, trading := t.TempDir(), t.TempDir(), t.TempDir()
	dir := plugin(t, filepath.Join(ws, Dir), "film-kit", "name: film-kit\ndescription: films\n")
	skill(t, filepath.Join(dir, "skills"), "film-make", "Use when making a film.")

	var st State
	st.SetOnly("film-kit", []string{film})
	if err := st.Save(ws); err != nil {
		t.Fatal(err)
	}

	for _, turn := range []string{film, filepath.Join(film, "films", "day-som")} {
		k := Load(ws, turn)
		if p := mustFind(t, k, "film-kit"); p.Status != Active {
			t.Fatalf("in %s: %s %v", turn, p.Status, p.Reasons)
		}
		if !strings.Contains(k.Prompt(), "film-make") {
			t.Errorf("in %s: the skill is not offered", turn)
		}
	}
	for _, turn := range []string{trading, ""} {
		k := Load(ws, turn)
		p := mustFind(t, k, "film-kit")
		if p.Status != Elsewhere || len(p.Reasons) == 0 || !strings.Contains(p.Reasons[0], filepath.Base(film)) {
			t.Fatalf("in %q: %s %v", turn, p.Status, p.Reasons)
		}
		if strings.Contains(k.Prompt(), "film-make") {
			t.Errorf("in %q: a pinned plugin's skill reached the prompt", turn)
		}
	}
	// A sibling whose name merely starts the same is not inside.
	if p := mustFind(t, Load(ws, film+"-old"), "film-kit"); p.Status != Elsewhere {
		t.Errorf("a prefix-named sibling counted as inside the project: %s", p.Status)
	}

	st.SetOnly("film-kit", nil)
	if err := st.Save(ws); err != nil {
		t.Fatal(err)
	}
	if p := mustFind(t, Load(ws, trading), "film-kit"); p.Status != Active || len(p.Only) != 0 {
		t.Fatalf("unpinned is not global again: %s %v", p.Status, p.Only)
	}
}
