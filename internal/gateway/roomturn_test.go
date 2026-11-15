package gateway

import (
	"errors"
	"strings"
	"testing"

	"github.com/ngocp/goterm-control/internal/coord"
)

// A room turn that does work becomes a task, and its session — from the line
// that asked — is in the task's log; the answer lands in the thread.
func TestRoomTurnRecordsWorkAsATaskWithItsSession(t *testing.T) {
	deps, cdb := mentionTestDeps(t, &recordingTurn{})
	dm, err := cdb.EnsureOwnerDM("bomclaw2")
	if err != nil {
		t.Fatal(err)
	}
	line, _, err := cdb.PostMessage(coord.NewChannelMessage{
		ChannelID: dm.ID, AuthorKind: coord.MemberUser, AuthorID: coord.OwnerUserID, Body: "sửa lỗi đăng nhập",
	})
	if err != nil {
		t.Fatal(err)
	}

	rt := OpenRoomTurn(deps, *line, line.ID, nil)
	rt.Sink().NoteThinking("đọc code auth trước")
	rt.Sink().NoteTool("Bash(go test ./auth)") // work: the turn becomes a task
	rt.Sink().Write("Đã sửa, test pass.")
	final := rt.Finish(rt.Sink().Text(), nil)

	task := rt.Task()
	if task == nil {
		t.Fatal("a turn that called a tool did not become a task")
	}
	got, _ := cdb.GetTask(task.ID)
	if got.State != coord.TaskCompleted || got.ChannelID != dm.ID {
		t.Fatalf("task after the turn: %s in %q", got.State, got.ChannelID)
	}
	entries, _ := cdb.TaskLog(task.ID, 0, 0)
	var kinds []string
	for _, e := range entries {
		kinds = append(kinds, e.Kind+":"+e.Label)
	}
	joined := strings.Join(kinds, ",")
	for _, want := range []string{"user:", "status:start", "thinking:", "tool:Bash(go test ./auth)", "text:", "status:end:completed"} {
		if !strings.Contains(joined, want) {
			t.Errorf("log missing %q: %s", want, joined)
		}
	}
	thread, _ := cdb.ThreadMessages(line.ID)
	if last := thread[len(thread)-1]; last.AuthorID != deps.AgentID || last.Body != final || !strings.Contains(final, task.ID) {
		t.Fatalf("answer in thread: %+v (final %q)", last, final)
	}
	if ids, _ := cdb.ThreadTaskIDs(line.ID); len(ids) != 1 || ids[0] != task.ID {
		t.Fatalf("thread tasks = %v", ids)
	}
}

// A quick answer is just an answer: no task, nothing on the board.
func TestRoomTurnQuickAnswerIsNotATask(t *testing.T) {
	deps, cdb := mentionTestDeps(t, &recordingTurn{})
	dm, _ := cdb.EnsureOwnerDM("bomclaw2")
	line, _, _ := cdb.PostMessage(coord.NewChannelMessage{
		ChannelID: dm.ID, AuthorKind: coord.MemberUser, AuthorID: coord.OwnerUserID, Body: "mấy giờ rồi?",
	})
	rt := OpenRoomTurn(deps, *line, line.ID, nil)
	rt.Sink().Write("10 giờ.")
	rt.Finish(rt.Sink().Text(), nil)
	if rt.Task() != nil {
		t.Fatal("a one-line answer became a task")
	}
	thread, _ := cdb.ThreadMessages(line.ID)
	if last := thread[len(thread)-1]; last.Body != "10 giờ." {
		t.Fatalf("thread: %+v", thread)
	}
}

// The owner interrupting from the board ends the turn with a note saying so.
func TestRoomTurnSteeredEndsWithANote(t *testing.T) {
	deps, cdb := mentionTestDeps(t, &recordingTurn{})
	dm, _ := cdb.EnsureOwnerDM("bomclaw2")
	line, _, _ := cdb.PostMessage(coord.NewChannelMessage{
		ChannelID: dm.ID, AuthorKind: coord.MemberUser, AuthorID: coord.OwnerUserID, Body: "làm báo cáo",
	})
	rt := OpenRoomTurn(deps, *line, line.ID, nil)
	rt.Sink().NoteTool("Read(a.md)")
	final := rt.Finish("đang viết…", ErrRoomSteered)
	if !strings.Contains(final, "chỉ hướng") || !strings.Contains(final, "đang viết") {
		t.Fatalf("note: %q", final)
	}
	if got, _ := cdb.GetTask(rt.Task().ID); got.State != coord.TaskCompleted {
		t.Fatalf("a steered turn left its task %s", got.State)
	}
	if errors.Is(nil, ErrRoomSteered) {
		t.Fatal("unreachable")
	}
}

// Work asked for in the Direct room (or on Telegram) is filed under the project
// it was about — the board of that project is where it is looked for.
func TestRoomTurnFilesDirectWorkUnderItsProject(t *testing.T) {
	deps, cdb := mentionTestDeps(t, &recordingTurn{})
	shop, err := cdb.CreateProject("shopee-deals", "", "owner", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	dm, _ := cdb.EnsureOwnerDM("bomclaw2")
	line, _, _ := cdb.PostMessage(coord.NewChannelMessage{
		ChannelID: dm.ID, AuthorKind: coord.MemberUser, AuthorID: coord.OwnerUserID, Body: "làm trang chi tiết có biểu đồ giá",
	})
	rt := OpenRoomTurn(deps, *line, line.ID, nil)
	rt.Sink().NoteTool("Bash(cd ../shopee-deals)")
	rt.Sink().NoteToolInput("Bash", `{"command":"cd `+shop.Workspace+` && npm test"}`)
	rt.Sink().Write("xong")
	rt.Finish(rt.Sink().Text(), nil)

	got, _ := cdb.GetTask(rt.Task().ID)
	if got.ChannelID != shop.ID {
		t.Fatalf("task filed under %q, want the project %q", got.ChannelID, shop.ID)
	}
	// The answer is still where it was asked.
	thread, _ := cdb.ThreadMessages(line.ID)
	if last := thread[len(thread)-1]; last.ChannelID != dm.ID {
		t.Fatalf("answer moved out of the Direct room: %+v", last)
	}
}

// A turn in a project's own room stays in that room, whatever it touched.
func TestRoomTurnInAProjectRoomStaysThere(t *testing.T) {
	deps, cdb := mentionTestDeps(t, &recordingTurn{})
	shop, _ := cdb.CreateProject("shopee-deals", "", "owner", t.TempDir(), nil)
	arcade, _ := cdb.CreateProject("arcade", "", "owner", t.TempDir(), nil)
	line, _, _ := cdb.PostMessage(coord.NewChannelMessage{
		ChannelID: arcade.ID, AuthorKind: coord.MemberUser, AuthorID: coord.OwnerUserID, Body: "so sánh với shopee-deals",
	})
	rt := OpenRoomTurn(deps, *line, line.ID, nil)
	rt.Sink().NoteTool("Read(x)")
	rt.Sink().NoteToolInput("Read", `{"file_path":"`+shop.Workspace+`/README.md"}`)
	rt.Finish("ok", nil)
	if got, _ := cdb.GetTask(rt.Task().ID); got.ChannelID != arcade.ID {
		t.Fatalf("moved to %q", got.ChannelID)
	}
}
