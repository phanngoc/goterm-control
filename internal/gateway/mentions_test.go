package gateway

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ngocp/goterm-control/internal/coord"
	"github.com/ngocp/goterm-control/internal/execution"
	"github.com/ngocp/goterm-control/internal/session"
)

// recordingTurn answers every turn with a fixed reply and remembers what it was
// asked, including the session it ran under — which is how these tests see
// whether the thread's conversation was resumed.
type recordingTurn struct {
	mu       sync.Mutex
	calls    int
	prompts  []string
	sessions []string
	resumed  []string
	reply    string
	newID    string

	// duringTurn runs against the sink before the reply is written, so a test
	// can watch what the room sees while the turn is still going.
	duringTurn func(TurnSink)
}

func (r *recordingTurn) RunTurn(ctx context.Context, sess *session.Session, chatID int64,
	modelID, userText string, sink TurnSink) (*execution.RunResult, error) {
	if r.duringTurn != nil {
		r.duringTurn(sink)
	}
	r.mu.Lock()
	r.calls++
	r.prompts = append(r.prompts, userText)
	r.sessions = append(r.sessions, sess.ID)
	r.resumed = append(r.resumed, sess.GetSessionID())
	reply, newID := r.reply, r.newID
	r.mu.Unlock()

	if newID != "" {
		sess.SetSessionID(newID) // the CLI hands back the session it just wrote
	}
	sink.Write(reply)
	return &execution.RunResult{SessionID: sess.ID, Status: execution.RunSuccess}, nil
}

func (r *recordingTurn) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func mentionTestDeps(t *testing.T, turn *recordingTurn) (Deps, *coord.DB) {
	t.Helper()
	cdb, err := coord.Open(filepath.Join(t.TempDir(), "coord.db"))
	if err != nil {
		t.Fatalf("coord: %v", err)
	}
	t.Cleanup(func() { cdb.Close() })
	for _, id := range []string{"bomclaw", "bomclaw2"} {
		if err := cdb.RegisterAgent(coord.Agent{ID: id, DisplayName: id, WSAddr: "ws://127.0.0.1:0/ws"}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
	return Deps{Coord: cdb, Turn: turn, AgentID: "bomclaw2", ProviderName: "claude"}, cdb
}

// TestMentionBecomesAChatTurn is the acceptance test of the whole feature:
// naming an agent must produce an answer in the room, not silence — and not a
// task, which is what the first attempt at this did.
func TestMentionBecomesAChatTurn(t *testing.T) {
	turn := &recordingTurn{reply: "chào, mình đây", newID: "sess-1"}
	deps, cdb := mentionTestDeps(t, turn)

	m, wake, err := cdb.PostMessage(coord.NewChannelMessage{
		ChannelID: coord.GeneralChannelID, AuthorKind: coord.MemberUser, AuthorID: coord.OwnerUserID,
		Body: "@bomclaw2 chào bạn",
	})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if len(wake) != 1 || wake[0] != "bomclaw2" {
		t.Fatalf("expected bomclaw2 to be woken, got %v", wake)
	}

	NewMentionWatcher(deps).sweep(context.Background())

	if turn.count() != 1 {
		t.Fatalf("expected exactly one turn, got %d", turn.count())
	}
	if !strings.Contains(turn.prompts[0], "chào bạn") {
		t.Fatalf("prompt did not carry what was said:\n%s", turn.prompts[0])
	}

	// The reply is in the room, in a thread under the line that named it.
	thread, err := cdb.ThreadMessages(m.ID)
	if err != nil {
		t.Fatalf("thread: %v", err)
	}
	if len(thread) != 2 {
		t.Fatalf("expected the mention and one reply, got %d messages", len(thread))
	}
	if thread[1].Body != "chào, mình đây" || thread[1].AuthorID != "bomclaw2" {
		t.Fatalf("unexpected reply: %+v", thread[1])
	}

	// And the summons is cleared, or the next sweep answers it again forever.
	left, err := cdb.UnreadMentions(coord.MemberAgent, "bomclaw2", 10)
	if err != nil {
		t.Fatalf("unread: %v", err)
	}
	if len(left) != 0 {
		t.Fatalf("mention still unread after being answered: %v", left)
	}
}

// TestSecondTurnInAThreadResumesTheFirst is the difference between a
// conversation and two strangers: turn 2 must run on the session turn 1 left.
func TestSecondTurnInAThreadResumesTheFirst(t *testing.T) {
	turn := &recordingTurn{reply: "ừ", newID: "sess-1"}
	deps, cdb := mentionTestDeps(t, turn)
	w := NewMentionWatcher(deps)

	root, _, err := cdb.PostMessage(coord.NewChannelMessage{
		ChannelID: coord.GeneralChannelID, AuthorKind: coord.MemberUser, AuthorID: coord.OwnerUserID,
		Body: "@bomclaw2 xem hộ log",
	})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	w.sweep(context.Background())

	// A follow-up inside the same thread.
	if _, _, err := cdb.PostMessage(coord.NewChannelMessage{
		ChannelID: coord.GeneralChannelID, ThreadRoot: root.ID,
		AuthorKind: coord.MemberUser, AuthorID: coord.OwnerUserID,
		Body: "@bomclaw2 thế còn dòng cuối?",
	}); err != nil {
		t.Fatalf("follow-up: %v", err)
	}
	w.sweep(context.Background())

	if turn.count() != 2 {
		t.Fatalf("expected two turns, got %d", turn.count())
	}
	if turn.sessions[0] != turn.sessions[1] {
		t.Fatalf("thread ran under two different sessions: %s then %s", turn.sessions[0], turn.sessions[1])
	}
	if turn.resumed[0] != "" {
		t.Fatalf("first turn resumed something: %q", turn.resumed[0])
	}
	if turn.resumed[1] != "sess-1" {
		t.Fatalf("second turn did not resume the first: %q", turn.resumed[1])
	}
}

// TestThreadStopsAtTheTurnCap: A names B, B answers naming A, and nothing in a
// conversation ever says stop. The cap is the stop.
func TestThreadStopsAtTheTurnCap(t *testing.T) {
	turn := &recordingTurn{reply: "vẫn đây", newID: "sess-1"}
	deps, cdb := mentionTestDeps(t, turn)
	w := NewMentionWatcher(deps)

	root, _, err := cdb.PostMessage(coord.NewChannelMessage{
		ChannelID: coord.GeneralChannelID, AuthorKind: coord.MemberUser, AuthorID: coord.OwnerUserID,
		Body: "@bomclaw2 bắt đầu",
	})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	for i := 0; i < MaxThreadTurns+3; i++ {
		if _, _, err := cdb.PostMessage(coord.NewChannelMessage{
			ChannelID: coord.GeneralChannelID, ThreadRoot: root.ID,
			AuthorKind: coord.MemberAgent, AuthorID: "bomclaw",
			Body: "@bomclaw2 còn đó không",
		}); err != nil {
			t.Fatalf("post %d: %v", i, err)
		}
		w.sweep(context.Background())
	}
	if turn.count() != MaxThreadTurns {
		t.Fatalf("expected the thread to stop at %d turns, ran %d", MaxThreadTurns, turn.count())
	}
}

// TestStaleMentionIsClearedNotAnswered: answering yesterday's "are you there"
// is worse than not answering it.
func TestStaleMentionIsClearedNotAnswered(t *testing.T) {
	turn := &recordingTurn{reply: "muộn rồi"}
	deps, cdb := mentionTestDeps(t, turn)

	m, _, err := cdb.PostMessage(coord.NewChannelMessage{
		ChannelID: coord.GeneralChannelID, AuthorKind: coord.MemberUser, AuthorID: coord.OwnerUserID,
		Body: "@bomclaw2 còn thức không",
	})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	old := time.Now().Add(-StaleMention - time.Hour).UTC().Format(time.RFC3339Nano)
	if _, err := cdb.Conn().Exec(`UPDATE channel_messages SET created_at = ? WHERE id = ?`, old, m.ID); err != nil {
		t.Fatalf("age the message: %v", err)
	}

	NewMentionWatcher(deps).sweep(context.Background())

	if turn.count() != 0 {
		t.Fatalf("a day-old mention was answered")
	}
	left, err := cdb.UnreadMentions(coord.MemberAgent, "bomclaw2", 10)
	if err != nil {
		t.Fatalf("unread: %v", err)
	}
	if len(left) != 0 {
		t.Fatal("stale mention was left unread, so every sweep will look at it again")
	}
}

// TestPlainLineWakesNobody guards the shape of the feature: visibility and
// attention stay separate, so three agents in a room do not wake each other on
// every line.
func TestPlainLineWakesNobody(t *testing.T) {
	turn := &recordingTurn{reply: "không nên nói gì"}
	deps, cdb := mentionTestDeps(t, turn)

	if _, _, err := cdb.PostMessage(coord.NewChannelMessage{
		ChannelID: coord.GeneralChannelID, AuthorKind: coord.MemberUser, AuthorID: coord.OwnerUserID,
		Body: "hi bạn",
	}); err != nil {
		t.Fatalf("post: %v", err)
	}
	NewMentionWatcher(deps).sweep(context.Background())
	if turn.count() != 0 {
		t.Fatal("a line with no @ woke an agent")
	}
}

// TestTheChannelSessionIsRegistered: sessions built with session.New have no
// database row, and the messages table has a foreign key to it — so every
// channel turn logged "FOREIGN KEY constraint failed" and dropped both the
// question and the answer. The turn still worked, which is exactly why it went
// unnoticed: the only symptom was a conversation with nothing behind it.
func TestTheChannelSessionIsRegistered(t *testing.T) {
	turn := &recordingTurn{reply: "ừ", newID: "sess-1"}
	deps, cdb := mentionTestDeps(t, turn)
	deps.Sessions = session.NewManager(nil)

	if _, _, err := cdb.PostMessage(coord.NewChannelMessage{
		ChannelID: coord.GeneralChannelID, AuthorKind: coord.MemberUser, AuthorID: coord.OwnerUserID,
		Body: "@bomclaw2 chào",
	}); err != nil {
		t.Fatal(err)
	}
	NewMentionWatcher(deps).sweep(context.Background())

	if turn.calls != 1 {
		t.Fatalf("expected one turn, got %d", turn.calls)
	}
	var found *session.Session
	for _, s := range deps.Sessions.List() {
		if s.ID == turn.sessions[0] {
			found = s
		}
	}
	if found == nil {
		t.Fatalf("the session the turn ran on (%s) was never registered", turn.sessions[0])
	}
	if found.ChatID != channelChatID {
		t.Errorf("channel session should sit on chat %d, got %d", channelChatID, found.ChatID)
	}
}

// TestAdoptingTwiceKeepsOneSession: the second turn in a thread must not
// register a second session under the same id, or the two halves of one
// conversation end up in different rows.
func TestAdoptingTwiceKeepsOneSession(t *testing.T) {
	turn := &recordingTurn{reply: "ừ", newID: "sess-1"}
	deps, cdb := mentionTestDeps(t, turn)
	deps.Sessions = session.NewManager(nil)
	w := NewMentionWatcher(deps)

	root, _, err := cdb.PostMessage(coord.NewChannelMessage{
		ChannelID: coord.GeneralChannelID, AuthorKind: coord.MemberUser, AuthorID: coord.OwnerUserID,
		Body: "@bomclaw2 lần một",
	})
	if err != nil {
		t.Fatal(err)
	}
	w.sweep(context.Background())
	if _, _, err := cdb.PostMessage(coord.NewChannelMessage{
		ChannelID: coord.GeneralChannelID, ThreadRoot: root.ID,
		AuthorKind: coord.MemberUser, AuthorID: coord.OwnerUserID, Body: "@bomclaw2 lần hai",
	}); err != nil {
		t.Fatal(err)
	}
	w.sweep(context.Background())

	n := 0
	for _, s := range deps.Sessions.List() {
		if s.ChatID == channelChatID {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("one thread produced %d sessions", n)
	}
}

// TestAddressingAnAgentWakesItWithoutTyping: picking an agent from a dropdown
// and then also having to write "@bomclaw2" is asking the same question twice,
// and forgetting the second half is silence — which is exactly what happened
// the first time somebody used the picker.
func TestAddressingAnAgentWakesItWithoutTyping(t *testing.T) {
	turn := &recordingTurn{reply: "ừ", newID: "s1"}
	deps, _ := mentionTestDeps(t, turn)
	deps.Sessions = session.NewManager(nil)

	raw, err := handleChannelPost(deps, json.RawMessage(
		`{"channel_id":"`+coord.GeneralChannelID+`","body":"về kinh tế VN 3 tháng qua","notify":["bomclaw2"]}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	var posted coord.ChannelMessage
	if err := json.Unmarshal(raw, &posted); err != nil {
		t.Fatal(err)
	}
	// The author is the person, not the agent they addressed.
	if posted.AuthorKind != coord.MemberUser || posted.AuthorID != coord.OwnerUserID {
		t.Fatalf("a dashboard post was attributed to %s/%s", posted.AuthorKind, posted.AuthorID)
	}
	// And the body is untouched: addressing is structure, not prose.
	if posted.Body != "về kinh tế VN 3 tháng qua" {
		t.Fatalf("the body was rewritten: %q", posted.Body)
	}

	NewMentionWatcher(deps).sweep(context.Background())
	if turn.count() != 1 {
		t.Fatalf("the addressed agent did not answer (%d turns)", turn.count())
	}
}

// Addressing nobody still leaves the room readable and nobody interrupted.
func TestAddressingNobodyWakesNobody(t *testing.T) {
	turn := &recordingTurn{reply: "không nên nói gì"}
	deps, _ := mentionTestDeps(t, turn)
	deps.Sessions = session.NewManager(nil)

	if _, err := handleChannelPost(deps, json.RawMessage(
		`{"channel_id":"`+coord.GeneralChannelID+`","body":"ghi chú cho cả phòng"}`)); err != nil {
		t.Fatalf("post: %v", err)
	}
	NewMentionWatcher(deps).sweep(context.Background())
	if turn.count() != 0 {
		t.Fatal("a line addressed to nobody woke an agent")
	}
}

// TestAnAgentBroughtIntoAThreadGetsAllOfIt is the case that matters when you
// switch who you are talking to mid-conversation: the second agent has no
// session for this thread, so the prompt is the only thing it will ever know
// about it.
func TestAnAgentBroughtIntoAThreadGetsAllOfIt(t *testing.T) {
	turn := &recordingTurn{reply: "đã đọc", newID: "s1"}
	deps, cdb := mentionTestDeps(t, turn)
	deps.Sessions = session.NewManager(nil)

	root, _, err := cdb.PostMessage(coord.NewChannelMessage{
		ChannelID: coord.GeneralChannelID, AuthorKind: coord.MemberUser, AuthorID: coord.OwnerUserID,
		Body: "@bomclaw tổng hợp kinh tế VN 3 tháng qua",
	})
	if err != nil {
		t.Fatal(err)
	}
	// The first agent works in the thread. Its analysis is long — the thing
	// the old 400-rune cut used to destroy.
	longAnswer := "GDP quý gần nhất tăng 6.9%. " + strings.Repeat("Chi tiết từng ngành và nguồn số liệu. ", 30)
	if _, _, err := cdb.PostMessage(coord.NewChannelMessage{
		ChannelID: coord.GeneralChannelID, ThreadRoot: root.ID, AuthorID: "bomclaw", Body: longAnswer,
	}); err != nil {
		t.Fatal(err)
	}

	// Now the owner switches to the other agent, in the same thread.
	if _, _, err := cdb.PostMessage(coord.NewChannelMessage{
		ChannelID: coord.GeneralChannelID, ThreadRoot: root.ID,
		AuthorKind: coord.MemberUser, AuthorID: coord.OwnerUserID,
		Body:   "bạn thấy sao?",
		Notify: []coord.Member{{Kind: coord.MemberAgent, ID: "bomclaw2"}},
	}); err != nil {
		t.Fatal(err)
	}
	NewMentionWatcher(deps).sweep(context.Background())

	if turn.count() != 1 {
		t.Fatalf("the newly named agent did not answer (%d turns)", turn.count())
	}
	p := turn.prompts[0]
	// It must see the question that started the thread...
	if !strings.Contains(p, "tổng hợp kinh tế VN 3 tháng qua") {
		t.Error("the thread's opening question is missing from the prompt")
	}
	// ...and its colleague's answer, not a truncated stub of it.
	if !strings.Contains(p, "GDP quý gần nhất tăng 6.9%") {
		t.Error("the other agent's answer is missing")
	}
	if !strings.Contains(p, longAnswer[len(longAnswer)-40:]) {
		t.Error("the other agent's answer was cut off — a premise the question depends on")
	}
	// And it is told plainly that it is new here.
	if !strings.Contains(p, "not spoken here before") {
		t.Errorf("the prompt does not say it is new to the thread:\n%s", p)
	}
}

// An agent that has spoken here resumes its own session, so it needs what
// happened while it was away — not the whole thread pasted at it again.
func TestAReturningAgentGetsOnlyWhatItMissed(t *testing.T) {
	turn := &recordingTurn{reply: "ok", newID: "s1"}
	deps, cdb := mentionTestDeps(t, turn)
	deps.Sessions = session.NewManager(nil)
	w := NewMentionWatcher(deps)

	root, _, err := cdb.PostMessage(coord.NewChannelMessage{
		ChannelID: coord.GeneralChannelID, AuthorKind: coord.MemberUser, AuthorID: coord.OwnerUserID,
		Body: "@bomclaw2 câu hỏi mở đầu", Notify: []coord.Member{{Kind: coord.MemberAgent, ID: "bomclaw2"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	w.sweep(context.Background()) // bomclaw2 answers once, into the thread

	if _, _, err := cdb.PostMessage(coord.NewChannelMessage{
		ChannelID: coord.GeneralChannelID, ThreadRoot: root.ID, AuthorID: "bomclaw",
		Body: "một đồng nghiệp bổ sung dữ kiện mới",
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cdb.PostMessage(coord.NewChannelMessage{
		ChannelID: coord.GeneralChannelID, ThreadRoot: root.ID,
		AuthorKind: coord.MemberUser, AuthorID: coord.OwnerUserID, Body: "còn giờ thì sao?",
		Notify: []coord.Member{{Kind: coord.MemberAgent, ID: "bomclaw2"}},
	}); err != nil {
		t.Fatal(err)
	}
	w.sweep(context.Background())

	if turn.count() != 2 {
		t.Fatalf("expected two turns, got %d", turn.count())
	}
	p := turn.prompts[1]
	if !strings.Contains(p, "while you were away") {
		t.Errorf("a returning agent was treated as new:\n%s", p)
	}
	if !strings.Contains(p, "một đồng nghiệp bổ sung dữ kiện mới") {
		t.Error("what happened while it was away is missing")
	}
	// Its own earlier line is not pasted back at it: it resumes and remembers.
	if strings.Count(p, "câu hỏi mở đầu") > 0 {
		t.Error("the thread was replayed to an agent that already remembers it")
	}
}

// TestTheRoomSeesWorkInProgress: a turn that reads six files takes long enough
// that a silent thread is indistinguishable from a broken one. The progress
// line is what tells them apart — and it must become the answer, not sit above
// it, or every reply ends up with a running commentary attached.
func TestTheRoomSeesWorkInProgress(t *testing.T) {
	var duringBody string
	turn := &recordingTurn{reply: "xong rồi, đây là kết quả", newID: "s1"}
	deps, cdb := mentionTestDeps(t, turn)
	deps.Sessions = session.NewManager(nil)

	root, _, err := cdb.PostMessage(coord.NewChannelMessage{
		ChannelID: coord.GeneralChannelID, AuthorKind: coord.MemberUser, AuthorID: coord.OwnerUserID,
		Body: "xem hộ log", Notify: []coord.Member{{Kind: coord.MemberAgent, ID: "bomclaw2"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	turn.duringTurn = func(sink TurnSink) {
		// A tool reports immediately: which tool it reached for says more than
		// the half-sentence it has written.
		sink.NoteTool("Read")
		sink.NoteTool("Bash")
		thread, err := cdb.ThreadMessages(root.ID)
		if err != nil {
			t.Errorf("thread: %v", err)
			return
		}
		for _, m := range thread {
			if m.AuthorID == "bomclaw2" {
				duringBody = m.Body
			}
		}
	}

	NewMentionWatcher(deps).sweep(context.Background())

	if duringBody == "" {
		t.Fatal("the room saw nothing while the agent worked")
	}
	if !strings.Contains(duringBody, "đang làm") {
		t.Errorf("progress line does not say it is working: %q", duringBody)
	}
	for _, tool := range []string{"Read", "Bash"} {
		if !strings.Contains(duringBody, tool) {
			t.Errorf("progress line does not name %s: %q", tool, duringBody)
		}
	}

	// And when it is done, the progress is gone: one message, holding the
	// answer, in the place the progress line had.
	thread, err := cdb.ThreadMessages(root.ID)
	if err != nil {
		t.Fatal(err)
	}
	var mine []coord.ChannelMessage
	for _, m := range thread {
		if m.AuthorID == "bomclaw2" {
			mine = append(mine, m)
		}
	}
	if len(mine) != 1 {
		t.Fatalf("expected one message from the agent, got %d — progress was left behind", len(mine))
	}
	if mine[0].Body != "xong rồi, đây là kết quả" {
		t.Fatalf("the answer did not replace the progress: %q", mine[0].Body)
	}
	if strings.Contains(mine[0].Body, "đang làm") {
		t.Error("the finished reply still carries progress text")
	}
}

// A turn that produces nothing must not leave an agent permanently about to
// speak.
func TestAnEmptyTurnLeavesNoProgressBehind(t *testing.T) {
	turn := &recordingTurn{reply: "   "}
	deps, cdb := mentionTestDeps(t, turn)
	deps.Sessions = session.NewManager(nil)

	root, _, err := cdb.PostMessage(coord.NewChannelMessage{
		ChannelID: coord.GeneralChannelID, AuthorKind: coord.MemberUser, AuthorID: coord.OwnerUserID,
		Body: "hỏi gì đó", Notify: []coord.Member{{Kind: coord.MemberAgent, ID: "bomclaw2"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	NewMentionWatcher(deps).sweep(context.Background())

	thread, err := cdb.ThreadMessages(root.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range thread {
		if m.AuthorID == "bomclaw2" {
			t.Fatalf("a turn with nothing to say left %q in the thread", m.Body)
		}
	}
}

// TestThePromptNamesThePeers: the roster used to be a paragraph in each config,
// hand-copied — and it drifted exactly as that shape guarantees. Agent 1 said
// so out loud in a thread: "bomclaw3 là agent nào thì em vẫn chưa biết".
// Generated from the table that every gateway writes at startup, it cannot.
func TestThePromptNamesThePeers(t *testing.T) {
	turn := &recordingTurn{reply: "ok", newID: "s1"}
	deps, cdb := mentionTestDeps(t, turn)
	deps.Sessions = session.NewManager(nil)
	// A third agent appears, and nobody edits a config file.
	if err := cdb.RegisterAgent(coord.Agent{
		ID: "bomclaw3", DisplayName: "Agent 3", Provider: "opencode",
		Model: "opencode/muse-spark-1.3-contributor-free", WSAddr: "ws://127.0.0.1:0/ws",
	}); err != nil {
		t.Fatal(err)
	}

	if _, _, err := cdb.PostMessage(coord.NewChannelMessage{
		ChannelID: coord.GeneralChannelID, AuthorKind: coord.MemberUser, AuthorID: coord.OwnerUserID,
		Body: "ai giúp mình việc này", Notify: []coord.Member{{Kind: coord.MemberAgent, ID: "bomclaw2"}},
	}); err != nil {
		t.Fatal(err)
	}
	NewMentionWatcher(deps).sweep(context.Background())

	if turn.count() != 1 {
		t.Fatalf("expected one turn, got %d", turn.count())
	}
	p := turn.prompts[0]
	for _, want := range []string{"bomclaw", "bomclaw3", "opencode", "muse-spark"} {
		if !strings.Contains(p, want) {
			t.Errorf("the roster does not mention %q:\n%s", want, p)
		}
	}
	// And it does not introduce the agent to itself.
	if strings.Contains(p, "**bomclaw2** —") {
		t.Error("the agent was listed among its own peers")
	}
}

// TestTheThreadsOutputIsInThePrompt: an agent asked to review "the report" has
// a filename at best and a guess at worst. The artifact index gives it an id
// that survives the file moving, and the next agent brought in gets the same.
func TestTheThreadsOutputIsInThePrompt(t *testing.T) {
	turn := &recordingTurn{reply: "đã xem", newID: "s1"}
	deps, cdb := mentionTestDeps(t, turn)
	deps.Sessions = session.NewManager(nil)

	root, _, err := cdb.PostMessage(coord.NewChannelMessage{
		ChannelID: coord.GeneralChannelID, AuthorKind: coord.MemberUser, AuthorID: coord.OwnerUserID,
		Body: "tổng hợp việc làm IT Đà Nẵng",
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := cdb.CreateTask(coord.NewTask{CreatedBy: "bomclaw", Title: "tổng hợp"})
	if err != nil {
		t.Fatal(err)
	}
	if err := cdb.BindThreadToTask(root.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	art, err := cdb.PutArtifact(coord.NewArtifact{
		TaskID: task.ID, Kind: coord.ArtifactDocument, Title: "index.html",
		Content: []byte("<html>báo cáo</html>"),
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := cdb.PostMessage(coord.NewChannelMessage{
		ChannelID: coord.GeneralChannelID, ThreadRoot: root.ID,
		AuthorKind: coord.MemberUser, AuthorID: coord.OwnerUserID,
		Body: "review giúp mình", Notify: []coord.Member{{Kind: coord.MemberAgent, ID: "bomclaw2"}},
	}); err != nil {
		t.Fatal(err)
	}
	NewMentionWatcher(deps).sweep(context.Background())

	if turn.count() != 1 {
		t.Fatalf("expected one turn, got %d", turn.count())
	}
	p := turn.prompts[0]
	if !strings.Contains(p, art.ID) {
		t.Errorf("the artifact id is not in the prompt:\n%s", p)
	}
	if !strings.Contains(p, "index.html") {
		t.Error("the artifact title is missing")
	}
	if !strings.Contains(p, "artifact get") {
		t.Error("the prompt does not say how to read it")
	}
}

// A thread with no task behind it has produced nothing, and must not claim to.
func TestAThreadWithNoTaskListsNoFiles(t *testing.T) {
	turn := &recordingTurn{reply: "ok", newID: "s1"}
	deps, cdb := mentionTestDeps(t, turn)
	deps.Sessions = session.NewManager(nil)

	if _, _, err := cdb.PostMessage(coord.NewChannelMessage{
		ChannelID: coord.GeneralChannelID, AuthorKind: coord.MemberUser, AuthorID: coord.OwnerUserID,
		Body: "chào", Notify: []coord.Member{{Kind: coord.MemberAgent, ID: "bomclaw2"}},
	}); err != nil {
		t.Fatal(err)
	}
	NewMentionWatcher(deps).sweep(context.Background())
	if strings.Contains(turn.prompts[0], "has produced") {
		t.Error("a thread with no work behind it listed files")
	}
}
