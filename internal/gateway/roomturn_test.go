package gateway

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

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

// Work done from a private message shows in its project's room as a thread —
// the "nhận việc" card a queued task opens — and says there how it ended. The
// conversation itself, and every later reply to the owner, stays in Direct.
func TestRoomTurnDirectWorkOpensAThreadInTheProjectRoom(t *testing.T) {
	deps, cdb := mentionTestDeps(t, &recordingTurn{})
	shop, err := cdb.CreateProject("shopee-deals", "", "owner", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	dm, _ := cdb.EnsureOwnerDM("bomclaw2")
	line, _, _ := cdb.PostMessage(coord.NewChannelMessage{
		ChannelID: dm.ID, AuthorKind: coord.MemberUser, AuthorID: coord.OwnerUserID, Body: "publish lại trang chi tiết",
	})
	rt := OpenRoomTurn(deps, *line, line.ID, nil)
	rt.Sink().NoteTool("Bash(npm run build)")
	rt.Sink().NoteToolInput("Bash", `{"command":"cd `+shop.Workspace+` && npm run build"}`)

	// Mid-turn: the card is already in the room, so the team sees work in progress.
	top, err := cdb.ChannelMessages(shop.ID, 50, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var card *coord.ChannelMessage
	for i := range top {
		if top[i].ThreadRoot == "" && top[i].TaskID != "" {
			card = &top[i]
		}
	}
	task := rt.Task()
	if card == nil || task == nil || card.TaskID != task.ID || card.AuthorID != deps.AgentID {
		t.Fatalf("no task card in the project room: %+v", top)
	}
	// The dashboard turns a claim line into a card; keep its shape.
	for _, want := range []string{"nhận việc `" + task.ID + "`", "publish lại trang chi tiết", "giao bởi owner"} {
		if !strings.Contains(card.Body, want) {
			t.Errorf("card body %q lacks %q", card.Body, want)
		}
	}

	rt.Sink().Write("Đã build và publish, link: https://shop.example")
	rt.Finish(rt.Sink().Text(), nil)

	thread, _ := cdb.ThreadMessages(card.ID)
	var bodies []string
	for _, m := range thread {
		bodies = append(bodies, m.Body)
	}
	joined := strings.Join(bodies, "\n")
	if !strings.Contains(joined, "tin nhắn riêng") || !strings.Contains(joined, "✅ xong — Đã build và publish") {
		t.Fatalf("project thread = %q", joined)
	}
	// Replies to the owner still go to Direct, not to the tracking card.
	if root, ch, _ := cdb.ConversationThread(task); ch != dm.ID || root != line.ID {
		t.Fatalf("conversation thread moved to %s/%s", ch, root)
	}
	dmThread, _ := cdb.ThreadMessages(line.ID)
	if last := dmThread[len(dmThread)-1]; !strings.Contains(last.Body, "Đã build và publish") {
		t.Fatalf("answer not in Direct: %+v", last)
	}
}

// A quick private answer is not work and leaves project rooms alone; a failed
// turn says so in the project thread.
func TestRoomTurnDirectQuickAnswerStaysPrivateAndFailureIsReported(t *testing.T) {
	deps, cdb := mentionTestDeps(t, &recordingTurn{})
	shop, _ := cdb.CreateProject("shopee-deals", "", "owner", t.TempDir(), nil)
	dm, _ := cdb.EnsureOwnerDM("bomclaw2")

	q, _, _ := cdb.PostMessage(coord.NewChannelMessage{
		ChannelID: dm.ID, AuthorKind: coord.MemberUser, AuthorID: coord.OwnerUserID, Body: "shopee-deals đang chạy port nào?",
	})
	rt := OpenRoomTurn(deps, *q, q.ID, nil)
	rt.Finish("3000", nil)
	if top, _ := cdb.ChannelMessages(shop.ID, 50, time.Time{}); len(top) != 0 {
		t.Fatalf("a quick answer posted to the project room: %+v", top)
	}

	w, _, _ := cdb.PostMessage(coord.NewChannelMessage{
		ChannelID: dm.ID, AuthorKind: coord.MemberUser, AuthorID: coord.OwnerUserID, Body: "deploy shopee-deals",
	})
	rt = OpenRoomTurn(deps, *w, w.ID, nil)
	rt.Sink().NoteTool("Bash(./deploy.sh)")
	rt.Sink().NoteToolInput("Bash", `{"command":"cd `+shop.Workspace+` && ./deploy.sh"}`)
	rt.Finish("", errors.New("deploy.sh: exit 1"))
	top, _ := cdb.ChannelMessages(shop.ID, 50, time.Time{})
	if len(top) != 1 {
		t.Fatalf("project room = %+v", top)
	}
	thread, _ := cdb.ThreadMessages(top[0].ID)
	if last := thread[len(thread)-1]; !strings.Contains(last.Body, "⚠️ không xong") || !strings.Contains(last.Body, "exit 1") {
		t.Fatalf("failure not reported: %+v", last)
	}
}

// The thread's session panel shows a turn while it streams — before it is a
// task, and when it never becomes one — and says it is live until it ends.
// It used to be blank until the first tool call: the log was held in memory
// until then, and dropped for an answer that called none.
func TestThreadLogShowsATurnBeforeItIsATask(t *testing.T) {
	deps, cdb := mentionTestDeps(t, &recordingTurn{})
	dm, _ := cdb.EnsureOwnerDM("bomclaw2")
	line, _, _ := cdb.PostMessage(coord.NewChannelMessage{
		ChannelID: dm.ID, AuthorKind: coord.MemberUser, AuthorID: coord.OwnerUserID, Body: "gửi link đăng nhập lại",
	})
	read := func(after int64) ThreadSession {
		t.Helper()
		raw, err := handleThreadLog(deps, []byte(`{"root_id":"`+line.ID+`","after":`+strconv.FormatInt(after, 10)+`}`))
		if err != nil {
			t.Fatal(err)
		}
		var s ThreadSession
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	rt := OpenRoomTurn(deps, *line, line.ID, nil)
	s := read(0)
	if !s.Live || s.Running != "" || len(s.Entries) < 2 || s.Entries[0].Kind != coord.LogUser {
		t.Fatalf("turn just opened: live=%v running=%q entries=%+v", s.Live, s.Running, s.Entries)
	}
	cursor := s.Entries[len(s.Entries)-1].ID

	rt.Sink().NoteTool("Bash(curl /dev/null)") // now it is a task
	task := rt.Task()
	if task == nil {
		t.Fatal("no task after a tool call")
	}
	s = read(cursor)
	if !s.Live || s.Running != task.ID || len(s.Entries) == 0 {
		t.Fatalf("after the tool call: live=%v running=%q entries=%+v", s.Live, s.Running, s.Entries)
	}
	// What was logged before the task now belongs to it, in order, once.
	all, _ := cdb.TaskLog(task.ID, 0, 0)
	if len(all) < 3 || all[0].Kind != coord.LogUser || all[0].ID != read(0).Entries[0].ID {
		t.Fatalf("task log after bind: %+v", all)
	}
	rt.Sink().Write("Link: https://example.test")
	rt.Finish(rt.Sink().Text(), nil)
	if s = read(0); s.Live || s.Running != "" {
		t.Fatalf("finished turn still live: %+v", s)
	}
	if n := len(s.Entries); n != len(all)+2 { // + the reply text and the end line
		t.Fatalf("entries after finish = %d, task log before = %d", n, len(all))
	}

	// A quick answer never becomes a task and is still in the thread.
	line2, _, _ := cdb.PostMessage(coord.NewChannelMessage{
		ChannelID: dm.ID, ThreadRoot: line.ID, AuthorKind: coord.MemberUser, AuthorID: coord.OwnerUserID, Body: "cảm ơn",
	})
	rt2 := OpenRoomTurn(deps, *line2, line.ID, nil)
	rt2.Sink().Write("Không có gì.")
	rt2.Finish(rt2.Sink().Text(), nil)
	if rt2.Task() != nil {
		t.Fatal("a quick answer became a task")
	}
	s = read(0)
	last := s.Entries[len(s.Entries)-1]
	if s.Live || last.Label != "end:completed" || last.TaskID != coord.TurnLogKey(line2.ID) {
		t.Fatalf("quick answer in the thread: live=%v last=%+v", s.Live, last)
	}
}
