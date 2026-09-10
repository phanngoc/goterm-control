package coord

import (
	"database/sql"
	"fmt"
	"time"
)

// Message is one agent naming another. It predates channels and is kept
// because the agents' own instructions, `bomclaw msg`, `bomclaw inbox` and the
// messages.* RPC all speak it — changing the world out from under a running
// agent mid-turn is not worth the tidiness.
//
// Underneath it is now a channel: SendMessage posts into the pair's DM room
// and mentions the recipient, and an inbox is that agent's unread mentions
// wherever they were written. So a DM and a line in #general reach an agent by
// exactly one path, and the third agent added in this change can read along.
type Message struct {
	ID        string    `json:"id"`
	FromAgent string    `json:"from_agent"`
	ToAgent   string    `json:"to_agent"`
	TaskID    string    `json:"task_id,omitempty"`
	Body      string    `json:"body"`
	ChannelID string    `json:"channel_id,omitempty"`
	ReadAt    time.Time `json:"read_at,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// SendMessage posts to the DM channel between the two agents and names the
// recipient, so it lands in their mentions like any other summons.
func (db *DB) SendMessage(from, to, taskID, body string) (*Message, error) {
	ch, err := db.EnsureDM(from, to)
	if err != nil {
		return nil, err
	}
	// Addressed explicitly rather than by writing an @ into someone's prose:
	// a DM is addressed by construction, and the recipient may not even be
	// registered yet.
	m, _, err := db.PostMessage(NewChannelMessage{
		ChannelID: ch.ID, AuthorKind: MemberAgent, AuthorID: from, Body: body, TaskID: taskID,
		Notify: []Member{{Kind: MemberAgent, ID: to}},
	})
	if err != nil {
		return nil, err
	}
	return &Message{
		ID: m.ID, FromAgent: from, ToAgent: to, TaskID: taskID, Body: m.Body,
		ChannelID: m.ChannelID, CreatedAt: m.CreatedAt,
	}, nil
}

// Inbox returns the messages that named an agent, newest first.
func (db *DB) Inbox(agentID string, unreadOnly bool, limit int) ([]Message, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT m.id, m.author_id, m.task_id, m.body, m.channel_id, n.read_at, m.created_at
		FROM channel_messages m JOIN channel_mentions n ON n.message_id = m.id
		WHERE n.member_kind = ? AND n.member_id = ?`
	if unreadOnly {
		q += ` AND n.read_at = ''`
	}
	q += ` ORDER BY m.created_at DESC LIMIT ?`

	rows, err := db.conn.Query(q, MemberAgent, agentID, limit)
	if err != nil {
		return nil, fmt.Errorf("inbox of %s: %w", agentID, err)
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var m Message
		var read, created string
		if err := rows.Scan(&m.ID, &m.FromAgent, &m.TaskID, &m.Body, &m.ChannelID, &read, &created); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		m.ToAgent = agentID
		m.ReadAt, m.CreatedAt = parseTS(read), parseTS(created)
		out = append(out, m)
	}
	return out, rows.Err()
}

// RecentMessages returns the whole cross-agent stream, newest first — every
// channel, not just DMs, since a channel line is the same kind of event now.
func (db *DB) RecentMessages(limit int) ([]Message, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := db.conn.Query(`SELECT m.id, m.author_id, m.task_id, m.body, m.channel_id, m.created_at,
			COALESCE((SELECT n.member_id FROM channel_mentions n WHERE n.message_id = m.id LIMIT 1), '')
		FROM channel_messages m ORDER BY m.created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("recent messages: %w", err)
	}
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

// MarkRead clears an agent's summons. It takes the agent because a line may
// name several: one of them reading it must not clear it for the others.
func (db *DB) MarkRead(agentID string, ids []string) (int64, error) {
	return db.MarkMentionsRead(MemberAgent, agentID, ids)
}

// UnreadCount is how many messages still name this agent.
func (db *DB) UnreadCount(agentID string) (int, error) {
	var n int
	err := db.conn.QueryRow(`SELECT count(*) FROM channel_mentions
		WHERE member_kind = ? AND member_id = ? AND read_at = ''`, MemberAgent, agentID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("unread count for %s: %w", agentID, err)
	}
	return n, nil
}

func containsMember(ms []Member, kind, id string) bool {
	for _, m := range ms {
		if m.Kind == kind && m.ID == id {
			return true
		}
	}
	return false
}

// migrateMessagesToChannels moves the old agent_messages rows into channels,
// once, on whichever gateway opens the database first after the upgrade.
//
// It runs in one transaction guarded by a meta key. Open() takes every
// transaction as BEGIN IMMEDIATE, so the second gateway either waits and then
// sees the flag, or fails to get the lock and retries — it cannot half-migrate
// alongside the first. The old table is left in place: it is small, and if
// something here is wrong the data is still where it was.
func (db *DB) migrateMessagesToChannels() error {
	var done string
	err := db.conn.QueryRow(`SELECT value FROM meta WHERE key = 'channels_migrated'`).Scan(&done)
	if err == nil && done != "" {
		return nil
	}
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("check channel migration: %w", err)
	}

	tx, err := db.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin channel migration: %w", err)
	}
	defer tx.Rollback()

	// Re-check inside the write lock: another gateway may have just finished.
	if err := tx.QueryRow(`SELECT value FROM meta WHERE key = 'channels_migrated'`).Scan(&done); err == nil && done != "" {
		return nil
	} else if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("re-check channel migration: %w", err)
	}

	now := ts(time.Now())
	if _, err := tx.Exec(`INSERT INTO channels (id, name, kind, purpose, created_by, created_at)
		VALUES (?, 'general', ?, 'Everything that does not need its own room', 'system', ?)
		ON CONFLICT(id) DO NOTHING`, GeneralChannelID, ChannelPublic, now); err != nil {
		return fmt.Errorf("create #general: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO channel_members (channel_id, member_kind, member_id, joined_at)
		VALUES (?, ?, ?, ?) ON CONFLICT DO NOTHING`,
		GeneralChannelID, MemberUser, OwnerUserID, now); err != nil {
		return fmt.Errorf("add owner to #general: %w", err)
	}
	// `WHERE true` is load-bearing: in INSERT..SELECT, SQLite cannot tell an
	// ON CONFLICT clause from part of the SELECT without a WHERE to close it.
	if _, err := tx.Exec(`INSERT INTO channel_members (channel_id, member_kind, member_id, joined_at)
		SELECT ?, ?, id, ? FROM agents WHERE true ON CONFLICT DO NOTHING`,
		GeneralChannelID, MemberAgent, now); err != nil {
		return fmt.Errorf("add agents to #general: %w", err)
	}

	rows, err := tx.Query(`SELECT id, from_agent, to_agent, task_id, body, read_at, created_at
		FROM agent_messages ORDER BY created_at`)
	if err != nil {
		return fmt.Errorf("read agent_messages: %w", err)
	}
	type oldMsg struct{ id, from, to, task, body, read, created string }
	var olds []oldMsg
	for rows.Next() {
		var o oldMsg
		if err := rows.Scan(&o.id, &o.from, &o.to, &o.task, &o.body, &o.read, &o.created); err != nil {
			rows.Close()
			return fmt.Errorf("scan agent_message: %w", err)
		}
		olds = append(olds, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, o := range olds {
		chID := DMChannelID(o.from, o.to)
		if _, err := tx.Exec(`INSERT INTO channels (id, name, kind, purpose, created_by, created_at)
			VALUES (?, ?, ?, '', ?, ?) ON CONFLICT(id) DO NOTHING`,
			chID, o.from+" ↔ "+o.to, ChannelDM, o.from, o.created); err != nil {
			return fmt.Errorf("create dm %s: %w", chID, err)
		}
		for _, who := range []string{o.from, o.to} {
			if _, err := tx.Exec(`INSERT INTO channel_members (channel_id, member_kind, member_id, joined_at)
				VALUES (?, ?, ?, ?) ON CONFLICT DO NOTHING`, chID, MemberAgent, who, o.created); err != nil {
				return fmt.Errorf("add %s to %s: %w", who, chID, err)
			}
		}
		// The old id is kept so anything already pointing at a message — a log
		// line, a note, an agent's own memory — still resolves.
		if _, err := tx.Exec(`INSERT INTO channel_messages
			(id, channel_id, thread_root, author_kind, author_id, body, task_id, created_at)
			VALUES (?, ?, '', ?, ?, ?, ?, ?) ON CONFLICT(id) DO NOTHING`,
			o.id, chID, MemberAgent, o.from, o.body, o.task, o.created); err != nil {
			return fmt.Errorf("move message %s: %w", o.id, err)
		}
		if _, err := tx.Exec(`INSERT INTO channel_mentions
			(message_id, member_kind, member_id, read_at, created_at)
			VALUES (?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
			o.id, MemberAgent, o.to, o.read, o.created); err != nil {
			return fmt.Errorf("move mention %s: %w", o.id, err)
		}
	}

	if _, err := tx.Exec(`INSERT INTO meta (key, value) VALUES ('channels_migrated', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, fmt.Sprint(len(olds))); err != nil {
		return fmt.Errorf("stamp channel migration: %w", err)
	}
	return tx.Commit()
}
