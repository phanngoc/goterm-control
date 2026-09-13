package coord

import (
	"strings"
	"testing"
)

func registerTestAgents(t *testing.T, db *DB, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if err := db.RegisterAgent(Agent{ID: id, DisplayName: id, WSAddr: "ws://127.0.0.1:0/ws"}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
}

func TestDMChannelIDIsTheSameFromBothSides(t *testing.T) {
	if DMChannelID("bomclaw", "bomclaw3") != DMChannelID("bomclaw3", "bomclaw") {
		t.Fatal("a DM resolved to two different rooms depending on who spoke first")
	}
}

func TestOnlyMentionsWake(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw", "bomclaw2", "bomclaw3")

	// A plain channel line reaches everyone's eyes and nobody's doorbell.
	_, wake, err := db.PostMessage(NewChannelMessage{
		ChannelID: GeneralChannelID, AuthorID: "bomclaw",
		Body: "starting on the landing page",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(wake) != 0 {
		t.Errorf("woke %v for an unaddressed line; only a mention may wake an agent", wake)
	}

	// Naming someone does.
	_, wake, err = db.PostMessage(NewChannelMessage{
		ChannelID: GeneralChannelID, AuthorID: "bomclaw",
		Body: "@bomclaw3 can you take the copy? @bomclaw is already on markup",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(wake) != 1 || wake[0] != "bomclaw3" {
		t.Errorf("wake = %v, want [bomclaw3] — naming yourself is not a summons", wake)
	}
}

func TestUnknownMentionIsJustText(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw")

	_, wake, err := db.PostMessage(NewChannelMessage{
		ChannelID: GeneralChannelID, AuthorID: "bomclaw",
		Body: "mail ops@example.com about the `@type` tag",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(wake) != 0 {
		t.Errorf("wake = %v, want none: prose is full of @ and none of it is a summons", wake)
	}
}

func TestMentionPullsAnAgentIntoTheRoom(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw", "bomclaw3")

	ch, err := db.CreateChannel("", "landing", ChannelPublic, "", "bomclaw",
		[]Member{{Kind: MemberAgent, ID: "bomclaw"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.PostMessage(NewChannelMessage{
		ChannelID: ch.ID, AuthorID: "bomclaw", Body: "@bomclaw3 need you here",
	}); err != nil {
		t.Fatal(err)
	}

	got, err := db.GetChannel(ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !containsMember(got.Members, MemberAgent, "bomclaw3") {
		t.Fatal("bomclaw3 was named but not added — the summons would go nowhere")
	}
}

func TestThreadsStayOneLevelDeep(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw", "bomclaw2")

	root, _, err := db.PostMessage(NewChannelMessage{
		ChannelID: GeneralChannelID, AuthorID: "bomclaw", Body: "the scanner is failing on empty stderr",
	})
	if err != nil {
		t.Fatal(err)
	}
	reply, _, err := db.PostMessage(NewChannelMessage{
		ChannelID: GeneralChannelID, ThreadRoot: root.ID, AuthorID: "bomclaw2", Body: "looking",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Replying to a reply belongs to the same thread, not a thread of its own.
	deep, _, err := db.PostMessage(NewChannelMessage{
		ChannelID: GeneralChannelID, ThreadRoot: reply.ID, AuthorID: "bomclaw", Body: "thanks",
	})
	if err != nil {
		t.Fatal(err)
	}
	if deep.ThreadRoot != root.ID {
		t.Errorf("thread root = %q, want %q", deep.ThreadRoot, root.ID)
	}

	thread, err := db.ThreadMessages(root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(thread) != 3 {
		t.Fatalf("thread has %d messages, want 3", len(thread))
	}

	// The channel's main line shows the root once, with its reply count —
	// not the replies as separate lines.
	main, err := db.ChannelMessages(GeneralChannelID, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(main) != 1 {
		t.Fatalf("main line has %d messages, want 1", len(main))
	}
	if main[0].Replies != 2 {
		t.Errorf("replies = %d, want 2", main[0].Replies)
	}
}

func TestUnreadAndMentionsAreDifferentQuestions(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw", "bomclaw2")

	for _, body := range []string{"one", "two", "@bomclaw2 three"} {
		if _, _, err := db.PostMessage(NewChannelMessage{
			ChannelID: GeneralChannelID, AuthorID: "bomclaw", Body: body,
		}); err != nil {
			t.Fatal(err)
		}
	}

	chans, err := db.ListChannels(MemberAgent, "bomclaw2")
	if err != nil {
		t.Fatal(err)
	}
	var general *Channel
	for i := range chans {
		if chans[i].ID == GeneralChannelID {
			general = &chans[i]
		}
	}
	if general == nil {
		t.Fatal("bomclaw2 is not in #general")
	}
	if general.Unread != 3 {
		t.Errorf("unread = %d, want 3", general.Unread)
	}
	if general.Mentions != 1 {
		t.Errorf("mentions = %d, want 1 — only one line named it", general.Mentions)
	}

	// Catching up on the room does not answer the summons.
	if err := db.MarkChannelRead(GeneralChannelID, MemberAgent, "bomclaw2"); err != nil {
		t.Fatal(err)
	}
	pending, err := db.UnreadMentions(MemberAgent, "bomclaw2", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Errorf("%d mentions still pending, want 1: reading the channel is not answering", len(pending))
	}
}

func TestOneAgentReadingDoesNotClearAnother(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw", "bomclaw2", "bomclaw3")

	m, _, err := db.PostMessage(NewChannelMessage{
		ChannelID: GeneralChannelID, AuthorID: "bomclaw",
		Body: "@bomclaw2 @bomclaw3 both of you, please",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkMentionsRead(MemberAgent, "bomclaw2", []string{m.ID}); err != nil {
		t.Fatal(err)
	}

	still, err := db.UnreadMentions(MemberAgent, "bomclaw3", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(still) != 1 {
		t.Fatalf("bomclaw3 has %d pending, want 1 — bomclaw2 answered for itself only", len(still))
	}
}

func TestSendMessageStillWorksAndLandsInTheInbox(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw", "bomclaw2")

	m, err := db.SendMessage("bomclaw", "bomclaw2", "t_123", "patch is ready")
	if err != nil {
		t.Fatal(err)
	}
	if m.ChannelID != DMChannelID("bomclaw", "bomclaw2") {
		t.Errorf("landed in %q, want the pair's DM", m.ChannelID)
	}
	if strings.Contains(m.Body, "@") {
		t.Errorf("body was rewritten to %q; addressing is not the author's prose", m.Body)
	}

	inbox, err := db.Inbox("bomclaw2", true, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 || inbox[0].FromAgent != "bomclaw" || inbox[0].TaskID != "t_123" {
		t.Fatalf("inbox = %+v, want one message from bomclaw about t_123", inbox)
	}
	if _, err := db.MarkRead("bomclaw2", []string{inbox[0].ID}); err != nil {
		t.Fatal(err)
	}
	if n, _ := db.UnreadCount("bomclaw2"); n != 0 {
		t.Errorf("unread = %d after marking read, want 0", n)
	}
}

func TestAgentMessagesAreMigratedIntoChannels(t *testing.T) {
	db := testDB(t)

	// Simulate a database written by the previous version: rows in the old
	// table, and no record of a migration having run.
	if _, err := db.conn.Exec(`INSERT INTO agent_messages
		(id, from_agent, to_agent, task_id, body, read_at, created_at)
		VALUES ('m_old', 'bomclaw2', 'bomclaw', 't_9', 'identity check', '', '2026-09-01T10:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.Exec(`DELETE FROM meta WHERE key = 'channels_migrated'`); err != nil {
		t.Fatal(err)
	}
	if err := db.migrateMessagesToChannels(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	inbox, err := db.Inbox("bomclaw", false, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 || inbox[0].ID != "m_old" {
		t.Fatalf("inbox = %+v, want the old message under its original id", inbox)
	}
	if inbox[0].ChannelID != DMChannelID("bomclaw", "bomclaw2") {
		t.Errorf("old message landed in %q", inbox[0].ChannelID)
	}
	// Running twice must not duplicate it.
	if err := db.migrateMessagesToChannels(); err != nil {
		t.Fatal(err)
	}
	if again, _ := db.Inbox("bomclaw", false, 10); len(again) != 1 {
		t.Errorf("after a second migration the inbox has %d messages, want 1", len(again))
	}
}

func TestAnAgentCanLeaveItselfANote(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw")

	// Agents already did this with `msg --to <self>`, and the migrated
	// history contains such rows; it must keep working.
	m, err := db.SendMessage("bomclaw", "bomclaw", "t_7", "lease was lost, redo the review")
	if err != nil {
		t.Fatalf("self-message: %v", err)
	}
	inbox, err := db.Inbox("bomclaw", true, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 || inbox[0].ID != m.ID {
		t.Fatalf("inbox = %+v, want the note it left itself", inbox)
	}

	// Writing your own name in a sentence, though, is not a summons.
	_, wake, err := db.PostMessage(NewChannelMessage{
		ChannelID: GeneralChannelID, AuthorID: "bomclaw", Body: "@bomclaw is on markup",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(wake) != 0 {
		t.Errorf("wake = %v, want none — nobody rings their own doorbell", wake)
	}
}

// TestMainLineCarriesTheNewestReply: a count on its own reads like silence next
// to a question you asked an agent — which is how the first working reply was
// missed on the dashboard.
func TestMainLineCarriesTheNewestReply(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw", "bomclaw2")

	root, _, err := db.PostMessage(NewChannelMessage{
		ChannelID: GeneralChannelID, AuthorKind: MemberUser, AuthorID: OwnerUserID,
		Body: "@bomclaw2 kênh đã thông chưa",
	})
	if err != nil {
		t.Fatalf("post: %v", err)
	}

	// No replies yet: no preview, and nothing pretending there is one.
	line, err := db.ChannelMessages(GeneralChannelID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if line[0].Replies != 0 || line[0].LastReplyBy != "" || line[0].LastReplyText != "" {
		t.Fatalf("unanswered message carries a reply preview: %+v", line[0])
	}

	for _, body := range []string{"đang xem", "rồi nhé, thông rồi"} {
		if _, _, err := db.PostMessage(NewChannelMessage{
			ChannelID: GeneralChannelID, ThreadRoot: root.ID, AuthorID: "bomclaw2", Body: body,
		}); err != nil {
			t.Fatalf("reply: %v", err)
		}
	}

	line, err = db.ChannelMessages(GeneralChannelID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if line[0].Replies != 2 {
		t.Fatalf("replies: got %d, want 2", line[0].Replies)
	}
	// The NEWEST one — an answer two replies old is worse than none.
	if line[0].LastReplyBy != "bomclaw2" || line[0].LastReplyText != "rồi nhé, thông rồi" {
		t.Fatalf("preview should be the newest reply, got %q by %q", line[0].LastReplyText, line[0].LastReplyBy)
	}

	// Replies stay out of the main line: the room is roots, threads are threads.
	if len(line) != 1 {
		t.Fatalf("thread replies leaked into the main line: %d messages", len(line))
	}
}

// TestReplyPreviewIsCut keeps one long reply from pushing the room off screen.
func TestReplyPreviewIsCut(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw", "bomclaw2")
	root, _, _ := db.PostMessage(NewChannelMessage{
		ChannelID: GeneralChannelID, AuthorKind: MemberUser, AuthorID: OwnerUserID, Body: "@bomclaw2 báo cáo đi",
	})
	if _, _, err := db.PostMessage(NewChannelMessage{
		ChannelID: GeneralChannelID, ThreadRoot: root.ID, AuthorID: "bomclaw2",
		Body: strings.Repeat("báo cáo rất dài ", 100),
	}); err != nil {
		t.Fatal(err)
	}
	line, _ := db.ChannelMessages(GeneralChannelID, 10)
	if n := len([]rune(line[0].LastReplyText)); n > ReplyPreviewRunes {
		t.Fatalf("preview is %d runes, cap is %d", n, ReplyPreviewRunes)
	}
}

// TestHumanReplyWakesTheThread: answering inside a thread is talking to the
// agents already in it. Having to retype @bomclaw2 under its own reply is not
// a conversation.
func TestHumanReplyWakesTheThread(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw", "bomclaw2", "bomclaw3")

	root, _, err := db.PostMessage(NewChannelMessage{
		ChannelID: GeneralChannelID, AuthorKind: MemberUser, AuthorID: OwnerUserID,
		Body: "@bomclaw2 xem hộ log",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.PostMessage(NewChannelMessage{
		ChannelID: GeneralChannelID, ThreadRoot: root.ID, AuthorID: "bomclaw2", Body: "xong rồi",
	}); err != nil {
		t.Fatal(err)
	}

	// A follow-up with no @ at all.
	_, wake, err := db.PostMessage(NewChannelMessage{
		ChannelID: GeneralChannelID, ThreadRoot: root.ID,
		AuthorKind: MemberUser, AuthorID: OwnerUserID, Body: "thế còn dòng cuối?",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(wake) != 1 || wake[0] != "bomclaw2" {
		t.Fatalf("a person's follow-up should wake the agent in the thread, got %v", wake)
	}
	// And it must be readable as a summons, or the watcher never sees it.
	unread, err := db.UnreadMentions(MemberAgent, "bomclaw2", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(unread) != 2 {
		t.Fatalf("bomclaw2 should have the original mention and the follow-up, got %d", len(unread))
	}

	// An agent that never spoke in the thread stays out of it.
	if u, _ := db.UnreadMentions(MemberAgent, "bomclaw3", 10); len(u) != 0 {
		t.Fatalf("bomclaw3 was pulled into a thread it is not in: %v", u)
	}
}

// TestAgentReplyDoesNotWakeTheThread guards the loop the mention rule exists to
// prevent: three agents in one room answering each other's answers forever.
func TestAgentReplyDoesNotWakeTheThread(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw", "bomclaw2")

	root, _, _ := db.PostMessage(NewChannelMessage{
		ChannelID: GeneralChannelID, AuthorKind: MemberUser, AuthorID: OwnerUserID,
		Body: "@bomclaw2 bắt đầu đi",
	})
	if _, _, err := db.PostMessage(NewChannelMessage{
		ChannelID: GeneralChannelID, ThreadRoot: root.ID, AuthorID: "bomclaw2", Body: "ok",
	}); err != nil {
		t.Fatal(err)
	}

	// bomclaw speaks in the thread without naming anyone.
	_, wake, err := db.PostMessage(NewChannelMessage{
		ChannelID: GeneralChannelID, ThreadRoot: root.ID, AuthorID: "bomclaw", Body: "mình cũng đang xem",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(wake) != 0 {
		t.Fatalf("an agent's thread reply woke %v — that is the ping-pong loop", wake)
	}
}

// TestThreadFollowUpSkipsTheAuthor: nobody rings their own doorbell, and the
// owner is not an agent to be woken.
func TestThreadFollowUpSkipsTheAuthor(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw2")

	root, _, _ := db.PostMessage(NewChannelMessage{
		ChannelID: GeneralChannelID, AuthorKind: MemberUser, AuthorID: OwnerUserID, Body: "@bomclaw2 hi",
	})
	_, wake, err := db.PostMessage(NewChannelMessage{
		ChannelID: GeneralChannelID, ThreadRoot: root.ID,
		AuthorKind: MemberUser, AuthorID: OwnerUserID, Body: "còn đó không",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Only the agent that spoke... and here none has, so nobody is woken: the
	// root is the owner's own line.
	for _, w := range wake {
		if w == OwnerUserID {
			t.Fatal("the owner was queued as an agent to wake")
		}
	}
}
