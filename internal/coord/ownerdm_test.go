package coord

import "testing"

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
