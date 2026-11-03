package fsguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The guard is only worth anything if the OS enforces it, so this runs the
// real sandbox rather than checking the profile's text: a blocked folder must
// refuse a listing, the folder next to it must not, and the refusal must reach
// a grandchild — the CLIs run their tools that way.
func TestTheSandboxBlocksTheFolderAndItsGrandchildren(t *testing.T) {
	if !supported() {
		t.Skip("no sandbox-exec")
	}
	home, err := filepath.EvalSymlinks(t.TempDir()) // /var → /private/var: the sandbox sees real paths
	if err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(home, "Music")
	open := filepath.Join(home, "Documents")
	for _, d := range []string{blocked, open} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "f.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	Configure(Options{Enabled: true, Home: home})
	defer Configure(Options{})

	run := func(script string) (string, error) {
		out, err := CommandContext(t.Context(), "bash", "-c", script).CombinedOutput()
		return string(out), err
	}

	if out, err := run("ls " + open); err != nil || !strings.Contains(out, "f.txt") {
		t.Errorf("an unguarded folder was refused: %v %s", err, out)
	}
	if out, err := run("ls -ld " + blocked); err != nil {
		t.Errorf("the blocked folder itself must stay visible to ls -ld: %v %s", err, out)
	}
	if out, _ := run("ls " + blocked); !strings.Contains(out, "Operation not permitted") {
		t.Errorf("listing the blocked folder was not refused: %s", out)
	}
	if out, _ := run("bash -c 'cat " + filepath.Join(blocked, "f.txt") + "'"); !strings.Contains(out, "Operation not permitted") {
		t.Errorf("a grandchild read a blocked file: %s", out)
	}
	if out, _ := run("touch " + filepath.Join(blocked, "new")); !strings.Contains(out, "Operation not permitted") {
		t.Errorf("a write into the blocked folder was not refused: %s", out)
	}
	if !strings.Contains(PromptNote(), "Operation not permitted") {
		t.Errorf("prompt note does not explain the refusal: %q", PromptNote())
	}
}
