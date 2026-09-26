package coord

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A room people make for a piece of work is a project: somewhere on disk for
// the work to land, and a brief saying what the work is.
func TestCreateProjectMakesAFolderAndABrief(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw")
	dir := t.TempDir()

	c, err := db.CreateProject("Trading", "Bot giao dịch và nghiên cứu thị trường", "bomclaw", dir,
		[]Member{{Kind: MemberAgent, ID: "bomclaw"}})
	if err != nil {
		t.Fatal(err)
	}
	if c.Workspace == "" {
		t.Fatal("a project with no folder is just a room")
	}
	if _, err := os.Stat(c.Workspace); err != nil {
		t.Fatalf("the folder was not created: %v", err)
	}
	brief, err := os.ReadFile(filepath.Join(c.Workspace, AgentsFile))
	if err != nil {
		t.Fatalf("no brief: %v", err)
	}
	for _, want := range []string{"Trading", "Bot giao dịch", "Mục tiêu", "Vai trò"} {
		if !strings.Contains(string(brief), want) {
			t.Errorf("the seeded brief is missing %q", want)
		}
	}

	// And the row remembers where it is, so a prompt can point an agent at it.
	again, err := db.GetChannel(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.Workspace != c.Workspace {
		t.Fatalf("workspace lost: %q", again.Workspace)
	}
	if got := db.ProjectBrief(c.ID); !strings.Contains(got, "Trading") {
		t.Errorf("ProjectBrief did not read it back: %q", got)
	}
}

// Making the same project twice gives the same project — and above all does
// not overwrite a brief people have written.
func TestCreateProjectIsIdempotentAndKeepsTheBrief(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw")
	dir := t.TempDir()

	first, err := db.CreateProject("Trading", "", "bomclaw", dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	written := "# Trading\n\nĐã có người viết cái này bằng tay.\n"
	if err := os.WriteFile(filepath.Join(first.Workspace, AgentsFile), []byte(written), 0o644); err != nil {
		t.Fatal(err)
	}

	second, err := db.CreateProject("Trading", "", "bomclaw", dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("a second project appeared: %s vs %s", second.ID, first.ID)
	}
	if got := db.ProjectBrief(first.ID); got != written {
		t.Fatalf("the brief was overwritten:\n%s", got)
	}
}

// #general and DMs stay rooms. Giving the catch-all room a repository would
// invite work into the one place that exists for things too small to organise.
func TestAPlainRoomHasNoFolder(t *testing.T) {
	db := testDB(t)
	c, err := db.GetChannel(GeneralChannelID)
	if err != nil {
		t.Fatal(err)
	}
	if c.Workspace != "" {
		t.Fatalf("#general was given a project folder: %q", c.Workspace)
	}
	if db.ProjectBrief(GeneralChannelID) != "" {
		t.Error("a room with no folder reported a brief")
	}
}

// The board is unreadable once it shows every task on the machine, so it has
// to be scopeable — including to the work that belongs to no project.
func TestTasksScopeToTheirProject(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw")
	dir := t.TempDir()
	trading, _ := db.CreateProject("Trading", "", "bomclaw", dir, nil)
	other, _ := db.CreateProject("Mobile", "", "bomclaw", dir, nil)

	if _, err := db.CreateTask(NewTask{CreatedBy: "bomclaw", Title: "backtest", ChannelID: trading.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateTask(NewTask{CreatedBy: "bomclaw", Title: "build", ChannelID: other.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateTask(NewTask{CreatedBy: "human", Title: "một việc lẻ"}); err != nil {
		t.Fatal(err)
	}

	byProject, err := db.ListTasks(TaskFilter{ChannelID: trading.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(byProject) != 1 || byProject[0].Title != "backtest" {
		t.Fatalf("scoping to a project gave %d task(s): %+v", len(byProject), byProject)
	}

	// Work belonging to no project must still be findable, or it vanishes the
	// day the board starts scoping.
	loose, err := db.ListTasks(TaskFilter{ChannelID: NoChannel})
	if err != nil {
		t.Fatal(err)
	}
	if len(loose) != 1 || loose[0].Title != "một việc lẻ" {
		t.Fatalf("unfiled work is unreachable: %+v", loose)
	}

	all, err := db.ListTasks(TaskFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("no filter should still show everything, got %d", len(all))
	}
}

// A child is the same piece of work, split up. Landing half of it on another
// board would be a filing error nobody would think to look for.
func TestAChildInheritsTheProject(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "a1")
	dir := t.TempDir()
	proj, _ := db.CreateProject("Trading", "", "a1", dir, nil)

	if _, err := db.CreateTask(NewTask{CreatedBy: "a1", Title: "parent", AssignedTo: "a1", ChannelID: proj.ID}); err != nil {
		t.Fatal(err)
	}
	parent, err := db.ClaimTask("a1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.StartRun(parent.ID, "a1", parent.Attempts, ""); err != nil {
		t.Fatal(err)
	}
	child, err := db.CreateSubTask(parent.ID, "a1", NewTask{
		Title: "con", Body: "Một brief đủ dài để qua được luật thân bài tối thiểu của sub-task.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if child.ChannelID != proj.ID {
		t.Fatalf("the child landed on project %q, parent is on %q", child.ChannelID, proj.ID)
	}
}

// The brief is a file, and people and agents both edit it. Writing it has to
// replace the whole document atomically: an agent reading mid-save would
// otherwise get half a brief and act on it.
func TestWriteProjectBriefReplacesTheFile(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw")
	proj, err := db.CreateProject("Trading", "", "bomclaw", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}

	written := "# Trading\n\n## Mục tiêu\n\nChạy được một chiến lược có backtest.\n"
	if err := db.WriteProjectBrief(proj.ID, written); err != nil {
		t.Fatal(err)
	}
	if got := db.ProjectBrief(proj.ID); got != written {
		t.Fatalf("read back:\n%s", got)
	}
	// On disk, under the name the agents look for.
	onDisk, err := os.ReadFile(filepath.Join(proj.Workspace, AgentsFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != written {
		t.Fatal("the file on disk does not match what was saved")
	}
	// And the save left nothing behind: a half-written temp file in a project
	// folder is a file somebody will eventually open and believe.
	entries, _ := os.ReadDir(proj.Workspace)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".AGENTS-") {
			t.Fatalf("a temp file survived the save: %s", e.Name())
		}
	}
}

// A room with no folder has nowhere to put one, and says so rather than
// writing into whatever directory happens to be current.
func TestWritingABriefToAPlainRoomIsRefused(t *testing.T) {
	db := testDB(t)
	err := db.WriteProjectBrief(GeneralChannelID, "# nope")
	if err == nil {
		t.Fatal("writing a brief to #general was allowed")
	}
	if !strings.Contains(err.Error(), "room") {
		t.Errorf("the error should say why: %v", err)
	}
}
