package coord

import (
	"strings"
	"testing"
)

// The dashboard's Direct list was empty because every DM was agent↔agent and
// the owner was a member of nothing. "Message this one privately" had no room
// to happen in.
func TestTheOwnerGetsARoomWithEachAgent(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw", "bomclaw2", "bomclaw3")

	for _, id := range []string{"bomclaw", "bomclaw2", "bomclaw3"} {
		if _, err := db.EnsureOwnerDM(id); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}

	// From the owner's seat: three rooms, one per agent.
	mine, err := db.ListChannels(MemberUser, OwnerUserID)
	if err != nil {
		t.Fatal(err)
	}
	dms := map[string]bool{}
	for _, c := range mine {
		if c.Kind == ChannelDM {
			dms[c.Name] = true
		}
	}
	if len(dms) != 3 {
		t.Fatalf("the owner is in %d direct rooms, want one per agent: %v", len(dms), dms)
	}
	for _, id := range []string{"bomclaw", "bomclaw2", "bomclaw3"} {
		if !dms[id] {
			t.Errorf("no direct room with %s", id)
		}
	}
}

// Startup runs this every time. It must not reset the read cursor, or every
// restart would resurrect every message the owner had caught up on.
func TestEnsuringTheOwnerDMTwiceIsHarmless(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw")
	first, err := db.EnsureOwnerDM("bomclaw")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.PostMessage(NewChannelMessage{
		ChannelID: first.ID, AuthorID: "bomclaw", Body: "chào",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkChannelRead(first.ID, MemberUser, OwnerUserID); err != nil {
		t.Fatal(err)
	}

	again, err := db.EnsureOwnerDM("bomclaw")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID {
		t.Fatalf("made a second room: %s vs %s", again.ID, first.ID)
	}
	mine, _ := db.ListChannels(MemberUser, OwnerUserID)
	for _, c := range mine {
		if c.ID == first.ID && c.Unread != 0 {
			t.Errorf("a restart resurrected %d message(s) the owner had already read", c.Unread)
		}
	}
}

// Each agent answers on a different bot, so the screen has to say which.
func TestAnAgentRemembersWhichBotItAnswersOn(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw3")
	if err := db.SetAgentTelegramBot("bomclaw3", "@Goterm3_bot"); err != nil {
		t.Fatal(err)
	}
	agents, err := db.ListAgents()
	if err != nil {
		t.Fatal(err)
	}
	if agents[0].TelegramBot != "Goterm3_bot" {
		t.Errorf("telegram_bot = %q — the @ belongs in the link, not the stored name",
			agents[0].TelegramBot)
	}

	// A restart re-registers the agent, and must not wipe the name: the bot
	// logs in AFTER registration, so there is a window where clearing it would
	// leave the row blank until the next login.
	if err := db.RegisterAgent(Agent{ID: "bomclaw3", DisplayName: "bomclaw3"}); err != nil {
		t.Fatal(err)
	}
	agents, _ = db.ListAgents()
	if agents[0].TelegramBot != "Goterm3_bot" {
		t.Error("re-registering wiped which bot the agent answers on")
	}
}

// `bomclaw msg --to <anything>` took any string at all, and EnsureDM would
// build a room for it. An agent once passed a message id; the result was a
// permanent room named after that id, holding a report nobody would ever read.
func TestAMessageToSomebodyWhoIsNotAnAgentIsRefused(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw")

	before, _ := db.ListChannels("", "")
	if _, err := db.SendMessage("bomclaw", "cm_61e75346-b21c-4e43", "", "Xong báo cáo"); err == nil {
		t.Fatal("accepted a message id as a recipient")
	}
	after, _ := db.ListChannels("", "")
	if len(after) != len(before) {
		t.Fatalf("a room was created for a recipient that does not exist: %d → %d", len(before), len(after))
	}

	// And the refusal says where to look, because the agent reading it is the
	// one that has to pick a different name.
	_, err := db.SendMessage("bomclaw", "nobody", "", "hello")
	if err == nil || !strings.Contains(err.Error(), "bomclaw agents") {
		t.Errorf("error = %v — it does not say how to find a real recipient", err)
	}
}

// Archiving hides a room without destroying what was said in it. The rooms this
// cleans up were made by a bug, but the lines inside them are real things an
// agent said.
func TestArchivingHidesARoomAndKeepsItsMessages(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw", "bomclaw2")
	ch, err := db.EnsureDM("bomclaw", "bomclaw2")
	if err != nil {
		t.Fatal(err)
	}
	m, _, err := db.PostMessage(NewChannelMessage{
		ChannelID: ch.ID, AuthorID: "bomclaw", Body: "việc đã làm thật",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := db.ArchiveChannel(ch.ID); err != nil {
		t.Fatal(err)
	}
	list, _ := db.ListChannels(MemberAgent, "bomclaw")
	for _, c := range list {
		if c.ID == ch.ID {
			t.Fatal("an archived room is still listed")
		}
	}
	got, err := db.GetMessage(m.ID)
	if err != nil || got.Body != "việc đã làm thật" {
		t.Fatalf("archiving destroyed the message: %+v %v", got, err)
	}
	if err := db.ArchiveChannel(ch.ID); err == nil {
		t.Error("archiving twice reported success")
	}
}
