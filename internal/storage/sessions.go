package storage

import (
	"fmt"
	"time"

	"github.com/ngocp/goterm-control/internal/session"
)

// SQLiteSessionStore implements session.SessionPersister backed by SQLite.
type SQLiteSessionStore struct {
	db *DB
}

// NewSessionStore creates a session store backed by the given database.
func NewSessionStore(db *DB) *SQLiteSessionStore {
	return &SQLiteSessionStore{db: db}
}

// Load reads all sessions and chat_state from the database.
func (s *SQLiteSessionStore) Load() (map[int64]*session.ChatState, error) {
	// Load all sessions grouped by chat_id.
	rows, err := s.db.conn.Query(`SELECT
		id, chat_id, created_at, updated_at, claude_session_id,
		message_count, input_tokens, output_tokens, compact_summary,
		label, seq
		FROM sessions`)
	if err != nil {
		return nil, fmt.Errorf("query sessions: %w", err)
	}
	defer rows.Close()

	chats := make(map[int64]*session.ChatState)
	for rows.Next() {
		var (
			id, claudeID, summary, label string
			chatID                       int64
			createdStr, updatedStr       string
			msgCount, inTok, outTok, seq int
		)
		if err := rows.Scan(&id, &chatID, &createdStr, &updatedStr, &claudeID,
			&msgCount, &inTok, &outTok, &summary, &label, &seq); err != nil {
			continue
		}

		created, _ := time.Parse(time.RFC3339, createdStr)
		updated, _ := time.Parse(time.RFC3339, updatedStr)

		sess := session.NewFromDB(id, chatID, created, updated, claudeID, msgCount, inTok, outTok, summary, label, seq)

		cs, ok := chats[chatID]
		if !ok {
			cs = &session.ChatState{
				NextSeq:  1,
				Sessions: make(map[string]*session.Session),
			}
			chats[chatID] = cs
		}
		cs.Sessions[id] = sess
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Load chat_state to get active session IDs and next_seq.
	csRows, err := s.db.conn.Query(`SELECT chat_id, active_session_id, next_seq FROM chat_state`)
	if err != nil {
		// Table might not exist yet (first load before migration). Use defaults.
		for chatID, cs := range chats {
			if cs.ActiveSessionID == "" {
				// Pick the most recently updated session as active.
				cs.ActiveSessionID = pickMostRecent(cs.Sessions)
			}
			_ = chatID
		}
		return chats, nil
	}
	defer csRows.Close()

	for csRows.Next() {
		var chatID int64
		var activeID string
		var nextSeq int
		if err := csRows.Scan(&chatID, &activeID, &nextSeq); err != nil {
			continue
		}
		if cs, ok := chats[chatID]; ok {
			cs.ActiveSessionID = activeID
			cs.NextSeq = nextSeq
		}
	}

	// For any chat without chat_state row, pick the most recent session.
	for _, cs := range chats {
		if cs.ActiveSessionID == "" {
			cs.ActiveSessionID = pickMostRecent(cs.Sessions)
		}
	}

	return chats, nil
}

// Save persists all sessions and chat_state to the database.
func (s *SQLiteSessionStore) Save(chats map[int64]*session.ChatState) error {
	tx, err := s.db.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	sessStmt, err := tx.Prepare(`INSERT INTO sessions
		(id, chat_id, created_at, updated_at, claude_session_id,
		 message_count, input_tokens, output_tokens, compact_summary, label, seq)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			updated_at = excluded.updated_at,
			claude_session_id = excluded.claude_session_id,
			message_count = excluded.message_count,
			input_tokens = excluded.input_tokens,
			output_tokens = excluded.output_tokens,
			compact_summary = excluded.compact_summary,
			label = excluded.label`)
	if err != nil {
		return fmt.Errorf("prepare sessions: %w", err)
	}
	defer sessStmt.Close()

	csStmt, err := tx.Prepare(`INSERT INTO chat_state
		(chat_id, active_session_id, next_seq)
		VALUES (?, ?, ?)
		ON CONFLICT(chat_id) DO UPDATE SET
			active_session_id = excluded.active_session_id,
			next_seq = excluded.next_seq`)
	if err != nil {
		return fmt.Errorf("prepare chat_state: %w", err)
	}
	defer csStmt.Close()

	for chatID, cs := range chats {
		for _, sess := range cs.Sessions {
			snap := sess.Snapshot()
			_, err := sessStmt.Exec(
				snap.ID, snap.ChatID,
				snap.CreatedAt.Format(time.RFC3339), snap.UpdatedAt.Format(time.RFC3339),
				snap.ClaudeSessionID, snap.MessageCount,
				snap.InputTokens, snap.OutputTokens, snap.CompactSummary,
				snap.Label, snap.Seq,
			)
			if err != nil {
				return fmt.Errorf("upsert session %s: %w", snap.ID, err)
			}
		}

		if _, err := csStmt.Exec(chatID, cs.ActiveSessionID, cs.NextSeq); err != nil {
			return fmt.Errorf("upsert chat_state for chat %d: %w", chatID, err)
		}
	}
	return tx.Commit()
}

// pickMostRecent returns the ID of the most recently updated session.
func pickMostRecent(sessions map[string]*session.Session) string {
	var bestID string
	var bestTime time.Time
	for id, s := range sessions {
		if s.UpdatedAt.After(bestTime) {
			bestTime = s.UpdatedAt
			bestID = id
		}
	}
	return bestID
}
