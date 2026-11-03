package fsguard

import (
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// posixOnly skips path-resolution tests on Windows. The guard exists only on
// macOS — Configure turns it off everywhere else — and these cases are written
// in the POSIX paths it actually resolves there.
func posixOnly(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the guard is macOS-only; these cases use POSIX paths")
	}
}

func TestDefaultsResolveUnderHome(t *testing.T) {
	posixOnly(t)
	got := Resolve(Options{Home: "/Users/x"})
	for _, want := range []string{"/Users/x/Music", "/Users/x/Downloads", "/Users/x/Library/Mobile Documents"} {
		if !slices.Contains(got, want) {
			t.Errorf("default list lacks %s: %v", want, got)
		}
	}
	// The repo lives in ~/Documents and the agents edit it.
	if slices.Contains(got, "/Users/x/Documents") {
		t.Errorf("~/Documents must stay reachable: %v", got)
	}
}

func TestAllowTakesAPathAndEverythingUnderItOff(t *testing.T) {
	posixOnly(t)
	got := Resolve(Options{Home: "/Users/x", Allow: []string{"~/Downloads", "~/Library"}})
	for _, p := range got {
		if p == "/Users/x/Downloads" || strings.HasPrefix(p, "/Users/x/Library/") {
			t.Errorf("%s is still denied after allow: %v", p, got)
		}
	}
	if !slices.Contains(got, "/Users/x/Music") {
		t.Errorf("allow removed more than it named: %v", got)
	}
}

func TestDenyAddsAndNeverTakesHomeOrRoot(t *testing.T) {
	posixOnly(t)
	got := Resolve(Options{Home: "/Users/x", Deny: []string{"~/secret", "/Volumes/USB", "~", "/", "  "}})
	if !slices.Contains(got, "/Users/x/secret") || !slices.Contains(got, "/Volumes/USB") {
		t.Errorf("extra deny paths missing: %v", got)
	}
	// Denying home or / would deny the CLI its own config: every turn dies.
	if slices.Contains(got, "/Users/x") || slices.Contains(got, "/") {
		t.Errorf("home or root ended up denied: %v", got)
	}
}

func TestProfileQuotesPathsAndKeepsTheFolderVisible(t *testing.T) {
	p := Profile([]string{`/Users/x/Library/Mobile Documents`, `/tmp/a"b`})
	for _, want := range []string{
		`(allow default)`,
		`(deny file-read-data (literal "/Users/x/Library/Mobile Documents"))`,
		`(deny file-read* file-write* (prefix "/Users/x/Library/Mobile Documents/"))`,
		`(literal "/tmp/a\"b")`,
	} {
		if !strings.Contains(p, want) {
			t.Errorf("profile lacks %s:\n%s", want, p)
		}
	}
	// Denying file-read* on the folder itself would make `ls ~` fail.
	if strings.Contains(p, `file-read* file-write* (literal`) {
		t.Errorf("profile denies the folder's own metadata:\n%s", p)
	}
}

func TestOffMeansPlainExec(t *testing.T) {
	Configure(Options{Enabled: false, Home: "/Users/x"})
	defer Configure(Options{})
	cmd := CommandContext(t.Context(), "echo", "hi")
	if filepath.Base(cmd.Path) == "sandbox-exec" {
		t.Errorf("disabled guard still wrapped: %v", cmd.Args)
	}
	if PromptNote() != "" || Paths() != nil {
		t.Error("disabled guard still reports paths or a prompt note")
	}
}
