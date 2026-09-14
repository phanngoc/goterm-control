package coord

import (
	"fmt"
	"strings"
	"time"
)

// What the agents said to each other about one task.
//
// A task row records who asked, who took it and how it ended. It records
// nothing about the part that actually decides whether two agents converge: the
// back and forth while the work is happening — a schema agreed, a blocker
// named, a hand-off of where the file landed. That conversation exists, in DM
// rooms, and until now there was no way to read it beside the task it belongs
// to.

// TaskMail is the messages recorded against a task, oldest first.
//
// `bomclaw msg --task <id>` is what puts them there. It is the exact link, and
// exact is worth having even when it is sparse — a line an agent deliberately
// filed under this task means more than one that merely happened at the same
// time.
func (db *DB) TaskMail(taskID string) ([]Message, error) {
	if taskID == "" {
		return []Message{}, nil
	}
	rows, err := db.conn.Query(`SELECT m.id, m.author_id, m.task_id, m.body, m.channel_id, m.created_at,
			COALESCE((SELECT n.member_id FROM channel_mentions n WHERE n.message_id = m.id LIMIT 1), '')
		FROM channel_messages m WHERE m.task_id = ? ORDER BY m.created_at`, taskID)
	if err != nil {
		return nil, fmt.Errorf("mail for %s: %w", taskID, err)
	}
	return scanMessages(rows)
}

// MessagesIn returns what was said in these rooms inside a window, oldest
// first. Used for the conversation that happened alongside a task without
// being filed against it.
//
// The window matters as much as the rooms. Two agents that talk every day
// would otherwise drown one task's exchange in a month of unrelated lines, and
// a pane that shows everything shows nothing.
func (db *DB) MessagesIn(channelIDs []string, since, until time.Time, limit int) ([]Message, error) {
	if len(channelIDs) == 0 {
		return []Message{}, nil
	}
	if limit <= 0 || limit > 200 {
		limit = 60
	}
	args := make([]any, 0, len(channelIDs)+3)
	for _, id := range channelIDs {
		args = append(args, id)
	}
	q := `SELECT m.id, m.author_id, m.task_id, m.body, m.channel_id, m.created_at,
			COALESCE((SELECT n.member_id FROM channel_mentions n WHERE n.message_id = m.id LIMIT 1), '')
		FROM channel_messages m
		WHERE m.channel_id IN (` + placeholders(len(channelIDs)) + `)
		  AND m.created_at >= ?`
	args = append(args, ts(since))
	if !until.IsZero() {
		q += ` AND m.created_at <= ?`
		args = append(args, ts(until))
	}
	q += ` ORDER BY m.created_at LIMIT ?`
	args = append(args, limit)

	rows, err := db.conn.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("messages in %v: %w", channelIDs, err)
	}
	return scanMessages(rows)
}

// DMRoomsAmong is every pair-room between these agents. It is what "the
// conversation around this task" is looked for in: the task names its
// participants, and two agents coordinating have exactly one room.
func DMRoomsAmong(agents []string) []string {
	uniq := make([]string, 0, len(agents))
	seen := map[string]bool{}
	for _, a := range agents {
		if a = strings.TrimSpace(a); a != "" && !seen[a] {
			seen[a] = true
			uniq = append(uniq, a)
		}
	}
	var rooms []string
	for i := 0; i < len(uniq); i++ {
		for j := i + 1; j < len(uniq); j++ {
			rooms = append(rooms, DMChannelID(uniq[i], uniq[j]))
		}
	}
	return rooms
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func scanMessages(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}) ([]Message, error) {
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var m Message
		var created string
		if err := rows.Scan(&m.ID, &m.FromAgent, &m.TaskID, &m.Body, &m.ChannelID, &created, &m.ToAgent); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		m.CreatedAt = parseTS(created)
		out = append(out, m)
	}
	return out, rows.Err()
}
