package taskrunner

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ngocp/goterm-control/internal/coord"
)

func progressDB(t *testing.T) *coord.DB {
	t.Helper()
	db, err := coord.Open(filepath.Join(t.TempDir(), "coord.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.RegisterAgent(coord.Agent{ID: "a1", DisplayName: "a1", WSAddr: "ws://127.0.0.1:0/ws"}); err != nil {
		t.Fatal(err)
	}
	return db
}

// A task that came from a conversation says what it is doing there. Four
// minutes of silence in the room that asked is the same failure a channel turn
// had before it reported progress — worse, because a task is the long kind of
// work.
func TestATaskFromAThreadSaysWhatItIsDoing(t *testing.T) {
	db := progressDB(t)
	root, _, err := db.PostMessage(coord.NewChannelMessage{
		ChannelID: coord.GeneralChannelID, AuthorKind: coord.MemberUser, AuthorID: coord.OwnerUserID,
		Body: "nghiên cứu giúp mình",
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := db.CreateTask(coord.NewTask{CreatedBy: "a1", Title: "nghiên cứu crypto", AssignedTo: "a1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.BindThreadToTask(root.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	claimed, err := db.ClaimTask("a1")
	if err != nil {
		t.Fatal(err)
	}

	p := openProgress(db, claimed)
	if p == nil {
		t.Fatal("no progress line was opened for a task bound to a thread")
	}
	thread, _ := db.ThreadMessages(root.ID)
	if len(thread) != 2 || !strings.Contains(thread[1].Body, "đang chạy") {
		t.Fatalf("the room was not told: %+v", thread)
	}
	if !strings.Contains(thread[1].Body, task.ID) {
		t.Error("the progress line does not name the task it belongs to")
	}

	// And it goes when the run does: the permanent record is the report the
	// reporter posts, not a line that says "working" forever.
	p.Close()
	thread, _ = db.ThreadMessages(root.ID)
	if len(thread) != 1 {
		t.Fatalf("the progress line outlived the run: %+v", thread)
	}
}

// Work queued from the CLI or a schedule has no room to report into, and must
// not invent one.
func TestATaskWithNoThreadReportsNowhere(t *testing.T) {
	db := progressDB(t)
	task, err := db.CreateTask(coord.NewTask{CreatedBy: "human", Title: "việc từ lịch", AssignedTo: "a1"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, _ := db.ClaimTask("a1")
	if p := openProgress(db, claimed); p != nil {
		t.Fatal("a task with no conversation opened a progress line somewhere")
	}
	line, _ := db.ChannelMessages(coord.GeneralChannelID, 10, time.Time{})
	if len(line) != 0 {
		t.Fatalf("it posted into the channel anyway: %+v", line)
	}
	_ = task
}

// A run that died with the gateway leaves a line saying "working". The next
// run clears it, because a room where an agent is permanently about to finish
// is worse than one that says nothing.
func TestAStaleProgressLineIsSweptByTheNextRun(t *testing.T) {
	db := progressDB(t)
	root, _, _ := db.PostMessage(coord.NewChannelMessage{
		ChannelID: coord.GeneralChannelID, AuthorKind: coord.MemberUser, AuthorID: coord.OwnerUserID,
		Body: "việc dài",
	})
	task, _ := db.CreateTask(coord.NewTask{CreatedBy: "a1", Title: "việc dài", AssignedTo: "a1"})
	if err := db.BindThreadToTask(root.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	claimed, _ := db.ClaimTask("a1")

	first := openProgress(db, claimed) // the gateway dies here: no Close
	if first == nil {
		t.Fatal("no progress line")
	}
	second := openProgress(db, claimed)
	if second == nil {
		t.Fatal("no progress line on the second run")
	}

	thread, _ := db.ThreadMessages(root.ID)
	var working int
	for _, m := range thread {
		if strings.HasPrefix(m.Body, progressPrefix) {
			working++
		}
	}
	if working != 1 {
		t.Fatalf("expected exactly one progress line, found %d", working)
	}
}

// The tool names say what it is reaching for; this is what it is actually
// saying. A person waiting wants the second one, and until now the thread
// showed only the first.
func TestTheRoomSeesTheAnswerForming(t *testing.T) {
	db := progressDB(t)
	root, _, _ := db.PostMessage(coord.NewChannelMessage{
		ChannelID: coord.GeneralChannelID, AuthorKind: coord.MemberUser, AuthorID: coord.OwnerUserID,
		Body: "nghiên cứu giúp mình",
	})
	task, _ := db.CreateTask(coord.NewTask{CreatedBy: "a1", Title: "nghiên cứu", AssignedTo: "a1"})
	if err := db.BindThreadToTask(root.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	claimed, _ := db.ClaimTask("a1")
	p := openProgress(db, claimed)
	if p == nil {
		t.Fatal("no progress line")
	}

	// Below the threshold nothing is redrawn: a write per token tells the
	// reader something they cannot read that fast.
	p.Text("Bắt đầu. ")
	thread, _ := db.ThreadMessages(root.ID)
	if strings.Contains(thread[1].Body, "Bắt đầu") {
		t.Error("redrew after a few characters")
	}

	// Past it, the room sees what is being written.
	p.Text(strings.Repeat("Phân tích giai đoạn thị trường. ", 8))
	thread, _ = db.ThreadMessages(root.ID)
	if !strings.Contains(thread[1].Body, "Phân tích giai đoạn thị trường") {
		t.Fatalf("the forming answer is not in the room:\n%s", thread[1].Body)
	}
	// And it is still a progress line, not the answer: the answer is what the
	// reporter posts when the task is done.
	if !strings.HasPrefix(thread[1].Body, progressPrefix) {
		t.Error("the progress line stopped announcing itself as one")
	}
}

// Work that belongs to a room but came from no conversation — `task new`, a
// verify, a rework — opens a thread of its own and talks there: who picked it
// up, what it is doing, how it ended. 58 of 68 tasks in ch_phim-ngan-25d ran
// without a word in the room before this.
func TestARoomTaskWithNoThreadOpensOneAndSaysHowItEnded(t *testing.T) {
	db := progressDB(t)
	if err := db.RegisterAgent(coord.Agent{ID: "a2", DisplayName: "a2", WSAddr: "ws://127.0.0.1:0/ws"}); err != nil {
		t.Fatal(err)
	}
	task, err := db.CreateTask(coord.NewTask{CreatedBy: "system", Title: "Verify: phim @a2",
		AssignedTo: "a1", Kind: coord.KindVerify, ChannelID: coord.GeneralChannelID})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := db.ClaimTask("a1")
	if err != nil {
		t.Fatal(err)
	}
	p := openProgress(db, claimed)
	if p == nil {
		t.Fatal("a room task with no thread said nothing")
	}
	root, ch, _ := db.TaskThread(task.ID)
	if root == "" || ch != coord.GeneralChannelID {
		t.Fatalf("no thread bound to the task: %q %q", root, ch)
	}
	rootMsg, _ := db.GetMessage(root)
	if !strings.Contains(rootMsg.Body, "a1 kiểm tra") || !strings.Contains(rootMsg.Body, "giao bởi system") {
		t.Errorf("the claim does not say who picked what up: %q", rootMsg.Body)
	}
	if ms, _ := db.UnreadMentions(coord.MemberAgent, "a2", 10); len(ms) != 0 {
		t.Errorf("echoing a title woke an agent: %+v", ms)
	}

	p.Close()
	done := *claimed
	done.State, done.Result = coord.TaskCompleted, "pass: ổn"
	p.Finish(&done, false)
	thread, _ := db.ThreadMessages(root)
	last := thread[len(thread)-1]
	if !strings.Contains(last.Body, "pass: ổn") || strings.HasPrefix(last.Body, progressPrefix) {
		t.Fatalf("the thread does not say how it ended: %+v", thread)
	}
}

// A sub-task talks in the thread its parent was asked in — where the people
// waiting on the parent are reading — rather than opening a second one.
func TestASubTaskTalksInItsParentsThread(t *testing.T) {
	db := progressDB(t)
	ask, _, _ := db.PostMessage(coord.NewChannelMessage{
		ChannelID: coord.GeneralChannelID, AuthorKind: coord.MemberUser, AuthorID: coord.OwnerUserID, Body: "làm phim",
	})
	parent, _ := db.CreateTask(coord.NewTask{CreatedBy: "a1", Title: "phim", ChannelID: coord.GeneralChannelID})
	if err := db.BindThreadToTask(ask.ID, parent.ID); err != nil {
		t.Fatal(err)
	}
	child, err := db.CreateTask(coord.NewTask{CreatedBy: "system", Title: "Verify: phim", AssignedTo: "a1",
		Kind: coord.KindVerify, ParentID: parent.ID, ChannelID: coord.GeneralChannelID})
	if err != nil {
		t.Fatal(err)
	}
	claimed, _ := db.ClaimTask("a1")
	if claimed.ID != child.ID {
		claimed, _ = db.ClaimTask("a1")
	}
	p := openProgress(db, claimed)
	if p == nil || p.rootID != ask.ID || p.ownThread {
		t.Fatalf("the sub-task did not join its parent's thread: %+v", p)
	}
	if own, _, _ := db.TaskThread(child.ID); own != "" {
		t.Error("the sub-task opened a thread of its own as well")
	}
	thread, _ := db.ThreadMessages(ask.ID)
	if len(thread) < 3 || !strings.Contains(thread[1].Body, "kiểm tra") {
		t.Fatalf("no claim line in the parent's thread: %+v", thread)
	}
}

// When the reporter will post the result in the task's own thread, Finish
// stays quiet: one result, not two.
func TestFinishLeavesADelegatedResultToTheReporter(t *testing.T) {
	db := progressDB(t)
	task, _ := db.CreateTask(coord.NewTask{CreatedBy: "a2", Title: "x", AssignedTo: "a1", ChannelID: coord.GeneralChannelID})
	claimed, _ := db.ClaimTask("a1")
	p := openProgress(db, claimed)
	p.Close()
	done := *claimed
	done.State, done.Result = coord.TaskCompleted, "kết quả"
	p.Finish(&done, true)
	root, _, _ := db.TaskThread(task.ID)
	thread, _ := db.ThreadMessages(root)
	for _, m := range thread {
		if strings.Contains(m.Body, "kết quả") {
			t.Fatal("Finish posted a result the reporter is about to post")
		}
	}
}
