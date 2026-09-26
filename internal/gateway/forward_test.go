package gateway

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ngocp/goterm-control/internal/coord"
)

func testCoordDB(t *testing.T) *coord.DB {
	t.Helper()
	cdb, err := coord.Open(filepath.Join(t.TempDir(), "coord.db"))
	if err != nil {
		t.Fatalf("coord: %v", err)
	}
	t.Cleanup(func() { cdb.Close() })
	return cdb
}

type fakeSender struct {
	mu       sync.Mutex
	sent     []string
	attempts int
	next     int64
	err      error
}

func (f *fakeSender) SendChannelLine(chatID int64, text string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts++
	if f.err != nil {
		return 0, f.err
	}
	f.sent = append(f.sent, text)
	f.next++
	return 8800 + f.next, nil
}

// carrier registers an agent that has logged a Telegram bot in, which is what
// a telegram gateway asks of the process that carries it.
func carrier(t *testing.T, db *coord.DB, id string) {
	t.Helper()
	if err := db.RegisterAgent(coord.Agent{ID: id, DisplayName: id}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetAgentTelegramBot(id, "Goterm_"+id+"_bot"); err != nil {
		t.Fatal(err)
	}
}

// bindGateway registers a destination with its cut-off far in the past, so a
// line posted a millisecond later is after it.
func bindGateway(t *testing.T, db *coord.DB, kind, target, mode string) coord.ChannelGateway {
	t.Helper()
	g, err := db.AddChannelGateway(coord.ChannelGateway{
		ChannelID: coord.GeneralChannelID, Kind: kind, AgentID: "bomclaw2",
		Target: target, Mode: mode,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec(
		`UPDATE channel_gateways SET since = '2000-01-01T00:00:00.000000000Z' WHERE id = ?`, g.ID); err != nil {
		t.Fatal(err)
	}
	return g
}

func forwardFixture(t *testing.T, mode string) (*coord.DB, *ForwardWatcher, *fakeSender) {
	t.Helper()
	db := testCoordDB(t)
	carrier(t, db, "bomclaw2")
	bindGateway(t, db, coord.GatewayTelegram, "4242", mode)
	send := &fakeSender{}
	w := NewForwardWatcher(Deps{Coord: db, AgentID: "bomclaw2"}, NewTelegramTransport(send))
	if w == nil {
		t.Fatal("no watcher was built although coord and a transport were both there")
	}
	return db, w, send
}

func TestSweepSendsAndSettles(t *testing.T) {
	db, w, send := forwardFixture(t, coord.ForwardAll)

	m, _, err := db.PostMessage(coord.NewChannelMessage{
		ChannelID: coord.GeneralChannelID, AuthorID: "bomclaw2", Body: "BTC 64k",
	})
	if err != nil {
		t.Fatal(err)
	}

	w.sweep()
	if len(send.sent) != 1 {
		t.Fatalf("sent %d lines, want 1: %q", len(send.sent), send.sent)
	}
	if !strings.Contains(send.sent[0], "bomclaw2") || !strings.Contains(send.sent[0], "BTC 64k") {
		t.Errorf("the line says neither who wrote it nor what they said: %q", send.sent[0])
	}

	// Sweeping again must not resend it.
	w.sweep()
	if len(send.sent) != 1 {
		t.Fatalf("resent on the next sweep: %q", send.sent)
	}

	// And the Telegram id is recorded, or a reply has nothing to find.
	back, err := db.ForwardedMessage("bomclaw2", 8801)
	if err != nil || back == nil || back.ID != m.ID {
		t.Fatalf("the sent message id was not written back: %+v %v", back, err)
	}
}

func TestASendThatFailedIsNotMarkedDelivered(t *testing.T) {
	db, w, send := forwardFixture(t, coord.ForwardAll)
	send.err = errSendFailed

	if _, _, err := db.PostMessage(coord.NewChannelMessage{
		ChannelID: coord.GeneralChannelID, AuthorID: "bomclaw2", Body: "quan trọng",
	}); err != nil {
		t.Fatal(err)
	}
	w.sweep()

	pending, err := db.PendingForwards("bomclaw2", coord.ProgressPrefix, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatal("a line lost to a Telegram outage was marked delivered; nobody would ever see it")
	}

	// It goes out once Telegram is back and the cooldown has passed.
	send.err = nil
	w.backoff = 0
	w.sweep()
	if len(send.sent) != 1 {
		t.Fatalf("the delayed line never went: %q", send.sent)
	}
}

func TestMentionsModeSettlesWhatItDeclinesToSend(t *testing.T) {
	db, w, send := forwardFixture(t, coord.ForwardMentions)

	if _, _, err := db.PostMessage(coord.NewChannelMessage{
		ChannelID: coord.GeneralChannelID, AuthorID: "bomclaw2", Body: "đã build xong",
	}); err != nil {
		t.Fatal(err)
	}
	w.sweep()
	if len(send.sent) != 0 {
		t.Fatalf("mode=mentions sent the room's own chatter: %q", send.sent)
	}
	pending, err := db.PendingForwards("bomclaw2", coord.ProgressPrefix, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatal("a declined line stayed pending, so every sweep from now on re-asks a question\n" +
			"whose answer cannot change")
	}
}

func TestALongLineIsCutRatherThanRejectedByTelegram(t *testing.T) {
	long := strings.Repeat("dài ", 3000)
	out := ForwardLine(coord.Forward{ChannelName: "trading", AuthorID: "bomclaw2", Body: long})
	if n := len([]rune(out)); n > MaxForwardRunes+120 {
		t.Errorf("forwarded line is %d runes; Telegram refuses messages past its own cap", n)
	}
	if !strings.Contains(out, "dashboard") {
		t.Error("a cut line does not say where the rest is")
	}
}

func TestForwardLineNamesTheRoom(t *testing.T) {
	out := ForwardLine(coord.Forward{ChannelName: "trading", AuthorID: "bomclaw2", Body: "xong"})
	if !strings.HasPrefix(out, "*#trading") {
		t.Errorf("every bound room arrives in one Telegram chat; without the room's name\n"+
			"two projects read as one: %q", out)
	}
}

func TestNoTransportMeansNoWatcher(t *testing.T) {
	db := testCoordDB(t)
	if w := NewForwardWatcher(Deps{Coord: db, AgentID: "bomclaw"}); w != nil {
		t.Fatal("a gateway with nothing to deliver over built a watcher anyway")
	}
	if w := NewForwardWatcher(Deps{Coord: db, AgentID: "bomclaw"}, NewTelegramTransport(nil)); w != nil {
		t.Fatal("a gateway that does not poll built a Telegram watcher anyway")
	}
	if w := NewForwardWatcher(Deps{AgentID: "bomclaw"}, NewWebhookTransport()); w != nil {
		t.Fatal("a gateway with no shared database built a watcher anyway")
	}
	// A process with no bot still carries webhooks. Before v11 the watcher was
	// only built when Telegram polled, which would have left every webhook on
	// agents 2 and 3 undelivered.
	if w := NewForwardWatcher(Deps{Coord: db, AgentID: "bomclaw"}, NewWebhookTransport()); w == nil {
		t.Fatal("a gateway that does not poll Telegram cannot carry a webhook either")
	}
}

// A destination that refuses a line is left alone for a while. Without that, a
// webhook pointed at a host that is not coming back takes a POST every sweep
// for every line it is behind on — and it is behind on all of them, because a
// failed send is deliberately not settled.
func TestARefusedDestinationIsLeftAloneForAWhile(t *testing.T) {
	db, w, send := forwardFixture(t, coord.ForwardAll)
	send.err = errSendFailed

	for i := 0; i < 3; i++ {
		if _, _, err := db.PostMessage(coord.NewChannelMessage{
			ChannelID: coord.GeneralChannelID, AuthorID: "bomclaw2", Body: "một dòng",
		}); err != nil {
			t.Fatal(err)
		}
	}
	w.sweep()
	w.sweep()
	w.sweep()
	if send.attempts != 1 {
		t.Fatalf("a dead destination was tried %d times across three sweeps, want 1", send.attempts)
	}
}

// A dead destination must only starve itself. The per-sweep budget is per
// gateway for exactly this reason: a shared one would be eaten by the stuck
// lines of whichever destination is down, and the healthy one beside it would
// never get a turn.
func TestADeadGatewayDoesNotStarveAHealthyOne(t *testing.T) {
	db := testCoordDB(t)
	carrier(t, db, "bomclaw2")
	bindGateway(t, db, coord.GatewayTelegram, "4242", coord.ForwardAll)
	bindGateway(t, db, coord.GatewayWebhook, "https://dead.test/hook", coord.ForwardAll)

	send := &fakeSender{}
	w := NewForwardWatcher(Deps{Coord: db, AgentID: "bomclaw2"},
		NewTelegramTransport(send), deadTransport{})

	// More lines than one sweep's budget, so a shared budget would show.
	for i := 0; i < ForwardsPerSweep+4; i++ {
		if _, _, err := db.PostMessage(coord.NewChannelMessage{
			ChannelID: coord.GeneralChannelID, AuthorID: "bomclaw2", Body: "một dòng",
		}); err != nil {
			t.Fatal(err)
		}
	}
	w.sweep()
	if len(send.sent) != ForwardsPerSweep {
		t.Fatalf("Telegram got %d of its %d-line budget; the dead webhook ate it",
			len(send.sent), ForwardsPerSweep)
	}
	w.backoff = 0
	w.sweep()
	if len(send.sent) != ForwardsPerSweep+4 {
		t.Fatalf("Telegram is stuck at %d lines while a dead webhook keeps failing", len(send.sent))
	}
}

// coord refuses to create a row this process cannot serve, so reaching this is
// a misconfiguration — and a misconfiguration must not eat the line. Leaving it
// pending is what makes fixing the config enough to deliver it.
func TestAKindThisProcessCannotCarryIsNotSettled(t *testing.T) {
	db := testCoordDB(t)
	carrier(t, db, "bomclaw2")
	bindGateway(t, db, coord.GatewayWebhook, "https://example.test/hook", coord.ForwardAll)

	send := &fakeSender{}
	w := NewForwardWatcher(Deps{Coord: db, AgentID: "bomclaw2"}, NewTelegramTransport(send))
	if _, _, err := db.PostMessage(coord.NewChannelMessage{
		ChannelID: coord.GeneralChannelID, AuthorID: "bomclaw2", Body: "một dòng",
	}); err != nil {
		t.Fatal(err)
	}
	w.sweep()

	pending, err := db.PendingForwards("bomclaw2", coord.ProgressPrefix, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatal("a line for a transport this process does not have was silently settled;\n" +
			"fixing the configuration would no longer deliver it")
	}
}

// One room with three destinations asks the same "is the owner in this" once,
// not three times.
func TestOwnerFollowsIsAskedOncePerLine(t *testing.T) {
	db := testCoordDB(t)
	carrier(t, db, "bomclaw2")
	bindGateway(t, db, coord.GatewayTelegram, "4242", coord.ForwardMentions)
	bindGateway(t, db, coord.GatewayWebhook, "https://a.test/hook", coord.ForwardMentions)
	bindGateway(t, db, coord.GatewayWebhook, "https://b.test/hook", coord.ForwardMentions)

	send := &fakeSender{}
	w := NewForwardWatcher(Deps{Coord: db, AgentID: "bomclaw2"},
		NewTelegramTransport(send), deadTransport{})

	if _, _, err := db.PostMessage(coord.NewChannelMessage{
		ChannelID: coord.GeneralChannelID, AuthorID: "bomclaw2", Body: "chỉ là chuyện phiếm",
	}); err != nil {
		t.Fatal(err)
	}
	follows := map[string]bool{}
	pending, err := db.PendingForwards("bomclaw2", coord.ProgressPrefix, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 3 {
		t.Fatalf("three destinations produced %d pending lines", len(pending))
	}
	for _, f := range pending {
		w.deliver(f, follows)
	}
	if len(follows) != 1 {
		t.Fatalf("the same question was cached %d ways for one line", len(follows))
	}
}

// deadTransport is a webhook that never answers, without the HTTP round trip.
type deadTransport struct{}

func (deadTransport) Kind() string { return coord.GatewayWebhook }
func (deadTransport) Deliver(coord.Forward, string) (string, error) {
	return "", errSendFailed
}

type sendError struct{}

func (sendError) Error() string { return "telegram: bad gateway" }

var errSendFailed = sendError{}
