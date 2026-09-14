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
	mu   sync.Mutex
	sent []string
	next int64
	err  error
}

func (f *fakeSender) SendChannelLine(chatID int64, text string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return 0, f.err
	}
	f.sent = append(f.sent, text)
	f.next++
	return 8800 + f.next, nil
}

func forwardFixture(t *testing.T, mode string) (*coord.DB, *ForwardWatcher, *fakeSender) {
	t.Helper()
	db := testCoordDB(t)
	if err := db.RegisterAgent(coord.Agent{ID: "bomclaw2", DisplayName: "bomclaw2"}); err != nil {
		t.Fatal(err)
	}
	if err := db.BindChannelTelegram(coord.GeneralChannelID, 4242, mode); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec(
		`UPDATE channel_telegram SET created_at = '2000-01-01T00:00:00.000000000Z'`); err != nil {
		t.Fatal(err)
	}
	send := &fakeSender{}
	w := NewForwardWatcher(Deps{Coord: db}, send)
	if w == nil {
		t.Fatal("no watcher was built although coord and a sender were both there")
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
	back, err := db.ForwardedMessage(8801)
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

	pending, err := db.PendingForwards(coord.ProgressPrefix, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatal("a line lost to a Telegram outage was marked delivered; nobody would ever see it")
	}

	// It goes out once Telegram is back.
	send.err = nil
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
	pending, err := db.PendingForwards(coord.ProgressPrefix, 10)
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

func TestNoSenderMeansNoWatcher(t *testing.T) {
	db := testCoordDB(t)
	if w := NewForwardWatcher(Deps{Coord: db}, nil); w != nil {
		t.Fatal("a gateway that cannot send built a watcher anyway")
	}
	if w := NewForwardWatcher(Deps{}, &fakeSender{}); w != nil {
		t.Fatal("a gateway with no shared database built a watcher anyway")
	}
}

type sendError struct{}

func (sendError) Error() string { return "telegram: bad gateway" }

var errSendFailed = sendError{}
