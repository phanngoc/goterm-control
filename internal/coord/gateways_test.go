package coord

import (
	"testing"
	"time"
)

// botAgents registers agents that have logged a Telegram bot in, which is what
// a telegram gateway requires of its carrier.
func botAgents(t *testing.T, db *DB, ids ...string) {
	t.Helper()
	registerTestAgents(t, db, ids...)
	for _, id := range ids {
		if err := db.SetAgentTelegramBot(id, "Goterm_"+id+"_bot"); err != nil {
			t.Fatalf("set bot for %s: %v", id, err)
		}
	}
}

// bindTG registers a telegram gateway far enough in the past that messages
// posted during the test are after the cut-off.
func bindTG(t *testing.T, db *DB, channelID, agentID, target, mode string) ChannelGateway {
	t.Helper()
	g, err := db.AddChannelGateway(ChannelGateway{
		ChannelID: channelID, Kind: GatewayTelegram, AgentID: agentID, Target: target, Mode: mode,
	})
	if err != nil {
		t.Fatalf("bind %s: %v", channelID, err)
	}
	backdate(t, db, g.ID)
	return g
}

func bindHook(t *testing.T, db *DB, channelID, agentID, target, mode string) ChannelGateway {
	t.Helper()
	g, err := db.AddChannelGateway(ChannelGateway{
		ChannelID: channelID, Kind: GatewayWebhook, AgentID: agentID, Target: target, Mode: mode,
	})
	if err != nil {
		t.Fatalf("bind webhook on %s: %v", channelID, err)
	}
	backdate(t, db, g.ID)
	return g
}

// backdate moves a gateway's cut-off an hour back: a test posts within the same
// millisecond as the bind, and the cut-off is strictly greater-than.
func backdate(t *testing.T, db *DB, id string) {
	t.Helper()
	if _, err := db.conn.Exec(`UPDATE channel_gateways SET since = ? WHERE id = ?`,
		ts(time.Now().Add(-time.Hour)), id); err != nil {
		t.Fatal(err)
	}
}

func post(t *testing.T, db *DB, author, body string) *ChannelMessage {
	t.Helper()
	m, _, err := db.PostMessage(NewChannelMessage{
		ChannelID: GeneralChannelID, AuthorID: author, Body: body,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func pendingFor(t *testing.T, db *DB, agentID string) []Forward {
	t.Helper()
	p, err := db.PendingForwards(agentID, ProgressPrefix, 10)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProgressLineTravelsOnlyOnceItIsAnAnswer(t *testing.T) {
	db := testDB(t)
	botAgents(t, db, "bomclaw", "bomclaw2")
	bindTG(t, db, GeneralChannelID, "bomclaw", "4242", ForwardAll)

	// This is the shape the mention watcher writes: a ⏳ line first, then the
	// answer edited into the SAME row. Nothing new is ever inserted.
	m := post(t, db, "bomclaw2", ProgressPrefix+"đang xem…")

	if pending := pendingFor(t, db, "bomclaw"); len(pending) != 0 {
		t.Fatalf("a progress line was queued for Telegram: %+v\n"+
			"the owner would be sent the word \"thinking\" and never the reply", pending)
	}

	if err := db.UpdateMessageBody(m.ID, "BTC 64k, ETH 3.1k"); err != nil {
		t.Fatal(err)
	}
	pending := pendingFor(t, db, "bomclaw")
	if len(pending) != 1 || pending[0].MessageID != m.ID {
		t.Fatalf("the answer never became forwardable: %+v", pending)
	}
	if pending[0].Body != "BTC 64k, ETH 3.1k" {
		t.Errorf("forwarded the old body %q", pending[0].Body)
	}
	if pending[0].Target != "4242" || pending[0].Kind != GatewayTelegram {
		t.Errorf("address = %s/%s, want the bound one", pending[0].Kind, pending[0].Target)
	}
}

func TestAPersonsOwnWordsAreNeverSentBackToThem(t *testing.T) {
	db := testDB(t)
	botAgents(t, db, "bomclaw")
	bindTG(t, db, GeneralChannelID, "bomclaw", "4242", ForwardAll)

	// This is what a reply arriving from Telegram looks like once written.
	if _, _, err := db.PostMessage(NewChannelMessage{
		ChannelID: GeneralChannelID, AuthorKind: MemberUser, AuthorID: OwnerUserID,
		Body: "gộp cả BTC nữa",
	}); err != nil {
		t.Fatal(err)
	}
	if pending := pendingFor(t, db, "bomclaw"); len(pending) != 0 {
		t.Fatalf("the owner's own line was queued to be sent to the owner: %+v\n"+
			"that is the echo loop, and it repeats forever", pending)
	}
}

func TestMentionsModeCarriesTheThreadTheOwnerIsIn(t *testing.T) {
	db := testDB(t)
	botAgents(t, db, "bomclaw", "bomclaw2")
	bindTG(t, db, GeneralChannelID, "bomclaw", "4242", ForwardMentions)

	// A room talking to itself, with the owner nowhere in it.
	chatter := post(t, db, "bomclaw", "đã build xong")
	follows, err := db.OwnerFollows(chatter.ID, chatter.ThreadRoot)
	if err != nil {
		t.Fatal(err)
	}
	if follows {
		t.Error("mode=mentions would carry a line the owner has nothing to do with")
	}

	// Now the round trip: the owner asks, an agent answers in the thread.
	root := post(t, db, "bomclaw2", "giá hôm nay thế nào")
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
	botAgents(t, db, "bomclaw")

	post(t, db, "bomclaw", "tuần trước")
	// Bound now, with its real cut-off: everything above predates it.
	if _, err := db.AddChannelGateway(ChannelGateway{
		ChannelID: GeneralChannelID, Kind: GatewayTelegram, AgentID: "bomclaw",
		Target: "4242", Mode: ForwardAll,
	}); err != nil {
		t.Fatal(err)
	}
	if pending := pendingFor(t, db, "bomclaw"); len(pending) != 0 {
		t.Fatalf("binding a busy room would empty its backlog onto a phone: %+v", pending)
	}
}

func TestASettledLineIsNotReconsidered(t *testing.T) {
	db := testDB(t)
	botAgents(t, db, "bomclaw")
	bindTG(t, db, GeneralChannelID, "bomclaw", "4242", ForwardAll)

	post(t, db, "bomclaw", "xong")
	pending := pendingFor(t, db, "bomclaw")
	if len(pending) != 1 {
		t.Fatalf("nothing to settle: %+v", pending)
	}
	if err := db.RecordDelivery(pending[0], DeliverySent, "8821"); err != nil {
		t.Fatal(err)
	}
	if left := pendingFor(t, db, "bomclaw"); len(left) != 0 {
		t.Fatalf("a delivered line came back round: %+v", left)
	}

	// A line the mode filtered out is settled the same way, with no Telegram
	// id — otherwise every sweep re-asks a question whose answer cannot change.
	post(t, db, "bomclaw", "và cái này nữa")
	pending = pendingFor(t, db, "bomclaw")
	if len(pending) != 1 {
		t.Fatalf("the second line is not pending: %+v", pending)
	}
	if err := db.RecordDelivery(pending[0], DeliverySkipped, ""); err != nil {
		t.Fatal(err)
	}
	if left := pendingFor(t, db, "bomclaw"); len(left) != 0 {
		t.Fatalf("a filtered line stayed on the treadmill: %+v", left)
	}
}

func TestATelegramMessageFindsItsLineBack(t *testing.T) {
	db := testDB(t)
	botAgents(t, db, "bomclaw")
	bindTG(t, db, GeneralChannelID, "bomclaw", "4242", ForwardAll)

	m := post(t, db, "bomclaw", "báo cáo đây")
	pending := pendingFor(t, db, "bomclaw")
	if err := db.RecordDelivery(pending[0], DeliverySent, "8821"); err != nil {
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

func TestBindRejectsWhatNobodyCouldDeliver(t *testing.T) {
	db := testDB(t)
	botAgents(t, db, "bomclaw")
	base := ChannelGateway{ChannelID: GeneralChannelID, Kind: GatewayTelegram, AgentID: "bomclaw", Target: "4242"}

	bad := base
	bad.Mode = "sometimes"
	if _, err := db.AddChannelGateway(bad); err == nil {
		t.Error("accepted an unknown mode; it would silently forward nothing")
	}
	bad = base
	bad.Target = "0"
	if _, err := db.AddChannelGateway(bad); err == nil {
		t.Error("accepted chat id 0, which sends every line nowhere")
	}
	bad = base
	bad.Kind = "carrier pigeon"
	if _, err := db.AddChannelGateway(bad); err == nil {
		t.Error("accepted a transport nothing implements")
	}
	bad = base
	bad.AgentID = ""
	if _, err := db.AddChannelGateway(bad); err == nil {
		t.Error("accepted a gateway nobody carries; no sweep would ever see it")
	}
}

// A webhook target is a URL, and saying so at bind time beats a stack trace ten
// seconds later in a log nobody reads.
func TestAWebhookTargetMustBeAURL(t *testing.T) {
	db := testDB(t)
	botAgents(t, db, "bomclaw")
	for _, target := range []string{"4242", "hooks.slack.com/x", "ftp://x/y", ""} {
		if _, err := db.AddChannelGateway(ChannelGateway{
			ChannelID: GeneralChannelID, Kind: GatewayWebhook, AgentID: "bomclaw", Target: target,
		}); err == nil {
			t.Errorf("accepted %q as a webhook target", target)
		}
	}
	if _, err := db.AddChannelGateway(ChannelGateway{
		ChannelID: GeneralChannelID, Kind: GatewayWebhook, AgentID: "bomclaw",
		Target: "https://hooks.slack.com/services/T/B/X",
	}); err != nil {
		t.Errorf("rejected a real webhook URL: %v", err)
	}
}

// The dashboard offers a list of agents and has no other way to know which of
// them can actually carry Telegram. A row carried by a process with no bot is a
// line that sits pending forever.
func TestABindingRejectsATelegramCarrierWithNoBot(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw4") // registered, never logged a bot in
	if _, err := db.AddChannelGateway(ChannelGateway{
		ChannelID: GeneralChannelID, Kind: GatewayTelegram, AgentID: "bomclaw4", Target: "4242",
	}); err == nil {
		t.Fatal("accepted a telegram gateway carried by an agent with no bot")
	}
	// A webhook needs no bot: any process can POST.
	if _, err := db.AddChannelGateway(ChannelGateway{
		ChannelID: GeneralChannelID, Kind: GatewayWebhook, AgentID: "bomclaw4",
		Target: "https://example.test/hook",
	}); err != nil {
		t.Fatalf("a webhook should not need a Telegram bot: %v", err)
	}
}

func TestRebindingKeepsTheCutOff(t *testing.T) {
	db := testDB(t)
	botAgents(t, db, "bomclaw")
	bindTG(t, db, GeneralChannelID, "bomclaw", "4242", ForwardMentions)
	gws, err := db.ChannelGateways(GeneralChannelID)
	if err != nil || len(gws) != 1 {
		t.Fatalf("gateways: %+v %v", gws, err)
	}
	before := gws[0]

	after, err := db.AddChannelGateway(ChannelGateway{
		ChannelID: GeneralChannelID, Kind: GatewayTelegram, AgentID: "bomclaw",
		Target: "4242", Mode: ForwardAll,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !after.Since.Equal(before.Since) {
		t.Error("changing the mode reset the cut-off, which replays the backlog")
	}
	if after.ID != before.ID {
		t.Errorf("the same destination became a second row: %s then %s", before.ID, after.ID)
	}
	if after.Mode != ForwardAll {
		t.Errorf("mode = %q, want the new one", after.Mode)
	}
}

// Two rows for one destination is the owner getting every line twice — 6ac6462
// wearing a different hat. The unique index is what makes a second Add an edit.
func TestBindingTheSameDestinationTwiceIsOneRow(t *testing.T) {
	db := testDB(t)
	botAgents(t, db, "bomclaw", "bomclaw3")
	bindTG(t, db, GeneralChannelID, "bomclaw", "4242", ForwardAll)
	if _, err := db.AddChannelGateway(ChannelGateway{
		ChannelID: GeneralChannelID, Kind: GatewayTelegram, AgentID: "bomclaw3",
		Target: "4242", Mode: ForwardAll,
	}); err != nil {
		t.Fatal(err)
	}
	gws, err := db.ChannelGateways(GeneralChannelID)
	if err != nil {
		t.Fatal(err)
	}
	if len(gws) != 1 {
		t.Fatalf("one chat ended up with %d gateways; the owner would be told twice", len(gws))
	}
	if gws[0].AgentID != "bomclaw3" {
		t.Errorf("carrier = %q, want the one that bound last", gws[0].AgentID)
	}
}

func TestUnbindingStopsEverything(t *testing.T) {
	db := testDB(t)
	botAgents(t, db, "bomclaw")
	g := bindTG(t, db, GeneralChannelID, "bomclaw", "4242", ForwardAll)
	if err := db.RemoveChannelGateway(g.ID); err != nil {
		t.Fatal(err)
	}
	post(t, db, "bomclaw", "vẫn nói")
	if pending := pendingFor(t, db, "bomclaw"); len(pending) != 0 {
		t.Fatalf("an unbound room kept sending: %+v", pending)
	}
	gws, err := db.ChannelGateways(GeneralChannelID)
	if err != nil {
		t.Fatal(err)
	}
	if len(gws) != 0 {
		t.Errorf("gateway survived the unbind: %+v", gws)
	}
}

// Three agents on this machine, three Telegram bots, and message ids that
// collide across them: @Goterm_bot's message 8821 and @Goterm3_bot's 8821 are
// different messages. Both halves of the round trip have to know whose bot
// they are talking about.
func TestTwoBotsDoNotReadEachOthersMessageIds(t *testing.T) {
	db := testDB(t)
	botAgents(t, db, "bomclaw", "bomclaw3")
	// Two rooms, because one chat may only be carried by one bot.
	other, err := db.CreateChannel("", "ops", ChannelPublic, "", "system", nil)
	if err != nil {
		t.Fatal(err)
	}
	mineGW := bindTG(t, db, GeneralChannelID, "bomclaw", "4242", ForwardAll)
	theirsGW := bindTG(t, db, other.ID, "bomclaw3", "7777", ForwardAll)

	mine := post(t, db, "bomclaw", "của agent 1")
	theirs, _, err := db.PostMessage(NewChannelMessage{
		ChannelID: other.ID, AuthorID: "bomclaw3", Body: "của agent 3",
	})
	if err != nil {
		t.Fatal(err)
	}
	// The same Telegram id from two different bots.
	if err := db.RecordDelivery(Forward{MessageID: mine.ID, GatewayID: mineGW.ID, AgentID: "bomclaw"},
		DeliverySent, "8821"); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordDelivery(Forward{MessageID: theirs.ID, GatewayID: theirsGW.ID, AgentID: "bomclaw3"},
		DeliverySent, "8821"); err != nil {
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

// A gateway row is carried by exactly one process. Every gateway runs the same
// sweep, and if they all saw every pending line they would race for it — the
// loser's send having already gone out, which is two notifications for one
// line. Several gateways on one room does not change that: each row still has
// exactly one carrier.
func TestOnlyTheBoundAgentSeesTheRoom(t *testing.T) {
	db := testDB(t)
	botAgents(t, db, "bomclaw", "bomclaw3")
	bindTG(t, db, GeneralChannelID, "bomclaw", "4242", ForwardAll)

	post(t, db, "bomclaw3", "một dòng")
	if mine := pendingFor(t, db, "bomclaw"); len(mine) != 1 {
		t.Fatalf("the carrier cannot see its own room: %+v", mine)
	}
	if theirs := pendingFor(t, db, "bomclaw3"); len(theirs) != 0 {
		t.Fatalf("a second gateway would race to send the same line: %+v", theirs)
	}
}

// The point of the whole change: a room says one thing and every destination
// hears it.
func TestOneRoomReachesEveryGatewayItIsBoundTo(t *testing.T) {
	db := testDB(t)
	botAgents(t, db, "bomclaw")
	bindTG(t, db, GeneralChannelID, "bomclaw", "4242", ForwardAll)
	bindHook(t, db, GeneralChannelID, "bomclaw", "https://example.test/hook", ForwardAll)

	m := post(t, db, "bomclaw", "một câu, hai nơi")
	pending := pendingFor(t, db, "bomclaw")
	if len(pending) != 2 {
		t.Fatalf("one line produced %d deliveries, want 2: %+v", len(pending), pending)
	}
	kinds := map[string]bool{}
	for _, f := range pending {
		if f.MessageID != m.ID {
			t.Fatalf("unexpected line %s", f.MessageID)
		}
		kinds[f.Kind] = true
		if err := db.RecordDelivery(f, DeliverySent, ""); err != nil {
			t.Fatal(err)
		}
	}
	if !kinds[GatewayTelegram] || !kinds[GatewayWebhook] {
		t.Fatalf("both destinations should have been offered the line, got %v", kinds)
	}
	if left := pendingFor(t, db, "bomclaw"); len(left) != 0 {
		t.Fatalf("a settled line came back: %+v", left)
	}
}

// The exact bug the old schema had: delivery state lived on the message, so
// settling it anywhere settled it everywhere.
func TestADeliveryToOneGatewayDoesNotSettleTheOthers(t *testing.T) {
	db := testDB(t)
	botAgents(t, db, "bomclaw")
	bindTG(t, db, GeneralChannelID, "bomclaw", "4242", ForwardAll)
	hook := bindHook(t, db, GeneralChannelID, "bomclaw", "https://example.test/hook", ForwardAll)

	post(t, db, "bomclaw", "một câu")
	for _, f := range pendingFor(t, db, "bomclaw") {
		if f.GatewayID == hook.ID {
			if err := db.RecordDelivery(f, DeliverySent, ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	left := pendingFor(t, db, "bomclaw")
	if len(left) != 1 || left[0].Kind != GatewayTelegram {
		t.Fatalf("delivering to the webhook settled Telegram too: %+v", left)
	}
}

func TestASecondGatewayGetsNoBacklog(t *testing.T) {
	db := testDB(t)
	botAgents(t, db, "bomclaw")
	bindTG(t, db, GeneralChannelID, "bomclaw", "4242", ForwardAll)
	for i := 0; i < 5; i++ {
		post(t, db, "bomclaw", "dòng cũ")
	}
	// Added now, with its real cut-off.
	if _, err := db.AddChannelGateway(ChannelGateway{
		ChannelID: GeneralChannelID, Kind: GatewayWebhook, AgentID: "bomclaw",
		Target: "https://example.test/hook", Mode: ForwardAll,
	}); err != nil {
		t.Fatal(err)
	}
	for _, f := range pendingFor(t, db, "bomclaw") {
		if f.Kind == GatewayWebhook {
			t.Fatalf("a new destination was handed the room's backlog: %+v", f)
		}
	}
}

// mode=off keeps the gateway but stops the traffic, and PendingForwards filters
// it out rather than settling it — so a pause leaves lines with no delivery
// behind them. Switching back on must not empty the pause in one sweep.
func TestResumingAPausedGatewayDoesNotFloodIt(t *testing.T) {
	db := testDB(t)
	botAgents(t, db, "bomclaw")
	g := bindTG(t, db, GeneralChannelID, "bomclaw", "4242", ForwardAll)

	if _, err := db.UpdateChannelGateway(g.ID, ForwardOff, "", "", ""); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		post(t, db, "bomclaw", "nói trong lúc tạm dừng")
	}
	if pending := pendingFor(t, db, "bomclaw"); len(pending) != 0 {
		t.Fatalf("a paused gateway was still queuing: %+v", pending)
	}
	if _, err := db.UpdateChannelGateway(g.ID, ForwardMentions, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if pending := pendingFor(t, db, "bomclaw"); len(pending) != 0 {
		t.Fatalf("turning a paused gateway back on emptied the pause onto it: %+v\n"+
			"that is the flood the cut-off exists to prevent", pending)
	}
}

// The migration is the only step in this change that cannot be undone: getting
// it wrong means a week of already-delivered lines arriving on a phone at once.
func TestMigrationOfABindingKeepsWhatWasAlreadySent(t *testing.T) {
	db := testDB(t)
	botAgents(t, db, "bomclaw")

	sent := post(t, db, "bomclaw", "đã gửi tuần trước")
	declined := post(t, db, "bomclaw", "mode đã loại")
	fresh := post(t, db, "bomclaw", "chưa quyết định")

	// Write the v10 world by hand: a binding in the old table, and delivery
	// state in the old columns on the message rows.
	old := ts(time.Now().Add(-time.Hour))
	if _, err := db.conn.Exec(`INSERT INTO channel_telegram
			(channel_id, agent_id, chat_id, mode, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		GeneralChannelID, "bomclaw", 4242, ForwardAll, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.Exec(`UPDATE channel_messages
		SET forwarded_at = ?, tg_message_id = 8821, forwarded_by = 'bomclaw' WHERE id = ?`,
		ts(time.Now()), sent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.Exec(`UPDATE channel_messages
		SET forwarded_at = ?, tg_message_id = 0, forwarded_by = 'bomclaw' WHERE id = ?`,
		ts(time.Now()), declined.ID); err != nil {
		t.Fatal(err)
	}
	// The migration already ran on this fresh database, so clear the stamp.
	if _, err := db.conn.Exec(`DELETE FROM meta WHERE key = 'channel_gateways_migrated'`); err != nil {
		t.Fatal(err)
	}

	if err := db.migrateBindingsToGateways(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	gws, err := db.ChannelGateways(GeneralChannelID)
	if err != nil {
		t.Fatal(err)
	}
	if len(gws) != 1 || gws[0].Kind != GatewayTelegram || gws[0].Target != "4242" {
		t.Fatalf("the binding did not become a gateway: %+v", gws)
	}
	if gws[0].Mode != ForwardAll || gws[0].AgentID != "bomclaw" {
		t.Errorf("the gateway lost the binding's settings: %+v", gws[0])
	}
	if !gws[0].Since.Equal(parseTS(old)) {
		t.Errorf("the cut-off moved: %v, want %v", gws[0].Since, parseTS(old))
	}

	pending := pendingFor(t, db, "bomclaw")
	if len(pending) != 1 || pending[0].MessageID != fresh.ID {
		t.Fatalf("after migrating, pending is %+v — want only the undecided line.\n"+
			"anything else here is a week of history arriving on somebody's phone", pending)
	}

	// The return path still works for what was already sent.
	back, err := db.ForwardedMessage("bomclaw", 8821)
	if err != nil || back == nil || back.ID != sent.ID {
		t.Fatalf("a reply to an already-forwarded line lost its thread: %+v %v", back, err)
	}

	// And the declined line is recorded as declined, not merely absent.
	var state string
	if err := db.conn.QueryRow(`SELECT state FROM channel_deliveries WHERE message_id = ?`,
		declined.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != DeliverySkipped {
		t.Errorf("a mode-declined line migrated as %q, want %q", state, DeliverySkipped)
	}

	// Running it twice must change nothing.
	if _, err := db.conn.Exec(`DELETE FROM meta WHERE key = 'channel_gateways_migrated'`); err != nil {
		t.Fatal(err)
	}
	if err := db.migrateBindingsToGateways(); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if gws, err := db.ChannelGateways(GeneralChannelID); err != nil || len(gws) != 1 {
		t.Fatalf("a second run duplicated the gateway: %+v %v", gws, err)
	}
}
