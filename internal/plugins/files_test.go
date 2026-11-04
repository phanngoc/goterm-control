package plugins

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTreeAndReadStayInsideThePlugin(t *testing.T) {
	ws := t.TempDir()
	dir := plugin(t, filepath.Join(ws, Dir), "forge", "name: forge\n")
	writeFile(t, filepath.Join(dir, "bin", cmdFile("forge-pr")), "#!/bin/sh\necho pr\n", 0o755)
	writeFile(t, filepath.Join(dir, "skills", "review", "SKILL.md"), "# review\n", 0o644)
	writeFile(t, filepath.Join(dir, "logo.png"), "\x89PNG\x00\x00", 0o644)
	writeFile(t, filepath.Join(dir, ".git", "HEAD"), "ref", 0o644)
	secret := filepath.Join(ws, "plugins.yaml")
	writeFile(t, secret, "config: {forge: {token: s3cret}}", 0o600)
	linked := runtime.GOOS != "windows" && os.Symlink(secret, filepath.Join(dir, "leak")) == nil

	entries, truncated, err := Tree(dir)
	if err != nil || truncated {
		t.Fatalf("tree: %v %v", err, truncated)
	}
	var paths []string
	for _, e := range entries {
		paths = append(paths, e.Path)
	}
	got := strings.Join(paths, " ")
	want := "bin bin/" + cmdFile("forge-pr") + " skills skills/review skills/review/SKILL.md"
	if !strings.HasPrefix(got, want) || strings.Contains(got, ".git") {
		t.Fatalf("tree order = %q", got)
	}
	for _, e := range entries {
		if e.Path == "bin/"+cmdFile("forge-pr") && e.Mode != "x" {
			t.Error("the command is not marked executable")
		}
		if e.Path == "leak" && e.Link == "" {
			t.Error("a symlink was not reported as one")
		}
	}

	body, _, binary, err := ReadFile(dir, "skills/review/SKILL.md")
	if err != nil || binary || body != "# review\n" {
		t.Fatalf("read: %q %v %v", body, binary, err)
	}
	if _, _, binary, _ := ReadFile(dir, "logo.png"); !binary {
		t.Error("a binary file was not reported as binary")
	}
	for _, rel := range []string{"../plugins.yaml", "/etc/passwd", "", "bin"} {
		if _, _, _, err := ReadFile(dir, rel); err == nil {
			t.Errorf("read %q succeeded", rel)
		}
	}
	if linked {
		if _, _, _, err := ReadFile(dir, "leak"); err == nil {
			t.Error("a symlink to the agent's secrets was followed out of the plugin")
		}
	}
}
