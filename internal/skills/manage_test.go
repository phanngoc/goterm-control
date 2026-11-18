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
	list, err := seeded(ws)
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
	edited := list[0].Path
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

// A skill the backend's own loop filed under a category is the same skill. The
// hub has to reach it by name — having to know its category to remove it would
// make the hub useless for exactly the skills the loop produced.
func TestTheHubReachesASkillTheBackendNested(t *testing.T) {
	ws := t.TempDir()
	dir := filepath.Join(ws, Dir, "devops", "deploy")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	original := "---\nname: deploy\ndescription: 'bản hermes tự viết'\n---\nthân\n"
	if err := os.WriteFile(filepath.Join(dir, File), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Read(ws, "deploy")
	if err != nil {
		t.Fatalf("cannot read a nested skill by name: %v", err)
	}
	if string(got) != original {
		t.Fatalf("read back %q", got)
	}

	// Replacing it keeps it where it lives. A second copy at the top level
	// would leave the agent holding two skills of one name.
	improved := "---\nname: deploy\ndescription: 'đã sửa'\n---\nbản mới\n"
	if err := Install(ws, "deploy", []byte(improved)); err != nil {
		t.Fatal(err)
	}
	list, _ := Load(ws)
	if len(list) != 1 {
		t.Fatalf("installing over a nested skill made %d of them: %+v", len(list), list)
	}
	if list[0].Category != "devops" {
		t.Errorf("the skill moved out of its category: %+v", list[0])
	}
	back, _ := Read(ws, "deploy")
	if string(back) != improved {
		t.Errorf("the replacement did not land: %q", back)
	}

	// And it can be copied to a peer, which is the whole point of the hub.
	other := t.TempDir()
	if err := Copy(ws, other, "deploy"); err != nil {
		t.Fatal(err)
	}
	if b, err := Read(other, "deploy"); err != nil || string(b) != improved {
		t.Fatalf("the copy did not arrive: %q %v", b, err)
	}

	if err := Remove(ws, "deploy"); err != nil {
		t.Fatalf("cannot remove a nested skill by name: %v", err)
	}
	if list, _ := Load(ws); len(list) != 0 {
		t.Fatalf("still there: %+v", list)
	}
}

// The bundled set goes into every prompt of every agent, forever — so it holds
// only what is true of the MACHINE, not of any one repo. A skill about how some
// codebase deploys belongs in that codebase (skills.ProjectDir), where it stops
// applying the moment the agent works on something else.
//
// This test is the budget alarm: it fails when the bundled set grows past the
// room an agent needs for skills of its own.
func TestTheBundledSetFitsWithRoomLeft(t *testing.T) {
	ws := t.TempDir()
	if _, err := EnsureDefaults(ws); err != nil {
		t.Fatal(err)
	}
	list, err := seeded(ws)
	if err != nil {
		t.Fatal(err)
	}
	// Measure with the path an agent actually sees, not the test's temp dir:
	// macOS's TMPDIR is ~50 runes longer than Linux's /tmp, and that,
	// repeated on every line, made the same set pass on CI and fail on a Mac.
	// The stand-in is on the long side of real workspaces.
	const agentWS = "/Users/someone-long/goterm-workspace-3"
	for i := range list {
		list[i].Path = strings.Replace(list[i].Path, ws, agentWS, 1)
	}
	idx := []rune(Index(list))
	if len(idx) > MaxIndexRunes {
		t.Fatalf("the bundled skills alone are %d runes, past the %d cap — an agent\n"+
			"with skills of its own would have them silently cut", len(idx), MaxIndexRunes)
	}
	// Two thirds is the line. Past it there is no room for an agent's own
	// skills, and the ones it wrote are the first to be dropped.
	if limit := MaxIndexRunes * 2 / 3; len(idx) > limit {
		t.Errorf("the bundled skills use %d of %d runes (over %d). Either shorten a\n"+
			"description or give the rules their own budget before adding another.",
			len(idx), MaxIndexRunes, limit)
	}
	// Every bundled description has to say when to use it AND when not to:
	// the index is where routing happens, and "NOT for" is half the decision.
	for _, s := range list {
		if !strings.Contains(s.Description, "Use when") {
			t.Errorf("%q does not say when to use it", s.Name)
		}
		if !strings.Contains(s.Description, "NOT for") {
			t.Errorf("%q does not say when NOT to use it — half the routing signal", s.Name)
		}
	}
}

// seeded reads the bundled defaults as they land: each a plugin folder whose
// root SKILL.md is its skill. Load would not see them — it reads skills/, and
// finding skills inside plugins is the plugins package's finder's job.
func seeded(ws string) ([]Skill, error) {
	return LoadDir(filepath.Join(ws, PluginsDir))
}

// A bundled plugin can carry files beyond its SKILL.md: game-studio's
// stages/ are read on demand and must arrive with it — every stage the skill
// names — without each becoming an index line of its own.
func TestBundledPluginFilesArriveButAreNotSkills(t *testing.T) {
	ws := t.TempDir()
	if _, err := EnsureDefaults(ws); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(ws, PluginsDir, "game-studio")
	body, err := os.ReadFile(filepath.Join(dir, File))
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"spec", "build", "playtest", "iterate", "ship"} {
		rel := "stages/" + stage + ".md"
		if !strings.Contains(string(body), rel) {
			t.Errorf("SKILL.md does not point at %s", rel)
		}
		if b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel))); err != nil || len(b) < 200 {
			t.Errorf("%s did not arrive: %v", rel, err)
		}
	}
	list, err := seeded(ws)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, s := range list {
		if strings.HasPrefix(s.Dir, dir) {
			n++
		}
	}
	if n != 1 {
		t.Errorf("game-studio shows as %d skills, want 1", n)
	}
	// A reset puts the stages back too.
	os.RemoveAll(filepath.Join(dir, "stages"))
	if err := InstallDefault(ws, "game-studio"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "stages", "ship.md")); err != nil {
		t.Errorf("reset did not restore stages: %v", err)
	}
}
