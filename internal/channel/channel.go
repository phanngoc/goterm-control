package channel

import "context"

// Channel abstracts a messaging transport (Telegram, CLI, WebSocket, etc.).
type Channel interface {
	// ID returns the channel identifier (e.g. "telegram", "cli").
	ID() string

	// Start begins listening for inbound messages. Blocks until ctx is canceled.
	Start(ctx context.Context, handler InboundHandler) error

	// Send delivers a message to a specific chat.
	Send(ctx context.Context, chatID string, msg OutboundMessage) error

	// Stop gracefully shuts down the channel.
	Stop() error
}

// InboundHandler processes incoming messages from a channel.
type InboundHandler func(ctx context.Context, msg InboundMessage) *OutboundMessage

// InboundMessage represents a message received from a channel.
type InboundMessage struct {
	ChannelID string // which channel sent this
	ChatID    string // chat/conversation identifier
	UserID    string // who sent it
	Text      string // message text
	Command   string // parsed command (e.g. "reset", "model") without /
	Args      string // command arguments
}

// OutboundMessage represents a message to send via a channel.
type OutboundMessage struct {
	Text      string
	ImagePath string
}
