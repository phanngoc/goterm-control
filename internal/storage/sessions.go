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

// Load reads all sessions from the database, keyed by chat ID.
func (s *SQLiteSessionStore) Load() (map[int64]*session.Session, error) {
	rows, err := s.db.conn.Query(`SELECT
		id, chat_id, created_at, updated_at, claude_session_id,
		message_count, input_tokens, output_tokens, compact_summary
		FROM sessions`)
	if err != nil {
		return nil, fmt.Errorf("query sessions: %w", err)
	}
	defer rows.Close()

	sessions := make(map[int64]*session.Session)
	for rows.Next() {
		var (
			id, claudeID, summary string
			chatID                int64
			createdStr, updatedStr string
			msgCount, inTok, outTok int
		)
		if err := rows.Scan(&id, &chatID, &createdStr, &updatedStr, &claudeID,
			&msgCount, &inTok, &outTok, &summary); err != nil {
			continue
		}

		created, _ := time.Parse(time.RFC3339, createdStr)
		updated, _ := time.Parse(time.RFC3339, updatedStr)

		sess := session.NewFromDB(id, chatID, created, updated, claudeID, msgCount, inTok, outTok, summary)
		sessions[chatID] = sess
	}
	return sessions, rows.Err()
}

// Save persists all sessions to the database in a single transaction.
func (s *SQLiteSessionStore) Save(sessions map[int64]*session.Session) error {
	tx, err := s.db.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT INTO sessions
		(id, chat_id, created_at, updated_at, claude_session_id, message_count, input_tokens, output_tokens, compact_summary)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			updated_at = excluded.updated_at,
			claude_session_id = excluded.claude_session_id,
			message_count = excluded.message_count,
			input_tokens = excluded.input_tokens,
			output_tokens = excluded.output_tokens,
			compact_summary = excluded.compact_summary`)
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

	for _, sess := range sessions {
		snap := sess.Snapshot()
		_, err := stmt.Exec(
			snap.ID, snap.ChatID,
			snap.CreatedAt.Format(time.RFC3339), snap.UpdatedAt.Format(time.RFC3339),
			snap.ClaudeSessionID, snap.MessageCount,
			snap.InputTokens, snap.OutputTokens, snap.CompactSummary,
		)
		if err != nil {
			return fmt.Errorf("upsert session %s: %w", snap.ID, err)
		}
	}
	return tx.Commit()
}
