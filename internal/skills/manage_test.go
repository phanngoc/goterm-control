package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultsAreSeededOnceAndThenLeftAlone(t *testing.T) {
	ws := t.TempDir()
	written, err := EnsureDefaults(ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) == 0 {
		t.Fatal("no defaults were seeded")
	}
	list, err := Load(ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != len(written) {
		t.Fatalf("seeded %d, loaded %d", len(written), len(list))
	}
	// Every default has to say when it applies; that line IS the routing
	// decision, and a default without one teaches the agent nothing.
	for _, s := range list {
		if s.Description == "" {
			t.Errorf("bundled skill %q has no description", s.Name)
		}
	}

	// The agent revises its copy. Starting the gateway again must not undo it:
	// that would destroy the lesson silently, on a schedule nobody chose.
	edited := filepath.Join(ws, Dir, list[0].Name, File)
	if err := os.WriteFile(edited,
		[]byte("---\nname: "+list[0].Name+"\ndescription: 'điều tôi tự học được'\n---\nbài học\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	again, err := EnsureDefaults(ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("re-seeded %v on a second start", again)
	}
	body, _ := os.ReadFile(edited)
	if !strings.Contains(string(body), "điều tôi tự học được") {
		t.Fatal("a restart overwrote what the agent had learned — which is exactly\n" +
			"what per-agent copies exist to prevent")
	}
}

func TestInstallRemoveAndReadRoundTrip(t *testing.T) {
	ws := t.TempDir()
	body := []byte("---\nname: deploy\ndescription: 'khi cần đẩy bản mới'\n---\ncác bước\n")
	if err := Install(ws, "deploy", body); err != nil {
		t.Fatal(err)
	}
	got, err := Read(ws, "deploy")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) {
		t.Fatalf("read back %q", got)
	}
	// No temp file left where the agent would try to read it as a skill.
	entries, _ := os.ReadDir(filepath.Join(ws, Dir, "deploy"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".install-") {
			t.Errorf("left a temp file behind: %s", e.Name())
		}
	}
	if err := Remove(ws, "deploy"); err != nil {
		t.Fatal(err)
	}
	if list, _ := Load(ws); len(list) != 0 {
		t.Fatalf("still there: %+v", list)
	}
	if err := Remove(ws, "deploy"); err == nil {
		t.Error("removing what is not there reported success")
	}
}

// A skill name becomes a directory name. Anything that could climb out of the
// skills folder is refused rather than sanitised — silently renaming what
// someone asked for produces a skill they cannot find again.
func TestANameCannotEscapeTheSkillsFolder(t *testing.T) {
	ws := t.TempDir()
	for _, bad := range []string{"../outside", "a/b", "/etc/passwd", "..", ".hidden", "", "Deploy", "a b"} {
		if err := Install(ws, bad, []byte("x")); err == nil {
			t.Errorf("accepted %q as a skill name", bad)
		}
	}
	// And nothing was created anywhere.
	if _, err := os.Stat(filepath.Join(filepath.Dir(ws), "outside")); err == nil {
		t.Fatal("a skill was written outside the workspace")
	}
}

// The operation that makes per-agent copies worth having: without it each
// agent improves alone and its lesson dies with it.
func TestCopyCarriesOneAgentsVersionToAnother(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	if _, err := EnsureDefaults(a); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureDefaults(b); err != nil {
		t.Fatal(err)
	}
	improved := []byte("---\nname: browser\ndescription: 'bản đã cải tiến'\n---\nmẹo mới\n")
	if err := Install(a, "browser", improved); err != nil {
		t.Fatal(err)
	}

	if err := Copy(a, b, "browser"); err != nil {
		t.Fatal(err)
	}
	got, err := Read(b, "browser")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "mẹo mới") {
		t.Fatal("the improvement did not travel")
	}
	// And the source is untouched — a copy, not a move.
	if src, _ := Read(a, "browser"); !strings.Contains(string(src), "mẹo mới") {
		t.Error("copying took it away from the agent that wrote it")
	}
	if err := Copy(a, a, "browser"); err == nil {
		t.Error("copying an agent's skill to itself reported success")
	}
}

func TestAnOversizedSkillIsRefused(t *testing.T) {
	ws := t.TempDir()
	if err := Install(ws, "huge", make([]byte, MaxSkillBytes+1)); err == nil {
		t.Error("accepted a skill past the cap")
	}
	if err := Install(ws, "empty", nil); err == nil {
		t.Error("accepted a skill with no content")
	}
}

// A default can be removed like anything else, and comes back — so the hub can
// offer "put it back" without the agent's copy being sacred.
func TestABundledDefaultCanBeRestored(t *testing.T) {
	names := DefaultNames()
	if len(names) == 0 {
		t.Fatal("no bundled defaults")
	}
	body, err := Default(names[0])
	if err != nil {
		t.Fatal(err)
	}
	ws := t.TempDir()
	if err := Install(ws, names[0], body); err != nil {
		t.Fatal(err)
	}
	if list, _ := Load(ws); len(list) != 1 {
		t.Fatalf("got %+v", list)
	}
	if _, err := Default("nothing-like-this"); err == nil {
		t.Error("returned a bundled skill that does not exist")
	}
}
