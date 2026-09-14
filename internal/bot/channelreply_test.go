package bot

import (
	"path/filepath"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/ngocp/goterm-control/internal/coord"
)

func replyTestDB(t *testing.T) *coord.DB {
	t.Helper()
	cdb, err := coord.Open(filepath.Join(t.TempDir(), "coord.db"))
	if err != nil {
		t.Fatalf("coord: %v", err)
	}
	t.Cleanup(func() { cdb.Close() })
	for _, id := range []string{"bomclaw", "bomclaw2"} {
		if err := cdb.RegisterAgent(coord.Agent{ID: id, DisplayName: id}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
	return cdb
}

func reply(to int, text string) *tgbotapi.Message {
	return &tgbotapi.Message{
		Chat:           &tgbotapi.Chat{ID: 4242},
		Text:           text,
		ReplyToMessage: &tgbotapi.Message{MessageID: to},
	}
}

// A reply on the phone has to land in the thread the quoted line belongs to,
// and wake the agent that wrote it. Anything less and the round trip is a
// one-way street.
func TestAReplyLandsInTheThreadAndWakesTheAgent(t *testing.T) {
	db := replyTestDB(t)
	line, _, err := db.PostMessage(coord.NewChannelMessage{
		ChannelID: coord.GeneralChannelID, AuthorID: "bomclaw2", Body: "BTC 64k",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MarkForwarded("bomclaw", line.ID, 8821); err != nil {
		t.Fatal(err)
	}

	var woke []string
	var room string
	h := &Handler{coord: db, agentID: "bomclaw", onChannelReply: func(channelID string, wake []string) {
		room, woke = channelID, wake
	}}

	if !h.channelReply(reply(8821, "gộp cả ETH nữa")) {
		t.Fatal("a reply to a forwarded line was treated as an ordinary message —\n" +
			"it would have gone to the model instead of back into the room")
	}
	if room != coord.GeneralChannelID {
		t.Errorf("landed in %q", room)
	}
	if len(woke) != 1 || woke[0] != "bomclaw2" {
		t.Errorf("woke %v; the agent whose line was answered has to hear about it", woke)
	}

	// A reply to a top-level line starts that line's thread.
	msgs, err := db.ThreadMessages(line.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("thread has %d messages, want the line and the reply", len(msgs))
	}
	got := msgs[1]
	if got.Body != "gộp cả ETH nữa" {
		t.Errorf("body = %q", got.Body)
	}
	if got.AuthorKind != coord.MemberUser || got.AuthorID != coord.OwnerUserID {
		t.Errorf("written as %s:%s — it has to be the owner, which is also what\n"+
			"keeps the forward watcher from sending it straight back out",
			got.AuthorKind, got.AuthorID)
	}
}

// This is the case that must stay untouched: almost everything the owner types
// is a conversation with the agent, replies included.
func TestAReplyToAnythingElseIsStillAnOrdinaryMessage(t *testing.T) {
	db := replyTestDB(t)
	h := &Handler{coord: db, agentID: "bomclaw"}

	if h.channelReply(reply(9999, "chạy lại build giúp")) {
		t.Fatal("a reply quoting a message that was never ours was swallowed into a channel")
	}
	if h.channelReply(&tgbotapi.Message{Chat: &tgbotapi.Chat{ID: 4242}, Text: "ls -la"}) {
		t.Fatal("a plain message was swallowed into a channel")
	}
}

// Coordination off is a supported configuration, and every Telegram message
// passes through this function.
func TestWithoutCoordinationNothingIsIntercepted(t *testing.T) {
	h := &Handler{}
	if h.channelReply(reply(8821, "gì cũng được")) {
		t.Fatal("intercepted a message with no shared database to put it in")
	}
}

// The reply is written into the thread, not as a new top-level line, when the
// quoted message was itself inside one.
func TestAReplyToAThreadedLineStaysInThatThread(t *testing.T) {
	db := replyTestDB(t)
	root, _, err := db.PostMessage(coord.NewChannelMessage{
		ChannelID: coord.GeneralChannelID, AuthorID: "bomclaw2", Body: "giá hôm nay",
	})
	if err != nil {
		t.Fatal(err)
	}
	inThread, _, err := db.PostMessage(coord.NewChannelMessage{
		ChannelID: coord.GeneralChannelID, ThreadRoot: root.ID,
		AuthorID: "bomclaw2", Body: "BTC 64k",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MarkForwarded("bomclaw", inThread.ID, 8822); err != nil {
		t.Fatal(err)
	}

	h := &Handler{coord: db, agentID: "bomclaw", onChannelReply: func(string, []string) {}}
	if !h.channelReply(reply(8822, "còn ETH?")) {
		t.Fatal("not routed")
	}
	msgs, err := db.ThreadMessages(root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 {
		t.Fatalf("the reply started a second thread instead of joining the one it answers: %d", len(msgs))
	}
}
