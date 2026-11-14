package coord

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWikiInAProjectLivesInItsFolder(t *testing.T) {
	db := testDB(t)
	dir := t.TempDir()
	c, err := db.CreateProject("Docs", "", "bomclaw", dir, nil)
	if err != nil {
		t.Fatal(err)
	}

	m1, err := db.WriteWiki(c.ID, "design/gdd.md", "# Game Design\n\nbody", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(c.Workspace, "wiki", "design", "gdd.md")); err != nil {
		t.Fatalf("page not in <project>/wiki: %v", err)
	}
	if _, err := db.WriteWiki(c.ID, "report.html", "<html><title>Báo cáo Q4</title><h1>x</h1></html>", ""); err != nil {
		t.Fatal(err)
	}

	pages, err := db.ListWiki(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]WikiPage{}
	for _, p := range pages {
		got[p.Path] = p
	}
	if got["design/gdd.md"].Title != "Game Design" || got["design/gdd.md"].Format != "md" {
		t.Errorf("md page: %+v", got["design/gdd.md"])
	}
	if got["report.html"].Title != "Báo cáo Q4" || got["report.html"].Format != "html" {
		t.Errorf("html page: %+v", got["report.html"])
	}

	body, mtime, err := db.ReadWiki(c.ID, "design/gdd.md")
	if err != nil || !strings.HasPrefix(body, "# Game Design") || mtime != m1 {
		t.Fatalf("read: %q %q %v (want mtime %q)", body, mtime, err, m1)
	}

	// A save against a stale mtime is refused, not merged.
	if _, err := db.WriteWiki(c.ID, "design/gdd.md", "new", "2001-01-01T00:00:00Z"); !errors.Is(err, ErrWikiConflict) {
		t.Fatalf("stale save: %v", err)
	}
	if _, err := db.WriteWiki(c.ID, "design/gdd.md", "new", m1); err != nil {
		t.Fatalf("fresh save: %v", err)
	}

	if err := db.RenameWiki(c.ID, "design/gdd.md", "gdd-v2.md"); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteWiki(c.ID, "gdd-v2.md"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.ReadWiki(c.ID, "gdd-v2.md"); err == nil {
		t.Fatal("deleted page still reads")
	}
}

func TestWikiForARoomWithoutAFolder(t *testing.T) {
	db := testDB(t)
	shared := t.TempDir()
	old := SharedWikiRoot
	SharedWikiRoot = func() string { return shared }
	t.Cleanup(func() { SharedWikiRoot = old })

	ch, err := db.CreateChannel("", "general", ChannelPublic, "", "system", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.WriteWiki(ch.ID, "notes.txt", "hello", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(shared, ch.ID, "notes.txt")); err != nil {
		t.Fatalf("not in the shared wiki: %v", err)
	}
}

func TestWikiRefusesPathsOutside(t *testing.T) {
	db := testDB(t)
	c, err := db.CreateProject("Docs", "", "bomclaw", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"../AGENTS.md", "/etc/passwd.md", "a/../../x.md", ".hidden.md", "x/.git/config.md"} {
		if _, err := db.WriteWiki(c.ID, bad, "x", ""); err == nil {
			t.Errorf("wrote %q", bad)
		}
	}
	if _, err := db.WriteWiki(c.ID, "run.sh", "x", ""); err == nil {
		t.Error("wrote a .sh page")
	}
	if err := db.DeleteWiki(c.ID, ""); err == nil {
		t.Error("deleted the wiki root")
	}
	// A link inside the wiki pointing out of it does not become a way out.
	root, _ := db.WikiDir(c.ID)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "out")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.WriteWiki(c.ID, "out/x.md", "x", ""); err == nil {
		t.Error("wrote through a symlink out of the wiki")
	}
}
