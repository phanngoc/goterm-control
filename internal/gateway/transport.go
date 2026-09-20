package gateway

import (
	"fmt"
	"strconv"

	"github.com/ngocp/goterm-control/internal/coord"
)

// The places a room can speak into.
//
// Telegram was the only one, and its shape leaked everywhere: a chat id in the
// binding, a Telegram message id on the message row, a sender interface that
// spoke int64. A transport's job is narrower than that — take a line, put it
// somewhere, and say what it became out there, if anything.

// ChannelTransport carries one line out of a room to one destination.
type ChannelTransport interface {
	// Kind matches coord's gateway kind, and is how the watcher finds the
	// transport for a row.
	Kind() string
	// Deliver returns the id the destination gave this line, or "" when the
	// destination has no ids to give. That id is the return path: on Telegram
	// a reply quotes it and finds its thread. A transport that returns "" is a
	// one-way street, and that is allowed.
	//
	// It is handed both the line and the already-rendered text. The text is
	// what a person reads; the Forward is what a machine needs — a webhook
	// posts the fields, not a sentence with asterisks in it.
	//
	// A non-nil error leaves the line unsettled on purpose: an outage should
	// delay a line, not swallow it.
	Deliver(f coord.Forward, text string) (externalID string, err error)
}

// TelegramSender delivers one line and reports which Telegram message it
// became. That id is the whole return path: a reply quoting it is how an
// answer from the phone finds the thread it belongs to.
type TelegramSender interface {
	SendChannelLine(chatID int64, text string) (int64, error)
}

// telegramTransport adapts the bot's sender to the transport seam. It is a
// shim on purpose: internal/bot knows about Telegram and nothing about rooms,
// and it should stay that way.
type telegramTransport struct{ send TelegramSender }

// NewTelegramTransport returns nil when there is no bot to send through, so
// that a gateway which does not poll simply never registers this kind.
func NewTelegramTransport(send TelegramSender) ChannelTransport {
	if send == nil {
		return nil
	}
	return telegramTransport{send: send}
}

func (telegramTransport) Kind() string { return coord.GatewayTelegram }

func (t telegramTransport) Deliver(f coord.Forward, text string) (string, error) {
	chat, err := strconv.ParseInt(f.Target, 10, 64)
	if err != nil {
		// coord rejects this at bind time; reaching it means a row was written
		// around that check, and guessing a chat id is worse than refusing.
		return "", fmt.Errorf("telegram gateway %s: %q is not a chat id", f.GatewayID, f.Target)
	}
	id, err := t.send.SendChannelLine(chat, text)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(id, 10), nil
}
