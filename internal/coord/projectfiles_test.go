package coord

import (
	"fmt"
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

func TestWriteProjectFileReplacesAndCreates(t *testing.T) {
	db, c := projectWith(t, map[string]string{"src/main.go": "package main"})

	if err := db.WriteProjectFile(c.ID, "src/main.go", "package main // sửa rồi"); err != nil {
		t.Fatal(err)
	}
	body, _, _, err := db.ReadProjectFile(c.ID, "src/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if body != "package main // sửa rồi" {
		t.Fatalf("read back %q", body)
	}

	// A new file, in a directory that does not exist yet.
	if err := db.WriteProjectFile(c.ID, "docs/notes.md", "# ghi chú"); err != nil {
		t.Fatal(err)
	}
	if body, _, _, _ := db.ReadProjectFile(c.ID, "docs/notes.md"); body != "# ghi chú" {
		t.Fatalf("new file: %q", body)
	}

	// The save left nothing behind: a half-written temp file in a project
	// folder is something an agent will eventually read and believe.
	entries, _ := os.ReadDir(filepath.Join(c.Workspace, "src"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".edit-") {
			t.Fatalf("temp file survived: %s", e.Name())
		}
	}
}

// The same guard as the reads, because a write that escapes is worse than a
// read that does.
func TestWriteProjectFileCannotEscape(t *testing.T) {
	db, c := projectWith(t, nil)
	outside := filepath.Join(filepath.Dir(c.Workspace), "victim.txt")
	if err := os.WriteFile(outside, []byte("nguyên vẹn"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"../victim.txt", "/tmp/victim.txt", "src/../../victim.txt"} {
		if err := db.WriteProjectFile(c.ID, bad, "bị ghi đè"); err == nil {
			t.Errorf("%q was written", bad)
		}
	}
	if got, _ := os.ReadFile(outside); string(got) != "nguyên vẹn" {
		t.Fatal("a file outside the project was overwritten")
	}
}

// The browser cannot show a binary file, so a save landing on one is a save
// made blind — and the outcome is a destroyed asset with no undo.
func TestWritingOverABinaryFileIsRefused(t *testing.T) {
	db, c := projectWith(t, map[string]string{"logo.png": "\x89PNG\x00\x01binary"})
	err := db.WriteProjectFile(c.ID, "logo.png", "oops")
	if err == nil {
		t.Fatal("a binary file was overwritten with text")
	}
	if !strings.Contains(err.Error(), "binary") {
		t.Errorf("the error should say why: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(c.Workspace, "logo.png"))
	if !strings.Contains(string(body), "PNG") {
		t.Fatal("the file was damaged anyway")
	}
}

// An empty document is a document. A field that could not tell "" from "not
// writing" would make clearing a file impossible.
func TestAnEmptyFileCanBeSaved(t *testing.T) {
	db, c := projectWith(t, map[string]string{"notes.md": "có nội dung"})
	if err := db.WriteProjectFile(c.ID, "notes.md", ""); err != nil {
		t.Fatal(err)
	}
	if body, _, _, _ := db.ReadProjectFile(c.ID, "notes.md"); body != "" {
		t.Fatalf("clearing the file left %q", body)
	}
}

func TestAFileCreatedThroughASymlinkOutIsRefused(t *testing.T) {
	db, c := projectWith(t, nil)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(c.Workspace, "out")); err != nil {
		t.Fatal(err)
	}
	if err := db.WriteProjectFile(c.ID, "out/new.txt", "x"); err == nil {
		t.Fatal("a new file under a link pointing out of the project was written")
	}
	if err := db.MakeProjectDir(c.ID, "out/sub"); err == nil {
		t.Fatal("a folder under a link pointing out of the project was created")
	}
	if _, err := os.Stat(filepath.Join(outside, "new.txt")); err == nil {
		t.Fatal("the file landed outside the project")
	}
}

func TestMakeRenameDeleteInsideAProject(t *testing.T) {
	db, c := projectWith(t, map[string]string{"a.md": "A", "keep/x.txt": "X"})

	if err := db.MakeProjectDir(c.ID, "docs/deep"); err != nil {
		t.Fatal(err)
	}
	if err := db.MakeProjectDir(c.ID, "docs/deep"); err == nil {
		t.Fatal("making an existing folder was not refused")
	}
	if err := db.RenameProjectPath(c.ID, "a.md", "docs/a.md"); err != nil {
		t.Fatal(err)
	}
	if body, _, _, _ := db.ReadProjectFile(c.ID, "docs/a.md"); body != "A" {
		t.Fatalf("renamed file reads %q", body)
	}
	if err := db.RenameProjectPath(c.ID, "docs/a.md", "keep/x.txt"); err == nil {
		t.Fatal("a rename onto an existing file was not refused")
	}
	if err := db.RenameProjectPath(c.ID, "keep/x.txt", "../x.txt"); err == nil {
		t.Fatal("a rename out of the project was not refused")
	}
	if err := db.DeleteProjectPath(c.ID, "docs"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(c.Workspace, "docs")); !os.IsNotExist(err) {
		t.Fatal("deleted folder is still there")
	}
	for _, bad := range []string{"", ".", "..", "../keep", "/etc"} {
		if err := db.DeleteProjectPath(c.ID, bad); err == nil {
			t.Fatalf("delete %q was not refused", bad)
		}
	}
	if _, err := os.Stat(filepath.Join(c.Workspace, "keep", "x.txt")); err != nil {
		t.Fatal("a refused delete removed something")
	}
}

func TestDeletingASymlinkRemovesTheLinkNotItsTarget(t *testing.T) {
	db, c := projectWith(t, map[string]string{"v2/data.json": "{}"})
	if err := os.Symlink(filepath.Join(c.Workspace, "v2"), filepath.Join(c.Workspace, "current")); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteProjectPath(c.ID, "current"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(c.Workspace, "v2", "data.json")); err != nil {
		t.Fatal("deleting the link deleted what it pointed at")
	}
	if _, err := os.Lstat(filepath.Join(c.Workspace, "current")); !os.IsNotExist(err) {
		t.Fatal("the link is still there")
	}
}

func TestAFileTooLargeToShowWholeCannotBeSavedOver(t *testing.T) {
	db, c := projectWith(t, map[string]string{"big.log": strings.Repeat("x", MaxProjectFileBytes+1)})
	if err := db.WriteProjectFile(c.ID, "big.log", "cut"); err == nil {
		t.Fatal("a save over a file larger than what the editor received was not refused")
	}
}

func TestTheFileIndexSkipsToolCaches(t *testing.T) {
	db, c := projectWith(t, map[string]string{
		"a.py": "", "src/b.go": "", "node_modules/x/index.js": "", ".git/HEAD": "", "src/__pycache__/b.pyc": "",
	})
	paths, truncated, err := db.ProjectFileIndex(c.ID)
	if err != nil || truncated {
		t.Fatal(err, truncated)
	}
	if strings.Join(paths, ",") != "AGENTS.md,a.py,src/b.go" {
		t.Fatalf("index = %v", paths)
	}
}

func TestSearchProjectFindsLinesWithPositions(t *testing.T) {
	db, c := projectWith(t, map[string]string{
		"main.go":    "package main\n\nfunc Fetch() {}\n// fetch again\n",
		"notes.md":   "Việt Nam fetch dữ liệu\n",
		"blob.bin":   "fetch\x00\x00",
		"big.json":   strings.Repeat("fetch ", maxSearchFileBytes/6+10),
		"vendor.txt": "prefetcher\n",
	})
	m, skipped, _, err := db.SearchProject(c.ID, "fetch", SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 1 {
		t.Errorf("skipped = %d, want the one big file", skipped)
	}
	got := map[string]SearchMatch{}
	for _, x := range m {
		got[fmt.Sprintf("%s:%d", x.Path, x.Line)] = x
	}
	if len(m) != 4 {
		t.Fatalf("case-insensitive matches = %v", m)
	}
	if x := got["notes.md:1"]; x.Col != 10 || x.Len != 5 || []rune(x.Text)[x.At] != 'f' {
		t.Errorf("column counted in bytes, not characters: %+v", x)
	}
	if _, ok := got["blob.bin:1"]; ok {
		t.Error("a binary file was searched")
	}

	m, _, _, _ = db.SearchProject(c.ID, "Fetch", SearchOptions{CaseSensitive: true})
	if len(m) != 1 || m[0].Line != 3 {
		t.Errorf("case-sensitive = %v", m)
	}
	m, _, _, _ = db.SearchProject(c.ID, "fetch", SearchOptions{WholeWord: true})
	for _, x := range m {
		if x.Path == "vendor.txt" {
			t.Error("whole word matched inside prefetcher")
		}
	}
	m, _, _, _ = db.SearchProject(c.ID, `func \w+\(`, SearchOptions{Regex: true})
	if len(m) != 1 {
		t.Errorf("regex = %v", m)
	}
	if _, _, _, err := db.SearchProject(c.ID, "(", SearchOptions{Regex: true}); err == nil {
		t.Error("a bad regex was not reported")
	}
}

func TestALongLineIsCutAroundItsMatch(t *testing.T) {
	x := searchMatch("min.js", 1, strings.Repeat("a", 5000)+"NEEDLE"+strings.Repeat("b", 5000), []int{5000, 5006})
	if len([]rune(x.Text)) > 245 || string([]rune(x.Text)[x.At:x.At+6]) != "NEEDLE" || x.Col != 5001 {
		t.Fatalf("cut line = %d runes, at=%d col=%d", len([]rune(x.Text)), x.At, x.Col)
	}
}
