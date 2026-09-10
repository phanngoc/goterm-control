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
