package deploy

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A deploy's message names the commit, and PROD's status reads it back.
func TestDeployMessageRoundTrip(t *testing.T) {
	g := &GitVersion{Short: "abc1234", Subject: "fix the header"}
	msg := deployMessage(g)
	if msg != "bomclaw git abc1234: fix the header" {
		t.Fatalf("message %q", msg)
	}
	d := Deployment{Annotations: map[string]string{"workers/message": msg}, Source: "wrangler"}
	d.Versions = append(d.Versions, struct {
		VersionID  string  `json:"version_id"`
		Percentage float64 `json:"percentage"`
	}{"v-old", 10}, struct {
		VersionID  string  `json:"version_id"`
		Percentage float64 `json:"percentage"`
	}{"v-new", 90})
	v := d.Version()
	if v.ID != "v-new" || v.Commit != "abc1234" || v.By != "wrangler" {
		t.Fatalf("version %+v", v)
	}
	if c := commitIn(imageLabel(&GitVersion{Short: "def5678", Dirty: 2})); c != "def5678" {
		t.Fatalf("image label commit %q", c)
	}
	if deployMessage(nil) != "" || commitIn("deployed by hand") != "" {
		t.Fatal("no git, no commit")
	}
}

// DEV's checkout and how far it is ahead of a commit.
func TestGitAndCommitsSince(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if Git(context.Background(), dir) != nil {
		t.Fatal("not a checkout yet")
	}
	run("init", "-q")
	os.WriteFile(filepath.Join(dir, "a"), []byte("1"), 0o644)
	run("add", "a")
	run("commit", "-qm", "first")
	first := Git(context.Background(), dir)
	if first == nil || first.Subject != "first" || first.Dirty != 0 {
		t.Fatalf("first %+v", first)
	}
	os.WriteFile(filepath.Join(dir, "a"), []byte("2"), 0o644)
	run("commit", "-qam", "second")
	os.WriteFile(filepath.Join(dir, "b"), []byte("x"), 0o644)
	now := Git(context.Background(), dir)
	if now.Dirty != 1 || now.Subject != "second" {
		t.Fatalf("now %+v", now)
	}
	if n := CommitsSince(context.Background(), dir, first.Short); n != 1 {
		t.Fatalf("ahead %d, want 1", n)
	}
	if n := CommitsSince(context.Background(), dir, "0000000"); n != -1 {
		t.Fatalf("unknown commit: %d, want -1", n)
	}
}
