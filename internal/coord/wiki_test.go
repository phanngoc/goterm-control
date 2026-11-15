package coord

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWikiBelongsToTheProjectAndLivesInTheDatabase(t *testing.T) {
	db := testDB(t)
	dir := t.TempDir()
	c, err := db.CreateProject("Docs", "", "bomclaw", dir, nil)
	if err != nil {
		t.Fatal(err)
	}

	m1, err := db.WriteWiki(c.ID, "design/gdd.md", "# Game Design\n\nbody", "", "bomclaw")
	if err != nil {
		t.Fatal(err)
	}
	// Not a file any more: the project's folder is left alone.
	if _, err := os.Stat(filepath.Join(c.Workspace, "wiki")); !os.IsNotExist(err) {
		t.Fatalf("a save wrote into the project folder: %v", err)
	}
	if _, err := db.WriteWiki(c.ID, "report.html", "<html><title>Báo cáo Q4</title><h1>x</h1></html>", "", "owner"); err != nil {
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
	if g := got["design/gdd.md"]; g.Title != "Game Design" || g.Format != "md" || g.UpdatedBy != "bomclaw" {
		t.Errorf("md page: %+v", g)
	}
	if got["report.html"].Title != "Báo cáo Q4" || got["report.html"].Format != "html" {
		t.Errorf("html page: %+v", got["report.html"])
	}

	body, mtime, err := db.ReadWiki(c.ID, "design/gdd.md")
	if err != nil || !strings.HasPrefix(body, "# Game Design") || mtime != m1 {
		t.Fatalf("read: %q %q %v (want mtime %q)", body, mtime, err, m1)
	}

	// A save against a stale token is refused, not merged.
	if _, err := db.WriteWiki(c.ID, "design/gdd.md", "new", "2001-01-01T00:00:00Z", "owner"); !errors.Is(err, ErrWikiConflict) {
		t.Fatalf("stale save: %v", err)
	}
	m2, err := db.WriteWiki(c.ID, "design/gdd.md", "new", m1, "owner")
	if err != nil {
		t.Fatalf("fresh save: %v", err)
	}
	if m2 == m1 {
		t.Fatal("a save did not move the conflict token")
	}
	// The second of two quick saves from the same opened copy loses.
	if _, err := db.WriteWiki(c.ID, "design/gdd.md", "newer", m1, "bomclaw2"); !errors.Is(err, ErrWikiConflict) {
		t.Fatalf("racing save: %v", err)
	}

	if err := db.RenameWiki(c.ID, "design/gdd.md", "gdd-v2.md"); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteWiki(c.ID, "gdd-v2.md"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.ReadWiki(c.ID, "gdd-v2.md"); !errors.Is(err, ErrWikiNotFound) {
		t.Fatalf("deleted page still reads: %v", err)
	}
}

// Wikis of two projects never see each other's pages.
func TestWikiIsPerProject(t *testing.T) {
	db := testDB(t)
	a, _ := db.CreateProject("A", "", "owner", t.TempDir(), nil)
	b, _ := db.CreateProject("B", "", "owner", t.TempDir(), nil)
	if _, err := db.WriteWiki(a.ID, "x.md", "# A", "", "owner"); err != nil {
		t.Fatal(err)
	}
	if pages, _ := db.ListWiki(b.ID); len(pages) != 0 {
		t.Fatalf("B sees A's pages: %+v", pages)
	}
	if _, err := db.ListWiki("ch_missing"); err == nil {
		t.Fatal("a wiki without a project")
	}
}

// A wiki kept in files before is imported the first time it is opened —
// pages, folders and pictures — and the files are left where they were.
func TestWikiImportsItsOldFilesOnce(t *testing.T) {
	db := testDB(t)
	shared := t.TempDir()
	old := SharedWikiRoot
	SharedWikiRoot = func() string { return shared }
	t.Cleanup(func() { SharedWikiRoot = old })

	parent := t.TempDir()
	dir := filepath.Join(parent, "arcade") // CreateProject puts the folder at <parent>/<slug>
	must := func(p, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must(filepath.Join(dir, "wiki", "overview.md"), "# Tổng quan")
	must(filepath.Join(dir, "wiki", "img", "map.png"), "\x89PNG")
	must(filepath.Join(dir, "wiki", ".secret.md"), "no")
	must(filepath.Join(dir, "wiki", "run.sh"), "no")
	c, _ := db.CreateProject("Arcade", "", "owner", parent, nil)
	if c.Workspace != dir {
		t.Fatalf("project folder %s, want %s", c.Workspace, dir)
	}
	ch, _ := db.CreateChannel("", "general", ChannelPublic, "", "system", nil)
	must(filepath.Join(shared, ch.ID, "notes.txt"), "hello")

	pages, err := db.ListWiki(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]WikiPage{}
	for _, p := range pages {
		paths[p.Path] = p
	}
	if len(paths) != 2 || paths["overview.md"].Title != "Tổng quan" || paths["img/map.png"].Format != "other" {
		t.Fatalf("import: %+v", pages)
	}
	if b, _, _ := db.ReadWikiBytes(c.ID, "img/map.png"); string(b) != "\x89PNG" {
		t.Fatalf("picture bytes: %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "wiki", "overview.md")); err != nil {
		t.Fatal("the old file was removed")
	}
	// Once: a page deleted after the import does not come back from its file.
	if err := db.DeleteWiki(c.ID, "overview.md"); err != nil {
		t.Fatal(err)
	}
	if pages, _ := db.ListWiki(c.ID); len(pages) != 1 {
		t.Fatalf("re-imported: %+v", pages)
	}
	if body, _, err := db.ReadWiki(ch.ID, "notes.txt"); err != nil || body != "hello" {
		t.Fatalf("shared wiki import: %q %v", body, err)
	}
}

func TestWikiRefusesBadPaths(t *testing.T) {
	db := testDB(t)
	c, err := db.CreateProject("Docs", "", "bomclaw", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"../AGENTS.md", "/etc/passwd.md", "a/../../x.md", ".hidden.md", "x/.git/config.md", "a/b/c/d/e.md", ""} {
		if _, err := db.WriteWiki(c.ID, bad, "x", "", "owner"); err == nil {
			t.Errorf("wrote %q", bad)
		}
	}
	if _, err := db.WriteWiki(c.ID, "run.sh", "x", "", "owner"); err == nil {
		t.Error("wrote a .sh page")
	}
	if err := db.DeleteWiki(c.ID, ""); err == nil {
		t.Error("deleted the wiki root")
	}
	if _, err := db.WriteWiki(c.ID, "big.md", strings.Repeat("x", wikiMaxBytes+1), "", "owner"); err == nil {
		t.Error("wrote a page over the cap")
	}
}
