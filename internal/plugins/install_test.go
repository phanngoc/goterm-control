package plugins

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// upstream builds a plugin folder the way an author would publish it.
func upstream(t *testing.T, name, cmd string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "src")
	writeFile(t, filepath.Join(dir, File), "name: "+name+"\ndescription: from upstream\n", 0o644)
	writeFile(t, filepath.Join(dir, "bin", cmd), "#!/bin/sh\necho hi\n", 0o755)
	skill(t, filepath.Join(dir, "skills"), "how-to", "Use when needed.")
	return dir
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestInstallFromFolderIsUntrustedUntilTrusted(t *testing.T) {
	ws, src := t.TempDir(), upstream(t, "tool", "tool-run")
	if err := os.Symlink("/etc/passwd", filepath.Join(src, "leak")); err != nil {
		t.Fatal(err)
	}

	inst, err := Install(ws, Source{URL: src}, InstallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if inst.Name != "tool" || inst.Trusted || len(inst.Skipped) != 1 {
		t.Fatalf("got %+v", inst)
	}
	if _, err := os.Lstat(filepath.Join(inst.Dir, "leak")); !os.IsNotExist(err) {
		t.Fatalf("a symlink was installed")
	}
	if info, err := os.Stat(filepath.Join(inst.Dir, "bin", "tool-run")); err != nil || info.Mode()&0o111 == 0 {
		t.Fatalf("lost the executable bit: %v %v", info, err)
	}

	k := Load(ws, "")
	p := mustFind(t, k, "tool")
	if p.Scope != ScopeInstalled || !p.Untrusted || len(k.Path) != 0 {
		t.Fatalf("installed plugin ran code before trust: scope=%s untrusted=%v path=%v", p.Scope, p.Untrusted, k.Path)
	}
	if len(k.Skills) != 1 {
		t.Fatalf("its skill should apply at once: %+v", k.Skills)
	}

	if _, err := Install(ws, Source{URL: src}, InstallOptions{}); err == nil {
		t.Fatal("a second install silently replaced the first")
	}
	if _, err := Install(ws, Source{URL: src}, InstallOptions{Replace: true, Trust: true}); err != nil {
		t.Fatal(err)
	}
	if k := Load(ws, ""); len(k.Path) != 1 {
		t.Fatalf("trusted install still held back: %v", k.Path)
	}
	// Leftovers from the swap must never look like plugins.
	entries, _ := os.ReadDir(filepath.Join(ws, Dir))
	for _, e := range entries {
		if e.Name() != "tool" {
			t.Errorf("stray entry after replace: %s", e.Name())
		}
	}
}

func TestInstallFromGitPinsCommitAndUpdateResetsTrust(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(repo, "plugins", "forge", File), "name: forge\nversion: 1.0.0\n", 0o644)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "v1")

	ws := t.TempDir()
	inst, err := Install(ws, Source{URL: "file://" + repo, Path: "plugins/forge"}, InstallOptions{Trust: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(inst.Source.Commit) != 40 {
		t.Fatalf("commit not recorded: %+v", inst.Source)
	}
	st, _ := LoadState(ws)
	if !st.trusted("forge") {
		t.Fatal("--trust not recorded")
	}

	writeFile(t, filepath.Join(repo, "plugins", "forge", File), "name: forge\nversion: 2.0.0\n", 0o644)
	git(t, repo, "commit", "-q", "-am", "v2")
	if _, err := Update(ws, "forge", false); err != nil {
		t.Fatal(err)
	}
	if p := mustFind(t, Load(ws, ""), "forge"); p.Version != "2.0.0" {
		t.Fatalf("update fetched %q", p.Version)
	}
	if st, _ := LoadState(ws); st.trusted("forge") {
		t.Fatal("trust survived new code arriving under the same name")
	}
}

func TestInstallUnderAnotherName(t *testing.T) {
	ws, src := t.TempDir(), upstream(t, "tool", "x")
	if _, err := Install(ws, Source{URL: src}, InstallOptions{Name: "tool-b"}); err != nil {
		t.Fatal(err)
	}
	if p := mustFind(t, Load(ws, ""), "tool-b"); p.Status != Active {
		t.Fatalf("renamed install: %s %v", p.Status, p.Reasons)
	}
}

func TestInstallRefusals(t *testing.T) {
	ws := t.TempDir()
	for _, src := range []Source{
		{URL: "--upload-pack=touch /tmp/x"},
		{URL: "ext::sh -c touch% /tmp/pwned"},
		{URL: "not a thing"},
		{URL: upstream(t, "ok", "x"), Path: "../.."},
		{URL: t.TempDir()}, // no manifest
	} {
		if _, err := Install(ws, src, InstallOptions{}); err == nil {
			t.Errorf("installed from %+v", src)
		}
	}
}

func TestUninstallForgetsSwitchesAndCopyKeepsUpstream(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	if _, err := Install(a, Source{URL: upstream(t, "tool", "x"), Ref: ""}, InstallOptions{Trust: true}); err != nil {
		t.Fatal(err)
	}
	orig, _ := ReadSource(filepath.Join(a, Dir, "tool"))

	inst, err := Copy(a, b, "tool")
	if err != nil {
		t.Fatal(err)
	}
	if inst.Source.URL != orig.URL || inst.Trusted {
		t.Fatalf("copy: %+v (want upstream %s, untrusted)", inst.Source, orig.URL)
	}

	st, _ := LoadState(a)
	st.SetConfig("tool", "token", "s")
	_ = st.Save(a)
	if err := Uninstall(a, "tool"); err != nil {
		t.Fatal(err)
	}
	st, _ = LoadState(a)
	if st.trusted("tool") || len(st.Config["tool"]) != 0 {
		t.Fatalf("uninstall kept state: %+v", st)
	}
	if err := Uninstall(a, "tool"); err == nil || !strings.Contains(err.Error(), "no plugin") {
		t.Fatalf("second uninstall: %v", err)
	}
}
