package coord

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func projectWith(t *testing.T, files map[string]string) (*DB, *Channel) {
	t.Helper()
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw")
	c, err := db.CreateProject("Trading", "", "bomclaw", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		path := filepath.Join(c.Workspace, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return db, c
}

func TestProjectFilesListsDirectoriesFirst(t *testing.T) {
	db, c := projectWith(t, map[string]string{
		"zebra.md":       "z",
		"src/main.go":    "package main",
		"scripts/run.sh": "#!/bin/sh",
		"README.md":      "hi",
	})
	entries, err := db.ProjectFiles(c.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name)
	}
	// AGENTS.md is seeded, so: the two directories, then the files by name.
	if len(names) < 4 || !entries[0].Dir || !entries[1].Dir {
		t.Fatalf("directories should come first: %v", names)
	}
	if entries[0].Name != "scripts" || entries[1].Name != "src" {
		t.Fatalf("directories should be sorted by name: %v", names)
	}

	// And a subdirectory lists with paths the caller can pass straight back.
	sub, err := db.ProjectFiles(c.ID, "src")
	if err != nil {
		t.Fatal(err)
	}
	if len(sub) != 1 || sub[0].Path != "src/main.go" {
		t.Fatalf("subdirectory listing: %+v", sub)
	}
}

// The guard is not decoration: the path comes from a browser, and
// "../../.ssh/id_rsa" is the first thing anyone tries.
func TestProjectFilesCannotEscapeTheWorkspace(t *testing.T) {
	db, c := projectWith(t, map[string]string{"README.md": "hi"})
	secret := filepath.Join(filepath.Dir(c.Workspace), "secret.txt")
	if err := os.WriteFile(secret, []byte("không phải của bạn"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, bad := range []string{"../secret.txt", "../../etc/passwd", "/etc/passwd", "src/../../secret.txt"} {
		if _, _, _, err := db.ReadProjectFile(c.ID, bad); err == nil {
			t.Errorf("%q was read", bad)
		}
		if _, err := db.ProjectFiles(c.ID, bad); err == nil {
			t.Errorf("%q was listed", bad)
		}
	}
}

// A symlink inside the folder pointing out of it passes a string check and
// must not pass this one.
func TestASymlinkOutOfTheWorkspaceIsRefused(t *testing.T) {
	db, c := projectWith(t, map[string]string{"README.md": "hi"})
	outside := filepath.Join(filepath.Dir(c.Workspace), "outside.txt")
	if err := os.WriteFile(outside, []byte("bí mật"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(c.Workspace, "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if body, _, _, err := db.ReadProjectFile(c.ID, "link.txt"); err == nil {
		t.Fatalf("a symlink led outside the folder and returned %q", body)
	}
}

func TestReadProjectFileReportsBinaryRatherThanMangleIt(t *testing.T) {
	db, c := projectWith(t, map[string]string{"logo.png": "\x89PNG\x00\x01\x02binary"})
	_, _, binary, err := db.ReadProjectFile(c.ID, "logo.png")
	if err != nil {
		t.Fatal(err)
	}
	if !binary {
		t.Fatal("a binary file was returned as text")
	}
}

func TestReadProjectFileSaysWhenItCut(t *testing.T) {
	db, c := projectWith(t, map[string]string{"big.txt": strings.Repeat("a", MaxProjectFileBytes+500)})
	body, truncated, binary, err := db.ReadProjectFile(c.ID, "big.txt")
	if err != nil {
		t.Fatal(err)
	}
	if binary {
		t.Fatal("text was called binary")
	}
	if !truncated {
		t.Fatal("a cut file did not say so")
	}
	if len(body) != MaxProjectFileBytes {
		t.Fatalf("read %d bytes, cap is %d", len(body), MaxProjectFileBytes)
	}
}

// A room with no folder has nothing to browse and says so.
func TestBrowsingAPlainRoomIsRefused(t *testing.T) {
	db := testDB(t)
	if _, err := db.ProjectFiles(GeneralChannelID, ""); err == nil {
		t.Fatal("#general was browsable")
	}
}
