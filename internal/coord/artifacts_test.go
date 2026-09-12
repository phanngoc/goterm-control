package coord

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func putTestArtifact(t *testing.T, db *DB, taskID, kind, title, name, body string) *Artifact {
	t.Helper()
	a, err := db.PutArtifact(NewArtifact{
		TaskID: taskID, Kind: kind, Title: title, Filename: name,
		Content: []byte(body), CreatedBy: "a1",
	})
	if err != nil {
		t.Fatalf("put %s: %v", title, err)
	}
	return a
}

func TestArtifactRoundTrip(t *testing.T) {
	db := testDB(t)
	task, _ := db.CreateTask(NewTask{CreatedBy: "human", Title: "Fix the scanner"})

	body := strings.Repeat("diff --git a/x b/x\n", 200)
	a := putTestArtifact(t, db, task.ID, ArtifactPatch, "scanner cleanup", "cleanup.patch", body)

	if a.ContextID != task.ContextID {
		t.Errorf("context = %q, want the task's %q", a.ContextID, task.ContextID)
	}
	if a.Bytes != int64(len(body)) {
		t.Errorf("bytes = %d, want %d", a.Bytes, len(body))
	}
	// The preview is an index entry, not the content: it must be short, and
	// the full bytes must still come back intact.
	if n := len([]rune(a.Preview)); n > PreviewRunes {
		t.Errorf("preview is %d runes, want at most %d", n, PreviewRunes)
	}
	got, err := db.ReadArtifact(a.ID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != body {
		t.Errorf("content came back changed (%d bytes vs %d)", len(got), len(body))
	}
	if _, err := os.Stat(filepath.Join(db.ArtifactsDir(), a.Path)); err != nil {
		t.Errorf("file not on disk: %v", err)
	}
}

func TestArtifactFilenameCannotEscapeItsTask(t *testing.T) {
	db := testDB(t)
	task, _ := db.CreateTask(NewTask{CreatedBy: "human", Title: "Anything"})

	a := putTestArtifact(t, db, task.ID, ArtifactFile, "sneaky", "../../../../.ssh/authorized_keys", "ssh-rsa AAAA")
	if strings.Contains(a.Path, "..") {
		t.Fatalf("path escaped its directory: %q", a.Path)
	}
	want := filepath.Join(task.ContextID, task.ID)
	if !strings.HasPrefix(a.Path, want) {
		t.Errorf("path = %q, want it under %q", a.Path, want)
	}
}

func TestArtifactsWithTheSameNameDoNotOverwrite(t *testing.T) {
	db := testDB(t)
	task, _ := db.CreateTask(NewTask{CreatedBy: "human", Title: "Two reports"})

	first := putTestArtifact(t, db, task.ID, ArtifactDocument, "report", "report.md", "first")
	second := putTestArtifact(t, db, task.ID, ArtifactDocument, "report", "report.md", "second")

	if first.Path == second.Path {
		t.Fatalf("both landed on %q — one overwrote the other", first.Path)
	}
	got, _ := db.ReadArtifact(first.ID)
	if string(got) != "first" {
		t.Errorf("first artifact now reads %q", got)
	}
}

func TestTaskArtifactsSeparatesInputsFromOutputs(t *testing.T) {
	db := testDB(t)
	root, _ := db.CreateTask(NewTask{CreatedBy: "human", Title: "Ship the page", AssignedTo: "a1"})
	parent, _ := claimStart(t, db, "a1")

	plan := putTestArtifact(t, db, parent.ID, ArtifactDocument, "plan", "plan.md", "1. build 2. copy")
	child, err := db.CreateSubTask(parent.ID, "a1", NewTask{
		Title:  "Build the form",
		Body:   "Build the signup form described in the plan artifact; markup and validation only, no styling.",
		Inputs: []string{plan.ID},
	})
	if err != nil {
		t.Fatalf("sub: %v", err)
	}
	out := putTestArtifact(t, db, child.ID, ArtifactPatch, "form", "form.patch", "diff")

	arts, err := db.TaskArtifacts(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	roles := map[string]string{}
	for _, a := range arts {
		roles[a.ID] = a.Role
	}
	if roles[plan.ID] != RoleInput {
		t.Errorf("plan role = %q, want %q — the parent handed it down", roles[plan.ID], RoleInput)
	}
	if roles[out.ID] != RoleOutput {
		t.Errorf("form role = %q, want %q", roles[out.ID], RoleOutput)
	}

	// The whole tree is visible from the context, which is how a parent sees
	// what its children made without any child pushing content upward.
	tree, err := db.ContextArtifacts(root.ContextID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tree) != 2 {
		t.Errorf("context has %d artifacts, want 2", len(tree))
	}
}

func TestPutArtifactRejectsNonsense(t *testing.T) {
	db := testDB(t)
	task, _ := db.CreateTask(NewTask{CreatedBy: "human", Title: "T"})

	cases := []struct {
		name string
		in   NewArtifact
	}{
		{"no title", NewArtifact{TaskID: task.ID, Content: []byte("x")}},
		{"no task", NewArtifact{Title: "x", Content: []byte("x")}},
		{"unknown kind", NewArtifact{TaskID: task.ID, Title: "x", Kind: "sketch", Content: []byte("x")}},
		{"link without url", NewArtifact{TaskID: task.ID, Title: "x", Kind: ArtifactLink}},
		{"file without content", NewArtifact{TaskID: task.ID, Title: "x", Kind: ArtifactPatch}},
	}
	for _, c := range cases {
		if _, err := db.PutArtifact(c.in); err == nil {
			t.Errorf("%s: accepted, want an error", c.name)
		}
	}
}

// age moves a task's clock back so a test does not have to wait days.
func age(t *testing.T, db *DB, taskID string, d time.Duration) {
	t.Helper()
	when := time.Now().Add(-d).UTC().Format(time.RFC3339Nano)
	if _, err := db.Conn().Exec(`UPDATE tasks SET updated_at = ? WHERE id = ?`, when, taskID); err != nil {
		t.Fatalf("age %s: %v", taskID, err)
	}
}

// TestPurgeTakesFinishedTreesAndKeepsDocuments covers the three rules at once,
// because they only make sense against each other.
func TestPurgeTakesFinishedTreesAndKeepsDocuments(t *testing.T) {
	db := testDB(t)

	// An old, finished tree: a patch (goes) and a report (stays).
	done, _ := db.CreateTask(NewTask{CreatedBy: "human", Title: "old work", AssignedTo: "a1"})
	patch, err := db.PutArtifact(NewArtifact{TaskID: done.ID, Kind: ArtifactPatch, Title: "the diff", Content: []byte("--- a\n+++ b\n")})
	if err != nil {
		t.Fatalf("put patch: %v", err)
	}
	report, err := db.PutArtifact(NewArtifact{TaskID: done.ID, Kind: ArtifactDocument, Title: "the report", Content: []byte("# findings")})
	if err != nil {
		t.Fatalf("put report: %v", err)
	}
	if err := db.CancelTask(done.ID, "a1"); err != nil {
		t.Fatal(err)
	}
	age(t, db, done.ID, 60*24*time.Hour)

	// An equally old tree that is still open: nothing of it may go.
	open, _ := db.CreateTask(NewTask{CreatedBy: "human", Title: "still going", AssignedTo: "a1"})
	live, err := db.PutArtifact(NewArtifact{TaskID: open.ID, Kind: ArtifactPatch, Title: "wip", Content: []byte("wip")})
	if err != nil {
		t.Fatalf("put wip: %v", err)
	}
	age(t, db, open.ID, 60*24*time.Hour)

	// A finished tree from this morning: too young.
	recent, _ := db.CreateTask(NewTask{CreatedBy: "human", Title: "yesterday", AssignedTo: "a1"})
	fresh, err := db.PutArtifact(NewArtifact{TaskID: recent.ID, Kind: ArtifactPatch, Title: "recent", Content: []byte("new")})
	if err != nil {
		t.Fatalf("put recent: %v", err)
	}
	if err := db.CancelTask(recent.ID, "a1"); err != nil {
		t.Fatal(err)
	}

	patchFile := filepath.Join(db.ArtifactsDir(), patch.Path)
	if _, err := os.Stat(patchFile); err != nil {
		t.Fatalf("patch file should exist before the purge: %v", err)
	}

	// Dry run must name exactly what the purge then takes.
	preview, err := db.PurgeableArtifacts(time.Now().AddDate(0, 0, -30))
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if len(preview) != 1 || preview[0].ID != patch.ID {
		t.Fatalf("dry run should list only the old patch, got %+v", preview)
	}

	rows, files, err := db.PurgeArtifacts(time.Now().AddDate(0, 0, -30))
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if rows != 1 || files != 1 {
		t.Fatalf("expected 1 row and 1 file, got %d rows and %d files", rows, files)
	}
	if _, err := os.Stat(patchFile); !os.IsNotExist(err) {
		t.Error("the patch file is still on disk")
	}
	for _, keep := range []*Artifact{report, live, fresh} {
		if _, err := db.GetArtifact(keep.ID); err != nil {
			t.Errorf("%s (%s) should have been kept: %v", keep.Title, keep.Kind, err)
		}
	}
	if _, err := db.GetArtifact(patch.ID); err == nil {
		t.Error("the purged artifact still has a row")
	}
}

// TestDryRunDeletesNothing: the flag is the whole point of the flag.
func TestDryRunDeletesNothing(t *testing.T) {
	db := testDB(t)
	task, _ := db.CreateTask(NewTask{CreatedBy: "human", Title: "old", AssignedTo: "a1"})
	a, err := db.PutArtifact(NewArtifact{TaskID: task.ID, Kind: ArtifactFile, Title: "thing", Content: []byte("bytes")})
	if err != nil {
		t.Fatal(err)
	}
	db.CancelTask(task.ID, "a1")
	age(t, db, task.ID, 60*24*time.Hour)

	if _, err := db.PurgeableArtifacts(time.Now().AddDate(0, 0, -30)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetArtifact(a.ID); err != nil {
		t.Fatalf("the dry run deleted the row: %v", err)
	}
	if _, err := os.Stat(filepath.Join(db.ArtifactsDir(), a.Path)); err != nil {
		t.Fatalf("the dry run deleted the file: %v", err)
	}
}

// TestPurgeRefusesPathsOutsideTheRoot: filenames are sanitised on the way in,
// so this guards a row written before that, or by hand.
func TestPurgeRefusesPathsOutsideTheRoot(t *testing.T) {
	db := testDB(t)
	outside := filepath.Join(t.TempDir(), "precious.txt")
	if err := os.WriteFile(outside, []byte("not yours"), 0o644); err != nil {
		t.Fatal(err)
	}

	task, _ := db.CreateTask(NewTask{CreatedBy: "human", Title: "old", AssignedTo: "a1"})
	a, err := db.PutArtifact(NewArtifact{TaskID: task.ID, Kind: ArtifactFile, Title: "thing", Content: []byte("bytes")})
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(db.ArtifactsDir(), outside)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec(`UPDATE artifacts SET path = ? WHERE id = ?`, rel, a.ID); err != nil {
		t.Fatal(err)
	}
	db.CancelTask(task.ID, "a1")
	age(t, db, task.ID, 60*24*time.Hour)

	if _, _, err := db.PurgeArtifacts(time.Now().AddDate(0, 0, -30)); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("the purge deleted a file outside the artifacts root")
	}
}
