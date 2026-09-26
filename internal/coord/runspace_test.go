package coord

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func runsDB(t *testing.T) *DB {
	t.Helper()
	db := testDB(t)
	db.SetRunsDir(filepath.Join(t.TempDir(), "runs"))
	registerTestAgents(t, db, "bomclaw", "bomclaw2", "bomclaw3")
	return db
}

// The point of the whole thing: agent2 hands pieces to agent1 and agent3, and
// all three end up in one directory instead of three.
func TestEveryAgentInATaskTreeWorksInOneFolder(t *testing.T) {
	db := runsDB(t)

	parent, err := db.CreateTask(NewTask{
		Title: "dựng landing page", Body: "toàn bộ trang", CreatedBy: "bomclaw2",
	})
	if err != nil {
		t.Fatal(err)
	}
	one, err := db.CreateSubTask(parent.ID, "bomclaw2", NewTask{
		Title: "form đăng ký", Body: "Dựng form đăng ký, validate email, gửi về /api/signup.",
		CreatedBy: "bomclaw2", AssignedTo: "bomclaw",
	})
	if err != nil {
		t.Fatal(err)
	}
	three, err := db.CreateSubTask(parent.ID, "bomclaw2", NewTask{
		Title: "viết copy", Body: "Viết copy cho hero và ba khối tính năng, giọng ngắn gọn.",
		CreatedBy: "bomclaw2", AssignedTo: "bomclaw3",
	})
	if err != nil {
		t.Fatal(err)
	}

	dirs := map[string]string{}
	for _, task := range []*Task{parent, one, three} {
		dir, shared, err := db.EnsureTaskWorkspace(task)
		if err != nil {
			t.Fatalf("%s: %v", task.ID, err)
		}
		if !shared {
			t.Fatalf("%s did not get a shared folder", task.ID)
		}
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			t.Fatalf("%s: folder was not created: %v", task.ID, err)
		}
		dirs[task.ID] = dir
	}
	if dirs[parent.ID] != dirs[one.ID] || dirs[one.ID] != dirs[three.ID] {
		t.Fatalf("three agents on one task tree landed in three places:\n  %s\n  %s\n  %s\n"+
			"a file left by one is then invisible to the others", dirs[parent.ID], dirs[one.ID], dirs[three.ID])
	}

	// A separate piece of work is a separate context and must not share.
	other, err := db.CreateTask(NewTask{Title: "việc khác", CreatedBy: "bomclaw"})
	if err != nil {
		t.Fatal(err)
	}
	if dir, _ := db.TaskWorkspace(other); dir == dirs[parent.ID] {
		t.Fatal("an unrelated task tree was given the same folder")
	}
}

// A project already has a folder, and it is where people look for the work.
func TestAProjectsFolderWinsOverTheRunFolder(t *testing.T) {
	db := runsDB(t)
	projects := t.TempDir()
	p, err := db.CreateProject("trading", "bot giao dịch", "owner", projects, nil)
	if err != nil {
		t.Fatal(err)
	}
	task, err := db.CreateTask(NewTask{Title: "xem giá", CreatedBy: "bomclaw", ChannelID: p.ID})
	if err != nil {
		t.Fatal(err)
	}
	dir, shared := db.TaskWorkspace(task)
	if dir != p.Workspace {
		t.Fatalf("filed work ran in %q, want the project folder %q", dir, p.Workspace)
	}
	if shared {
		t.Error("a project folder is not the context's scratch and should not be described as shared")
	}
}

// A goal assembles in its project; its children experiment somewhere else.
//
// This replaces TestADelegatedTreeInAProjectStaysInTheProjectFolder, which
// pinned the first version of the rule: the whole tree ran in the project
// folder. That was right while decomposition was rare and stops being right the
// moment a goal fans out, because MaxOpenChildren allows eight agents at once
// and a project folder is a plain directory — two of them overwriting each
// other there is a silent loss with nothing to undo.
func TestAGoalAssemblesInItsProjectWhileChildrenWorkInScratch(t *testing.T) {
	db := runsDB(t)
	projects := t.TempDir()
	p, err := db.CreateProject("trading", "bot giao dịch", "owner", projects, nil)
	if err != nil {
		t.Fatal(err)
	}
	goal, err := db.CreateTask(NewTask{
		Title: "backtest chiến lược", Body: "toàn bộ", CreatedBy: "bomclaw2", ChannelID: p.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ClaimTask("bomclaw2"); err != nil {
		t.Fatal(err)
	}
	one, err := db.CreateSubTask(goal.ID, "bomclaw2", NewTask{
		Title: "tải dữ liệu", Body: "Tải nến 1h của BTC/ETH hai năm, lưu parquet, kiểm tra nến thiếu.",
		AssignedTo: "bomclaw",
	})
	if err != nil {
		t.Fatal(err)
	}
	three, err := db.CreateSubTask(goal.ID, "bomclaw2", NewTask{
		Title: "vẽ báo cáo", Body: "Vẽ equity curve và drawdown, xuất PNG kèm bảng số liệu tóm tắt.",
		AssignedTo: "bomclaw3",
	})
	if err != nil {
		t.Fatal(err)
	}

	// The goal is the only thing that writes where the product lives.
	dir, shared := db.TaskWorkspace(goal)
	if dir != p.Workspace || shared {
		t.Fatalf("the goal works in %q (shared=%v), want the project folder %q", dir, shared, p.Workspace)
	}

	// The children meet each other in scratch, and not in the project.
	scratch := db.RunspacePath(goal.ContextID)
	for _, child := range []*Task{one, three} {
		dir, shared := db.TaskWorkspace(child)
		if dir != scratch || !shared {
			t.Errorf("%s works in %q (shared=%v), want the shared scratch %q",
				child.Title, dir, shared, scratch)
		}
		if dir == p.Workspace {
			t.Errorf("%s writes straight into the project folder alongside its siblings", child.Title)
		}
	}
}

// A task's directory must never move under it. Keying on ParentID is what
// guarantees that: ParentID never changes, whereas "has this tree been split
// yet" flips the first time the task fans out — and the files it wrote in the
// previous run would then be somewhere it is no longer standing.
func TestATasksWorkspaceNeverMovesWhenItFansOut(t *testing.T) {
	db := runsDB(t)
	projects := t.TempDir()
	p, err := db.CreateProject("trading", "", "owner", projects, nil)
	if err != nil {
		t.Fatal(err)
	}
	goal, err := db.CreateTask(NewTask{Title: "việc lớn", CreatedBy: "bomclaw2", ChannelID: p.ID})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := db.TaskWorkspace(goal)

	if _, err := db.ClaimTask("bomclaw2"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateSubTask(goal.ID, "bomclaw2", NewTask{
		Title: "một mảnh", Body: "Một mô tả đủ dài để qua được ràng buộc brief tự đứng được của con.",
	}); err != nil {
		t.Fatal(err)
	}

	reread, err := db.GetTask(goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after, _ := db.TaskWorkspace(reread); after != before {
		t.Fatalf("the goal's working directory moved when it fanned out: %q → %q\n"+
			"whatever it wrote in the previous run is no longer where it is standing", before, after)
	}
}

// #general is a room, not a project, so a task filed there still needs
// somewhere for three agents to meet.
func TestATaskInARoomWithNoFolderStillGetsOne(t *testing.T) {
	db := runsDB(t)
	task, err := db.CreateTask(NewTask{Title: "hỏi nhanh", CreatedBy: "bomclaw", ChannelID: GeneralChannelID})
	if err != nil {
		t.Fatal(err)
	}
	dir, shared := db.TaskWorkspace(task)
	if dir == "" || !shared {
		t.Fatalf("a task in a folderless room got %q (shared=%v)", dir, shared)
	}
}

// Asking where a task works must not leave a directory behind: `task show` is
// a read.
func TestAskingWhereATaskWorksCreatesNothing(t *testing.T) {
	db := runsDB(t)
	task, err := db.CreateTask(NewTask{Title: "chưa chạy", CreatedBy: "bomclaw"})
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := db.TaskWorkspace(task)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("reading the path created %s", dir)
	}
	if _, _, err := db.EnsureTaskWorkspace(task); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("a run did not create %s: %v", dir, err)
	}
}

func TestRunFoldersGoWhenTheTreeHasBeenFinishedAWhile(t *testing.T) {
	db := runsDB(t)
	done, err := db.CreateTask(NewTask{Title: "xong lâu rồi", CreatedBy: "bomclaw"})
	if err != nil {
		t.Fatal(err)
	}
	live, err := db.CreateTask(NewTask{Title: "đang chạy", CreatedBy: "bomclaw"})
	if err != nil {
		t.Fatal(err)
	}
	doneDir, _, err := db.EnsureTaskWorkspace(done)
	if err != nil {
		t.Fatal(err)
	}
	liveDir, _, err := db.EnsureTaskWorkspace(live)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(doneDir, "scratch.txt"), []byte("tạm"), 0o644); err != nil {
		t.Fatal(err)
	}

	long := time.Now().Add(-48 * time.Hour)
	if _, err := db.conn.Exec(`UPDATE tasks SET state = ?, updated_at = ? WHERE id = ?`,
		TaskCompleted, ts(long), done.ID); err != nil {
		t.Fatal(err)
	}

	n, err := db.PurgeRunspaces(time.Now().Add(-24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("removed %d folders, want 1", n)
	}
	if _, err := os.Stat(doneDir); !os.IsNotExist(err) {
		t.Error("a finished tree's scratch is still on disk")
	}
	if _, err := os.Stat(liveDir); err != nil {
		t.Fatal("the purge took a folder belonging to work that is still running")
	}
}

// A tree that is finished but still moving — a child completed an hour ago —
// must keep its folder, or the parent wakes up to an empty directory.
func TestAFreshlyFinishedTreeKeepsItsFolder(t *testing.T) {
	db := runsDB(t)
	task, err := db.CreateTask(NewTask{Title: "vừa xong", CreatedBy: "bomclaw"})
	if err != nil {
		t.Fatal(err)
	}
	dir, _, err := db.EnsureTaskWorkspace(task)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.Exec(`UPDATE tasks SET state = ? WHERE id = ?`, TaskCompleted, task.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := db.PurgeRunspaces(time.Now().Add(-24 * time.Hour)); err != nil || n != 0 {
		t.Fatalf("purged %d folders of work that finished minutes ago (%v)", n, err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("the folder went while the tree was still warm")
	}
}
