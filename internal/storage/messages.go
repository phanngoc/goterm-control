package storage

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/ngocp/goterm-control/internal/agent"
)

// SQLiteMessageStore persists conversation messages for history reconstruction.
type SQLiteMessageStore struct {
	db *DB
}

// NewMessageStore creates a message store backed by the given database.
func NewMessageStore(db *DB) *SQLiteMessageStore {
	return &SQLiteMessageStore{db: db}
}

// Append inserts a single message into the messages table.
func (m *SQLiteMessageStore) Append(sessionID string, msg agent.Message) error {
	toolCallsJSON := ""
	if len(msg.ToolCalls) > 0 {
		if b, err := json.Marshal(msg.ToolCalls); err == nil {
			toolCallsJSON = string(b)
		}
	}

	toolResultsJSON := ""
	if len(msg.ToolResults) > 0 {
		if b, err := json.Marshal(msg.ToolResults); err == nil {
			toolResultsJSON = string(b)
		}
	}

	_, err := m.db.conn.Exec(`INSERT INTO messages
		(session_id, role, content, tool_calls, tool_results, tokens, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		sessionID, msg.Role, msg.Content,
		toolCallsJSON, toolResultsJSON, 0,
		time.Now().Format(time.RFC3339),
	)
	return err
}

// AppendBatch inserts multiple messages in a single transaction.
func (m *SQLiteMessageStore) AppendBatch(sessionID string, msgs []agent.Message) error {
	if len(msgs) == 0 {
		return nil
	}

	tx, err := m.db.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT INTO messages
		(session_id, role, content, tool_calls, tool_results, tokens, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

	now := time.Now().Format(time.RFC3339)
	for _, msg := range msgs {
		toolCallsJSON := ""
		if len(msg.ToolCalls) > 0 {
			if b, err := json.Marshal(msg.ToolCalls); err == nil {
				toolCallsJSON = string(b)
			}
		}
		toolResultsJSON := ""
		if len(msg.ToolResults) > 0 {
			if b, err := json.Marshal(msg.ToolResults); err == nil {
				toolResultsJSON = string(b)
			}
		}

		if _, err := stmt.Exec(sessionID, msg.Role, msg.Content,
			toolCallsJSON, toolResultsJSON, 0, now); err != nil {
			return fmt.Errorf("insert message: %w", err)
		}
	}
	return tx.Commit()
}

// LoadHistory loads the last `limit` messages for a session, ordered oldest first.
// If limit <= 0, loads all messages.
func (m *SQLiteMessageStore) LoadHistory(sessionID string, limit int) ([]agent.Message, error) {
	var query string
	var args []any

	if limit > 0 {
		// Subquery to get last N, then re-order ascending
		query = `SELECT role, content, tool_calls, tool_results FROM (
			SELECT role, content, tool_calls, tool_results, id
			FROM messages WHERE session_id = ? ORDER BY id DESC LIMIT ?
		) sub ORDER BY id ASC`
		args = []any{sessionID, limit}
	} else {
		query = `SELECT role, content, tool_calls, tool_results
			FROM messages WHERE session_id = ? ORDER BY id ASC`
		args = []any{sessionID}
	}

	rows, err := m.db.conn.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query messages: %w", err)
	}
	defer rows.Close()

	var msgs []agent.Message
	for rows.Next() {
		var role, content, toolCallsStr, toolResultsStr string
		if err := rows.Scan(&role, &content, &toolCallsStr, &toolResultsStr); err != nil {
			continue
		}

		msg := agent.Message{Role: role, Content: content}

		if toolCallsStr != "" {
			json.Unmarshal([]byte(toolCallsStr), &msg.ToolCalls)
		}
		if toolResultsStr != "" {
			json.Unmarshal([]byte(toolResultsStr), &msg.ToolResults)
		}

		msgs = append(msgs, msg)
	}
	return msgs, rows.Err()
}

// DeleteBySession removes all messages for a session.
func (m *SQLiteMessageStore) DeleteBySession(sessionID string) error {
	_, err := m.db.conn.Exec(`DELETE FROM messages WHERE session_id = ?`, sessionID)
	return err
}

// Count returns the number of messages for a session.
func (m *SQLiteMessageStore) Count(sessionID string) (int, error) {
	var count int
	err := m.db.conn.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_id = ?`, sessionID).Scan(&count)
	return count, err
}
