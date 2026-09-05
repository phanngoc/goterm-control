package coord

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Message is one note from one agent to another. Unlike a task it carries no
// state machine — it is the "FYI" channel, and the audit trail of what the
// agents actually said to each other.
type Message struct {
	ID        string    `json:"id"`
	FromAgent string    `json:"from_agent"`
	ToAgent   string    `json:"to_agent"`
	TaskID    string    `json:"task_id,omitempty"`
	Body      string    `json:"body"`
	ReadAt    time.Time `json:"read_at,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// SendMessage stores a message for another agent.
func (db *DB) SendMessage(from, to, taskID, body string) (*Message, error) {
	m := &Message{
		ID:        "m_" + uuid.NewString(),
		FromAgent: from,
		ToAgent:   to,
		TaskID:    taskID,
		Body:      body,
		CreatedAt: time.Now(),
	}
	_, err := db.conn.Exec(`INSERT INTO agent_messages
		(id, from_agent, to_agent, task_id, body, read_at, created_at)
		VALUES (?, ?, ?, ?, ?, '', ?)`,
		m.ID, m.FromAgent, m.ToAgent, m.TaskID, m.Body, ts(m.CreatedAt))
	if err != nil {
		return nil, fmt.Errorf("send message: %w", err)
	}
	return m, nil
}

// Inbox returns messages addressed to an agent, newest first.
// unreadOnly restricts it to messages not yet marked read.
func (db *DB) Inbox(agentID string, unreadOnly bool, limit int) ([]Message, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT id, from_agent, to_agent, task_id, body, read_at, created_at
		FROM agent_messages WHERE to_agent = ?`
	if unreadOnly {
		q += ` AND read_at = ''`
	}
	q += ` ORDER BY created_at DESC LIMIT ?`
	return db.queryMessages(q, agentID, limit)
}

// RecentMessages returns the whole cross-agent conversation, newest first —
// what the admin page shows as the "agents talking to each other" stream.
func (db *DB) RecentMessages(limit int) ([]Message, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	return db.queryMessages(`SELECT id, from_agent, to_agent, task_id, body, read_at, created_at
		FROM agent_messages ORDER BY created_at DESC LIMIT ?`, limit)
}

// MarkRead stamps messages as read. Returns how many rows changed.
func (db *DB) MarkRead(ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	q := `UPDATE agent_messages SET read_at = ? WHERE read_at = '' AND id IN (`
	args := []any{ts(time.Now())}
	for i, id := range ids {
		if i > 0 {
			q += ","
		}
		q += "?"
		args = append(args, id)
	}
	q += ")"
	res, err := db.conn.Exec(q, args...)
	if err != nil {
		return 0, fmt.Errorf("mark read: %w", err)
	}
	return res.RowsAffected()
}

func (db *DB) queryMessages(q string, args ...any) ([]Message, error) {
	rows, err := db.conn.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("query messages: %w", err)
	}
	defer rows.Close()

	out := []Message{}
	for rows.Next() {
		var m Message
		var read, created string
		if err := rows.Scan(&m.ID, &m.FromAgent, &m.ToAgent, &m.TaskID, &m.Body, &read, &created); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		m.ReadAt = parseTS(read)
		m.CreatedAt = parseTS(created)
		out = append(out, m)
	}
	return out, rows.Err()
}
