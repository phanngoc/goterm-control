package coord

import (
	"database/sql"
	"fmt"
	"time"
)

// Carrying a channel to Telegram and back.
//
// A room lived only in the dashboard: to know what three agents had just said,
// someone had to open a browser. A binding gives one channel a Telegram chat to
// speak into, and the reply that comes back lands in the thread it answers.
//
// This file holds the state — who is bound, what has already been sent, and
// which Telegram message a line became. The sending itself is gateway's, which
// is where the bot lives.

// Forward modes. A room that sends everything is a room whose notifications get
// muted within a day, and a muted channel loses the mentions too — so the
// default is the narrow one.
const (
	// ForwardAll sends every line an agent writes in the room.
	ForwardAll = "all"
	// ForwardMentions sends only what the owner is a party to: a line that
	// names them, or any reply in a thread they have spoken in. Following a
	// thread you have spoken in is Slack's rule, and it is what makes the
	// round trip work — an answer to a question asked from Telegram comes
	// back without anyone having to type "@owner" into it.
	ForwardMentions = "mentions"
	// ForwardOff keeps the binding but stops the traffic.
	ForwardOff = "off"
)

// ForwardModes is every accepted mode, for error messages and the CLI.
var ForwardModes = []string{ForwardAll, ForwardMentions, ForwardOff}

// TelegramBinding ties one channel to one Telegram chat, through one agent's
// bot.
//
// AgentID is not bookkeeping. Every agent on this machine runs its own bot, so
// "which chat" does not identify a conversation on its own — three bots can all
// reach the same person, and a message id only means something to the bot that
// sent it. The binding names the bot, and everything downstream is scoped by it.
type TelegramBinding struct {
	ChannelID string    `json:"channel_id"`
	AgentID   string    `json:"agent_id"`
	ChatID    int64     `json:"chat_id"`
	Mode      string    `json:"mode"`
	CreatedAt time.Time `json:"created_at"`
}

// Forward is a line waiting to leave the room.
type Forward struct {
	MessageID   string
	ChannelID   string
	ChannelName string
	ThreadRoot  string
	AuthorID    string
	Body        string
	ChatID      int64
	Mode        string
	CreatedAt   time.Time
}

// BindModeValid reports whether mode is one this package understands.
func BindModeValid(mode string) bool {
	switch mode {
	case ForwardAll, ForwardMentions, ForwardOff:
		return true
	}
	return false
}

// BindChannelTelegram points a channel at a Telegram chat. Re-binding changes
// the mode and the chat but keeps created_at, which is the cut-off for what
// gets forwarded: resetting it on a mode change would replay the room's
// backlog onto somebody's phone.
func (db *DB) BindChannelTelegram(channelID, agentID string, chatID int64, mode string) error {
	if mode == "" {
		mode = ForwardMentions
	}
	if agentID == "" {
		return fmt.Errorf("coord: a binding must name the agent whose bot carries it")
	}
	if !BindModeValid(mode) {
		return fmt.Errorf("coord: forward mode must be all, mentions or off (got %q)", mode)
	}
	if chatID == 0 {
		return fmt.Errorf("coord: a Telegram chat id is required")
	}
	if _, err := db.GetChannel(channelID); err != nil {
		return err
	}
	now := ts(time.Now())
	_, err := db.conn.Exec(`INSERT INTO channel_telegram (channel_id, agent_id, chat_id, mode, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(channel_id) DO UPDATE SET agent_id = excluded.agent_id, chat_id = excluded.chat_id,
			mode = excluded.mode, updated_at = excluded.updated_at`,
		channelID, agentID, chatID, mode, now, now)
	if err != nil {
		return fmt.Errorf("bind %s to telegram: %w", channelID, err)
	}
	return nil
}

// UnbindChannelTelegram drops the binding entirely. Distinct from mode=off:
// off is a pause that remembers the chat and the cut-off, this forgets both.
func (db *DB) UnbindChannelTelegram(channelID string) error {
	if _, err := db.conn.Exec(`DELETE FROM channel_telegram WHERE channel_id = ?`, channelID); err != nil {
		return fmt.Errorf("unbind %s: %w", channelID, err)
	}
	return nil
}

// ChannelBinding is one channel's binding, or nil when it has none.
func (db *DB) ChannelBinding(channelID string) (*TelegramBinding, error) {
	row := db.conn.QueryRow(`SELECT channel_id, agent_id, chat_id, mode, created_at
		FROM channel_telegram WHERE channel_id = ?`, channelID)
	var b TelegramBinding
	var created string
	switch err := row.Scan(&b.ChannelID, &b.AgentID, &b.ChatID, &b.Mode, &created); {
	case err == sql.ErrNoRows:
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("binding of %s: %w", channelID, err)
	}
	b.CreatedAt = parseTS(created)
	return &b, nil
}

// ChannelBindings lists every binding, for `bomclaw ch bind` with no arguments.
func (db *DB) ChannelBindings() ([]TelegramBinding, error) {
	rows, err := db.conn.Query(`SELECT channel_id, agent_id, chat_id, mode, created_at
		FROM channel_telegram ORDER BY channel_id`)
	if err != nil {
		return nil, fmt.Errorf("bindings: %w", err)
	}
	defer rows.Close()
	out := []TelegramBinding{}
	for rows.Next() {
		var b TelegramBinding
		var created string
		if err := rows.Scan(&b.ChannelID, &b.AgentID, &b.ChatID, &b.Mode, &created); err != nil {
			return nil, err
		}
		b.CreatedAt = parseTS(created)
		out = append(out, b)
	}
	return out, rows.Err()
}

// PendingForwards returns the lines in bound channels that have settled and
// have not been sent yet, oldest first.
//
// Three conditions carry the whole design:
//
// Only an agent's lines. A person's own words are never pushed back to that
// person, which makes an echo loop structurally impossible rather than
// something a flag has to remember: a reply arriving from Telegram is written
// as the owner, so it can never be picked up here and sent out again.
//
// Not while it is still a progress line. The mention watcher posts "⏳ …"
// BEFORE running the turn and then replaces that same row with the answer —
// same id, no new row. Forwarding on "a row appeared" would send the owner the
// word "thinking" and never the reply. So a ⏳ line is left alone and collected
// on a later sweep, once it has become what it is going to be.
//
// Nothing older than the binding. Binding a room that has been busy all week
// should not empty that week onto a phone.
//
// And only this agent's bindings. Three gateways run this loop; if they all saw
// every pending line they would race for it, and the loser's send would already
// have gone out — two notifications for one line, from two different bots.
// Scoping by agent removes the race instead of guarding it.
func (db *DB) PendingForwards(agentID, progressPrefix string, limit int) ([]Forward, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	rows, err := db.conn.Query(`SELECT m.id, m.channel_id, c.name, m.thread_root, m.author_id, m.body,
			m.created_at, b.chat_id, b.mode
		FROM channel_messages m
		JOIN channel_telegram b ON b.channel_id = m.channel_id
		JOIN channels c         ON c.id = m.channel_id
		WHERE m.forwarded_at = ''
		  AND b.agent_id = ?
		  AND b.mode <> ?
		  AND m.author_kind = ?
		  AND m.body NOT LIKE ? || '%'
		  AND m.created_at > b.created_at
		ORDER BY m.created_at
		LIMIT ?`, agentID, ForwardOff, MemberAgent, progressPrefix, limit)
	if err != nil {
		return nil, fmt.Errorf("pending forwards: %w", err)
	}
	defer rows.Close()
	out := []Forward{}
	for rows.Next() {
		var f Forward
		var created string
		if err := rows.Scan(&f.MessageID, &f.ChannelID, &f.ChannelName, &f.ThreadRoot,
			&f.AuthorID, &f.Body, &created, &f.ChatID, &f.Mode); err != nil {
			return nil, err
		}
		f.CreatedAt = parseTS(created)
		out = append(out, f)
	}
	return out, rows.Err()
}

// OwnerFollows reports whether the owner is a party to this message: named in
// it, or present in the thread it belongs to. It is what ForwardMentions asks
// before sending.
//
// A top-level message with no thread and no mention is not followed — that is
// the room talking to itself, which is exactly the traffic mode=mentions
// exists to leave behind.
func (db *DB) OwnerFollows(messageID, threadRoot string) (bool, error) {
	var n int
	if err := db.conn.QueryRow(`SELECT count(*) FROM channel_mentions
		WHERE message_id = ? AND member_kind = ? AND member_id = ?`,
		messageID, MemberUser, OwnerUserID).Scan(&n); err != nil {
		return false, fmt.Errorf("mentions of %s: %w", messageID, err)
	}
	if n > 0 {
		return true, nil
	}
	if threadRoot == "" {
		return false, nil
	}
	if err := db.conn.QueryRow(`SELECT count(*) FROM channel_messages
		WHERE (id = ? OR thread_root = ?) AND author_kind = ? AND author_id = ?`,
		threadRoot, threadRoot, MemberUser, OwnerUserID).Scan(&n); err != nil {
		return false, fmt.Errorf("owner in thread %s: %w", threadRoot, err)
	}
	return n > 0, nil
}

// MarkForwarded settles a message's forwarding, once and for all.
//
// tgMessageID is the Telegram message it became, or 0 when the decision was
// not to send it. Both are recorded the same way on purpose: a line that mode
// filtered out must not be reconsidered on every later sweep, because the
// answer would never change and the work would repeat forever.
func (db *DB) MarkForwarded(agentID, messageID string, tgMessageID int64) error {
	res, err := db.conn.Exec(`UPDATE channel_messages
		SET forwarded_at = ?, tg_message_id = ?, forwarded_by = ?
		WHERE id = ? AND forwarded_at = ''`, ts(time.Now()), tgMessageID, agentID, messageID)
	if err != nil {
		return fmt.Errorf("mark %s forwarded: %w", messageID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Already settled, or gone. Either way there is nothing to send and
		// nothing to fix — a deleted progress line takes this path.
		return nil
	}
	return nil
}

// ForwardedMessage finds the channel message a Telegram message came from, or
// nil when that Telegram message was not one of ours. Nil is the ordinary
// answer, not an error: most of what the owner types is not a reply to a
// forwarded line, and that has to keep meaning "talk to the model".
//
// Scoped to one agent because a Telegram message id belongs to the bot that
// sent it. Three bots serve this machine, and their ids collide freely — an
// unscoped lookup would match another bot's line and answer into a thread the
// person was not even looking at.
func (db *DB) ForwardedMessage(agentID string, tgMessageID int64) (*ChannelMessage, error) {
	if tgMessageID == 0 || agentID == "" {
		return nil, nil
	}
	var id string
	row := db.conn.QueryRow(`SELECT id FROM channel_messages
		WHERE forwarded_by = ? AND tg_message_id = ?`, agentID, tgMessageID)
	switch err := row.Scan(&id); {
	case err == sql.ErrNoRows:
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("telegram message %d: %w", tgMessageID, err)
	}
	return db.getMessage(id)
}
