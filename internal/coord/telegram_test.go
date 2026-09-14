package coord

import (
	"testing"
	"time"
)

// bind returns a channel bound far enough in the past that messages posted
// during the test are after the cut-off.
func bind(t *testing.T, db *DB, channelID string, mode string) {
	t.Helper()
	if err := db.BindChannelTelegram(channelID, "bomclaw", 4242, mode); err != nil {
		t.Fatalf("bind %s: %v", channelID, err)
	}
	// The cut-off is created_at, and a test posts within the same millisecond.
	if _, err := db.conn.Exec(`UPDATE channel_telegram SET created_at = ? WHERE channel_id = ?`,
		ts(time.Now().Add(-time.Hour)), channelID); err != nil {
		t.Fatal(err)
	}
}

func TestProgressLineTravelsOnlyOnceItIsAnAnswer(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw", "bomclaw2")
	bind(t, db, GeneralChannelID, ForwardAll)

	// This is the shape the mention watcher writes: a ⏳ line first, then the
	// answer edited into the SAME row. Nothing new is ever inserted.
	m, _, err := db.PostMessage(NewChannelMessage{
		ChannelID: GeneralChannelID, AuthorID: "bomclaw2", Body: ProgressPrefix + "đang xem…",
	})
	if err != nil {
		t.Fatal(err)
	}

	pending, err := db.PendingForwards("bomclaw", ProgressPrefix, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("a progress line was queued for Telegram: %+v\n"+
			"the owner would be sent the word \"thinking\" and never the reply", pending)
	}

	if err := db.UpdateMessageBody(m.ID, "BTC 64k, ETH 3.1k"); err != nil {
		t.Fatal(err)
	}
	pending, err = db.PendingForwards("bomclaw", ProgressPrefix, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].MessageID != m.ID {
		t.Fatalf("the answer never became forwardable: %+v", pending)
	}
	if pending[0].Body != "BTC 64k, ETH 3.1k" {
		t.Errorf("forwarded the old body %q", pending[0].Body)
	}
	if pending[0].ChatID != 4242 {
		t.Errorf("chat id = %d, want the bound one", pending[0].ChatID)
	}
}

func TestAPersonsOwnWordsAreNeverSentBackToThem(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw")
	bind(t, db, GeneralChannelID, ForwardAll)

	// This is what a reply arriving from Telegram looks like once written.
	if _, _, err := db.PostMessage(NewChannelMessage{
		ChannelID: GeneralChannelID, AuthorKind: MemberUser, AuthorID: OwnerUserID,
		Body: "gộp cả BTC nữa",
	}); err != nil {
		t.Fatal(err)
	}
	pending, err := db.PendingForwards("bomclaw", ProgressPrefix, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("the owner's own line was queued to be sent to the owner: %+v\n"+
			"that is the echo loop, and it repeats forever", pending)
	}
}

func TestMentionsModeCarriesTheThreadTheOwnerIsIn(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw", "bomclaw2")
	bind(t, db, GeneralChannelID, ForwardMentions)

	// A room talking to itself, with the owner nowhere in it.
	chatter, _, err := db.PostMessage(NewChannelMessage{
		ChannelID: GeneralChannelID, AuthorID: "bomclaw", Body: "đã build xong",
	})
	if err != nil {
		t.Fatal(err)
	}
	follows, err := db.OwnerFollows(chatter.ID, chatter.ThreadRoot)
	if err != nil {
		t.Fatal(err)
	}
	if follows {
		t.Error("mode=mentions would carry a line the owner has nothing to do with")
	}

	// Now the round trip: the owner asks, an agent answers in the thread.
	root, _, err := db.PostMessage(NewChannelMessage{
		ChannelID: GeneralChannelID, AuthorID: "bomclaw2", Body: "giá hôm nay thế nào",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.PostMessage(NewChannelMessage{
		ChannelID: GeneralChannelID, ThreadRoot: root.ID,
		AuthorKind: MemberUser, AuthorID: OwnerUserID, Body: "thêm BTC",
	}); err != nil {
		t.Fatal(err)
	}
	answer, _, err := db.PostMessage(NewChannelMessage{
		ChannelID: GeneralChannelID, ThreadRoot: root.ID,
		AuthorID: "bomclaw2", Body: "BTC 64k",
	})
	if err != nil {
		t.Fatal(err)
	}
	follows, err = db.OwnerFollows(answer.ID, answer.ThreadRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !follows {
		t.Error("an answer to the owner's own question would not be sent to them —\n" +
			"the round trip is broken and nobody would type @owner to fix it")
	}
}

func TestNothingOlderThanTheBindingTravels(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw")

	if _, _, err := db.PostMessage(NewChannelMessage{
		ChannelID: GeneralChannelID, AuthorID: "bomclaw", Body: "tuần trước",
	}); err != nil {
		t.Fatal(err)
	}
	// Bound now, with its real created_at: everything above predates it.
	if err := db.BindChannelTelegram(GeneralChannelID, "bomclaw", 4242, ForwardAll); err != nil {
		t.Fatal(err)
	}
	pending, err := db.PendingForwards("bomclaw", ProgressPrefix, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("binding a busy room would empty its backlog onto a phone: %+v", pending)
	}
}

func TestASettledLineIsNotReconsidered(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw")
	bind(t, db, GeneralChannelID, ForwardAll)

	m, _, err := db.PostMessage(NewChannelMessage{
		ChannelID: GeneralChannelID, AuthorID: "bomclaw", Body: "xong",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MarkForwarded("bomclaw", m.ID, 8821); err != nil {
		t.Fatal(err)
	}
	pending, err := db.PendingForwards("bomclaw", ProgressPrefix, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("a delivered line came back round: %+v", pending)
	}

	// A line the mode filtered out is settled the same way, with no Telegram
	// id — otherwise every sweep re-asks a question whose answer cannot change.
	other, _, err := db.PostMessage(NewChannelMessage{
		ChannelID: GeneralChannelID, AuthorID: "bomclaw", Body: "và cái này nữa",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MarkForwarded("bomclaw", other.ID, 0); err != nil {
		t.Fatal(err)
	}
	pending, err = db.PendingForwards("bomclaw", ProgressPrefix, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("a filtered line stayed on the treadmill: %+v", pending)
	}
}

func TestATelegramMessageFindsItsLineBack(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw")
	bind(t, db, GeneralChannelID, ForwardAll)

	m, _, err := db.PostMessage(NewChannelMessage{
		ChannelID: GeneralChannelID, AuthorID: "bomclaw", Body: "báo cáo đây",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MarkForwarded("bomclaw", m.ID, 8821); err != nil {
		t.Fatal(err)
	}

	found, err := db.ForwardedMessage("bomclaw", 8821)
	if err != nil {
		t.Fatal(err)
	}
	if found == nil || found.ID != m.ID {
		t.Fatalf("a reply on Telegram could not find its line: %+v", found)
	}

	// Anything else the owner types must keep meaning "talk to the agent".
	found, err = db.ForwardedMessage("bomclaw", 9999)
	if err != nil {
		t.Fatalf("an unknown Telegram id must be an ordinary answer, not an error: %v", err)
	}
	if found != nil {
		t.Fatalf("matched a Telegram message that was never ours: %+v", found)
	}
}

func TestBindRejectsAModeNobodyImplements(t *testing.T) {
	db := testDB(t)
	if err := db.BindChannelTelegram(GeneralChannelID, "bomclaw", 4242, "sometimes"); err == nil {
		t.Fatal("accepted an unknown mode; it would silently forward nothing")
	}
	if err := db.BindChannelTelegram(GeneralChannelID, "bomclaw", 0, ForwardAll); err == nil {
		t.Fatal("accepted chat id 0, which sends every line nowhere")
	}
}

func TestRebindingKeepsTheCutOff(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw")
	bind(t, db, GeneralChannelID, ForwardMentions)

	before, err := db.ChannelBinding(GeneralChannelID)
	if err != nil || before == nil {
		t.Fatalf("binding: %+v %v", before, err)
	}
	if err := db.BindChannelTelegram(GeneralChannelID, "bomclaw", 4242, ForwardAll); err != nil {
		t.Fatal(err)
	}
	after, err := db.ChannelBinding(GeneralChannelID)
	if err != nil || after == nil {
		t.Fatalf("binding: %+v %v", after, err)
	}
	if !after.CreatedAt.Equal(before.CreatedAt) {
		t.Error("changing the mode reset the cut-off, which replays the backlog")
	}
	if after.Mode != ForwardAll {
		t.Errorf("mode = %q, want the new one", after.Mode)
	}
}

func TestUnbindingStopsEverything(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw")
	bind(t, db, GeneralChannelID, ForwardAll)
	if err := db.UnbindChannelTelegram(GeneralChannelID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.PostMessage(NewChannelMessage{
		ChannelID: GeneralChannelID, AuthorID: "bomclaw", Body: "vẫn nói",
	}); err != nil {
		t.Fatal(err)
	}
	pending, err := db.PendingForwards("bomclaw", ProgressPrefix, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("an unbound room kept sending: %+v", pending)
	}
	b, err := db.ChannelBinding(GeneralChannelID)
	if err != nil {
		t.Fatal(err)
	}
	if b != nil {
		t.Errorf("binding survived the unbind: %+v", b)
	}
}

// Three agents on this machine, three Telegram bots, and message ids that
// collide across them: @Goterm_bot's message 8821 and @Goterm3_bot's 8821 are
// different messages. Both halves of the round trip have to know whose bot
// they are talking about.
func TestTwoBotsDoNotReadEachOthersMessageIds(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw", "bomclaw3")
	bind(t, db, GeneralChannelID, ForwardAll)

	mine, _, err := db.PostMessage(NewChannelMessage{
		ChannelID: GeneralChannelID, AuthorID: "bomclaw", Body: "của agent 1",
	})
	if err != nil {
		t.Fatal(err)
	}
	theirs, _, err := db.PostMessage(NewChannelMessage{
		ChannelID: GeneralChannelID, AuthorID: "bomclaw3", Body: "của agent 3",
	})
	if err != nil {
		t.Fatal(err)
	}
	// The same Telegram id from two different bots.
	if err := db.MarkForwarded("bomclaw", mine.ID, 8821); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkForwarded("bomclaw3", theirs.ID, 8821); err != nil {
		t.Fatal(err)
	}

	got, err := db.ForwardedMessage("bomclaw", 8821)
	if err != nil || got == nil {
		t.Fatalf("lookup: %+v %v", got, err)
	}
	if got.ID != mine.ID {
		t.Fatalf("a reply to agent 1's bot was matched against another bot's line —\n" +
			"it would be answered into a thread the person was not even looking at")
	}
	got, err = db.ForwardedMessage("bomclaw3", 8821)
	if err != nil || got == nil || got.ID != theirs.ID {
		t.Fatalf("agent 3's own line was not found by its own id: %+v %v", got, err)
	}
}

// A room is carried by exactly one bot. Every gateway runs the same sweep, and
// if they all saw every pending line they would race for it — the loser's send
// having already gone out, which is two notifications for one line.
func TestOnlyTheBoundAgentSeesTheRoom(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw", "bomclaw3")
	bind(t, db, GeneralChannelID, ForwardAll)

	if _, _, err := db.PostMessage(NewChannelMessage{
		ChannelID: GeneralChannelID, AuthorID: "bomclaw3", Body: "một dòng",
	}); err != nil {
		t.Fatal(err)
	}
	mine, err := db.PendingForwards("bomclaw", ProgressPrefix, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(mine) != 1 {
		t.Fatalf("the bound agent cannot see its own room: %+v", mine)
	}
	theirs, err := db.PendingForwards("bomclaw3", ProgressPrefix, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(theirs) != 0 {
		t.Fatalf("a second gateway would race to send the same line: %+v", theirs)
	}
}
