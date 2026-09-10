package coord

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
