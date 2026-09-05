package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"
)

// Channel names for channel_bindings.
const (
	ChannelTelegram = "telegram"
	ChannelWeb      = "web"
)

// WebAccountID is the external id the single dashboard account binds under.
// The dashboard has one account (PR #63), so one id is enough; a multi-user
// dashboard would bind each login separately.
const WebAccountID = "1"

// DashboardConversationID is the conversation a web login gets when there is
// no Telegram conversation to join. It matches the old hardcoded dashboard chat
// id, so a fresh install without Telegram behaves exactly as before.
const DashboardConversationID int64 = 1

// ConversationStore resolves a channel (Telegram chat, web login) to the
// conversation it belongs to.
//
// A conversation is identified by the same integer key the session manager has
// always used for a chat — sessions.chat_id. Nothing about sessions changes;
// the bindings table is what lets two channels share one key. This is the whole
// mechanism behind "the dashboard shows the Telegram conversation": both
// channels resolve to the same key, so they get the same active session, the
// same history and the same execution lane.
type ConversationStore struct {
	db *DB
}

// NewConversationStore creates a store backed by the given database.
func NewConversationStore(db *DB) *ConversationStore {
	return &ConversationStore{db: db}
}

// Resolve returns the conversation bound to (channel, externalID).
// ok is false when nothing is bound yet.
func (s *ConversationStore) Resolve(channel, externalID string) (int64, bool) {
	var id int64
	err := s.db.conn.QueryRow(`SELECT conversation_id FROM channel_bindings
		WHERE channel = ? AND external_id = ?`, channel, externalID).Scan(&id)
	if err != nil {
		return 0, false
	}
	return id, true
}

// Bind points (channel, externalID) at a conversation, creating the
// conversation row if needed. Rebinding an existing pair replaces it.
func (s *ConversationStore) Bind(channel, externalID string, conversationID int64) error {
	tx, err := s.db.conn.Begin()
	if err != nil {
		return fmt.Errorf("bind: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.Exec(`INSERT INTO conversations (id, title, created_at, updated_at)
		VALUES (?, '', ?, ?) ON CONFLICT(id) DO NOTHING`, conversationID, now, now); err != nil {
		return fmt.Errorf("bind: ensure conversation %d: %w", conversationID, err)
	}
	if _, err := tx.Exec(`INSERT INTO channel_bindings (channel, external_id, conversation_id, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(channel, external_id) DO UPDATE SET conversation_id = excluded.conversation_id`,
		channel, externalID, conversationID, now); err != nil {
		return fmt.Errorf("bind %s:%s: %w", channel, externalID, err)
	}
	return tx.Commit()
}

// ResolveWeb returns the conversation the dashboard should use, binding one on
// first use.
//
// The default follows the single-user rule: when exactly one Telegram
// conversation exists, the dashboard joins it — the whole point is that typing
// on the web and typing on Telegram are the same conversation. With none, the
// dashboard gets its own (DashboardConversationID). With several, it also gets
// its own, and the log says a person has to choose; guessing which of several
// people's chats the dashboard should join is not the code's call to make.
func (s *ConversationStore) ResolveWeb() int64 {
	if id, ok := s.Resolve(ChannelWeb, WebAccountID); ok {
		return id
	}
	target := DashboardConversationID
	telegram, err := s.conversationsFor(ChannelTelegram)
	switch {
	case err != nil:
		log.Printf("conversations: listing telegram bindings: %v — dashboard uses its own conversation", err)
	case len(telegram) == 1:
		target = telegram[0]
		log.Printf("conversations: dashboard joins the Telegram conversation %d", target)
	case len(telegram) > 1:
		log.Printf("conversations: %d Telegram conversations exist — dashboard keeps its own (%d); bind it explicitly to join one",
			len(telegram), target)
	}
	if err := s.Bind(ChannelWeb, WebAccountID, target); err != nil {
		log.Printf("conversations: %v", err)
	}
	return target
}

// EnsureTelegramBindings makes sure every Telegram chat the manager knows has a
// binding row. Telegram chats map to themselves (conversation id = chat id), so
// this is bookkeeping — it keeps the bindings table a complete picture of who
// is talking to the agent, which ResolveWeb's single-user rule relies on.
func (s *ConversationStore) EnsureTelegramBindings(chatIDs []int64) {
	for _, id := range chatIDs {
		if id == DashboardConversationID {
			continue
		}
		if _, ok := s.Resolve(ChannelTelegram, fmt.Sprint(id)); ok {
			continue
		}
		if err := s.Bind(ChannelTelegram, fmt.Sprint(id), id); err != nil {
			log.Printf("conversations: %v", err)
		}
	}
}

// conversationsFor lists the distinct conversations bound through a channel.
func (s *ConversationStore) conversationsFor(channel string) ([]int64, error) {
	rows, err := s.db.conn.Query(`SELECT DISTINCT conversation_id FROM channel_bindings
		WHERE channel = ? ORDER BY conversation_id`, channel)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// mergeChats moves every session of `from` under `to` and drops `from`'s chat
// state, so the manager sees one chat with the union of both histories. The
// active session stays whatever `to` had — a merge must not yank a live
// Telegram conversation onto the dashboard's last session.
func mergeChats(tx *sql.Tx, from, to int64) error {
	if _, err := tx.Exec(`UPDATE sessions SET chat_id = ? WHERE chat_id = ?`, to, from); err != nil {
		return fmt.Errorf("move sessions %d→%d: %w", from, to, err)
	}
	if _, err := tx.Exec(`DELETE FROM chat_state WHERE chat_id = ?`, from); err != nil {
		return fmt.Errorf("drop chat_state %d: %w", from, err)
	}
	if _, err := tx.Exec(`DELETE FROM conversations WHERE id = ?`, from); err != nil {
		return fmt.Errorf("drop conversation %d: %w", from, err)
	}
	return nil
}

// errNoChats marks a database with no conversations to migrate.
var errNoChats = errors.New("no chats")
