package gateway

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// What counts as the project changing: its own files, not what git ignores
// (renders, builds); and server code is told apart from the rest.
func TestProjectChangedFollowsTheProjectsOwnFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	write := func(rel string, at time.Time) {
		p := filepath.Join(dir, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(p, at, at)
	}
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	write(".gitignore", base)
	_ = os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("out/\n"), 0o644)
	_ = os.Chtimes(filepath.Join(dir, ".gitignore"), base, base)
	write("app/server.py", base.Add(1*time.Minute))
	write("web/main.js", base.Add(2*time.Minute))
	write("out/frame.png", base.Add(30*time.Minute)) // ignored: a render, not an edit

	anyAt, serverAt := projectChanged(dir)
	if !anyAt.Equal(base.Add(2*time.Minute)) {
		t.Errorf("any change = %v, want the JS edit at %v (the ignored render must not count)", anyAt, base.Add(2*time.Minute))
	}
	if !serverAt.Equal(base.Add(1 * time.Minute)) {
		t.Errorf("server change = %v, want the .py edit", serverAt)
	}
	if !hotReloading.MatchString("npx vite --host $HOST --port $PORT") || hotReloading.MatchString("exec python3 plugins/film-studio/bin/film studio --port $PORT") {
		t.Error("hot-reload detection is wrong")
	}
}
