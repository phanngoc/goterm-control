package coord

import (
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Channels turn the agents' message area into a place.
//
// agent_messages addressed one agent: every line had to pick a recipient, so
// nothing existed independently of two names and a third agent could not read
// along, let alone join in. The model here is Slack's — not Slack itself, and
// deliberately only the parts that survive the move from people to agents:
//
//   - a channel is a place, addressed by subject, not by a pair of names;
//   - everything in it is readable by every member, so an idle agent can pick
//     up work it was never told about;
//   - a thread keeps one deep dive out of the channel's main line, and is the
//     unit a task binds to;
//   - a mention is what claims attention. Reading is optional, being named is
//     not.
//
// The one idea that does not survive the move: Slack is built for people who
// PULL — you open it and read, and the badge is only a hint. An agent does not
// pull, it is woken. So unread on its own means nothing here; every visibility
// level is paired with a wake level, and only a mention rings the doorbell.
// Ringing on every channel message would, with three agents in one room, be a
// loop with extra steps. coord does not ring anything itself — PostMessage
// returns who was named and the caller poke them, because coord must not
// import gateway.

// Member kinds. The human is a first-class member, not a special case bolted
// onto an agent-only table.
const (
	MemberAgent = "agent"
	MemberUser  = "user"
)

// OwnerUserID is the single human this installation has. Dashboard auth has
// been single-account since PR #63, so a real user table would be one row.
const OwnerUserID = "owner"

// Channel kinds.
const (
	ChannelPublic = "channel"
	ChannelDM     = "dm"
)

// ReplyPreviewRunes is how much of the newest reply travels with the main line.
// Enough to see that an agent answered and roughly what it said; the thread is
// one click away for the rest.
const ReplyPreviewRunes = 160

// GeneralChannelID is the room that always exists, so a fresh agent has
// somewhere to say hello without anyone creating a channel first.
const GeneralChannelID = "ch_general"

// Channel is one room.
type Channel struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Kind       string    `json:"kind"`
	Purpose    string    `json:"purpose,omitempty"`
	CreatedBy  string    `json:"created_by"`
	CreatedAt  time.Time `json:"created_at"`
	ArchivedAt time.Time `json:"archived_at,omitempty"`

	// Filled by ListChannels for the member asking.
	Members       []Member  `json:"members,omitempty"`
	Unread        int       `json:"unread"`   // messages since this member last read
	Mentions      int       `json:"mentions"` // unread mentions of this member
	LastMessageAt time.Time `json:"last_message_at,omitempty"`
}

// Member is one participant: an agent or the human.
type Member struct {
	Kind       string    `json:"kind"`
	ID         string    `json:"id"`
	JoinedAt   time.Time `json:"joined_at"`
	LastReadAt time.Time `json:"last_read_at,omitempty"`
}

// ChannelMessage is one line in a channel. A reply carries ThreadRoot; a
// top-level message has it empty and may carry Replies.
type ChannelMessage struct {
	ID         string    `json:"id"`
	ChannelID  string    `json:"channel_id"`
	ThreadRoot string    `json:"thread_root,omitempty"`
	AuthorKind string    `json:"author_kind"`
	AuthorID   string    `json:"author_id"`
	Body       string    `json:"body"`
	TaskID     string    `json:"task_id,omitempty"` // on a root message: the task this thread is about
	CreatedAt  time.Time `json:"created_at"`

	Mentions []string  `json:"mentions,omitempty"`
	Replies  int       `json:"replies,omitempty"`
	LastAt   time.Time `json:"last_reply_at,omitempty"`

	// The newest reply, in the main line, so a room shows that it was answered
	// without anyone opening anything. A count alone says an answer exists; it
	// does not say what the answer was, and "1 reply" next to a question you
	// asked an agent reads exactly like silence.
	LastReplyBy   string `json:"last_reply_by,omitempty"`
	LastReplyText string `json:"last_reply_text,omitempty"`
}

// NewChannelMessage is the input to PostMessage.
type NewChannelMessage struct {
	ChannelID  string
	ThreadRoot string // "" = top level
	AuthorKind string // defaults to MemberAgent
	AuthorID   string
	Body       string
	TaskID     string // bind a thread to a task (root messages only)

	// Notify names members explicitly, on top of any @mentions in the body.
	// A DM is addressed by construction rather than by prose, and an agent
	// that has not registered yet — a fresh install, a peer that is down —
	// still has to receive it, which parsing alone cannot guarantee.
	Notify []Member
}

// DMChannelID is derived from the pair, sorted, so both directions resolve to
// the same room without a lookup.
func DMChannelID(a, b string) string {
	pair := []string{a, b}
	sort.Strings(pair)
	return "dm_" + pair[0] + "__" + pair[1]
}

// CreateChannel makes a room, or returns the existing one with that id.
// Idempotent so two gateways racing at startup produce one channel.
func (db *DB) CreateChannel(id, name, kind, purpose, createdBy string, members []Member) (*Channel, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("coord: channel name is required")
	}
	if id == "" {
		id = "ch_" + slug(name)
	}
	if kind == "" {
		kind = ChannelPublic
	}
	now := time.Now()
	_, err := db.conn.Exec(`INSERT INTO channels (id, name, kind, purpose, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO NOTHING`,
		id, name, kind, purpose, createdBy, ts(now))
	if err != nil {
		return nil, fmt.Errorf("create channel %s: %w", id, err)
	}
	for _, m := range members {
		if err := db.JoinChannel(id, m.Kind, m.ID); err != nil {
			return nil, err
		}
	}
	return db.GetChannel(id)
}

// EnsureDM returns the direct channel between two agents, creating it on first
// use. A DM is an ordinary channel with two members and kind='dm' — the UI
// treats it differently, nothing else does.
// A DM to yourself is allowed and is not a mistake: agents already used
// `msg --to <self>` to leave their next turn a note, and the migrated history
// contains those. Refusing it would have deleted a working habit.
func (db *DB) EnsureDM(a, b string) (*Channel, error) {
	if a == "" || b == "" {
		return nil, fmt.Errorf("coord: a DM needs two members")
	}
	name, members := a+" ↔ "+b, []Member{{Kind: MemberAgent, ID: a}, {Kind: MemberAgent, ID: b}}
	if a == b {
		name, members = a+" (notes to self)", []Member{{Kind: MemberAgent, ID: a}}
	}
	return db.CreateChannel(DMChannelID(a, b), name, ChannelDM, "", a, members)
}

// JoinChannel adds a member. Idempotent: re-joining does not reset the read
// cursor, which would resurrect every message the member had caught up on.
func (db *DB) JoinChannel(channelID, kind, id string) error {
	if kind != MemberAgent && kind != MemberUser {
		return fmt.Errorf("coord: member kind must be %q or %q, got %q", MemberAgent, MemberUser, kind)
	}
	_, err := db.conn.Exec(`INSERT INTO channel_members (channel_id, member_kind, member_id, joined_at)
		VALUES (?, ?, ?, ?) ON CONFLICT DO NOTHING`, channelID, kind, id, ts(time.Now()))
	if err != nil {
		return fmt.Errorf("join %s as %s:%s: %w", channelID, kind, id, err)
	}
	return nil
}

// LeaveChannel removes a member.
func (db *DB) LeaveChannel(channelID, kind, id string) error {
	_, err := db.conn.Exec(`DELETE FROM channel_members
		WHERE channel_id = ? AND member_kind = ? AND member_id = ?`, channelID, kind, id)
	if err != nil {
		return fmt.Errorf("leave %s: %w", channelID, err)
	}
	return nil
}

// GetChannel returns one room with its members.
func (db *DB) GetChannel(id string) (*Channel, error) {
	row := db.conn.QueryRow(`SELECT id, name, kind, purpose, created_by, created_at, archived_at
		FROM channels WHERE id = ?`, id)
	c, err := scanChannel(row)
	if err != nil {
		return nil, fmt.Errorf("channel %s: %w", id, err)
	}
	c.Members, err = db.channelMembers(id)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// ListChannels returns the rooms a member is in, most recently active first.
// An empty memberID lists every channel — what the admin page shows.
func (db *DB) ListChannels(memberKind, memberID string) ([]Channel, error) {
	q := `SELECT c.id, c.name, c.kind, c.purpose, c.created_by, c.created_at, c.archived_at
		FROM channels c`
	var args []any
	if memberID != "" {
		q += ` JOIN channel_members m ON m.channel_id = c.id AND m.member_kind = ? AND m.member_id = ?`
		args = append(args, memberKind, memberID)
	}
	q += ` WHERE c.archived_at = ''`
	rows, err := db.conn.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("list channels: %w", err)
	}
	defer rows.Close()

	out := []Channel{}
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range out {
		if out[i].Members, err = db.channelMembers(out[i].ID); err != nil {
			return nil, err
		}
		if err = db.fillChannelCounters(&out[i], memberKind, memberID); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].LastMessageAt.After(out[j].LastMessageAt)
	})
	return out, nil
}

// fillChannelCounters answers the two different questions a member has about a
// room: how much has happened since I looked (Unread), and how much of it was
// addressed to me (Mentions). Only the second one is allowed to wake anybody.
func (db *DB) fillChannelCounters(c *Channel, memberKind, memberID string) error {
	var last string
	if err := db.conn.QueryRow(`SELECT COALESCE(MAX(created_at), '') FROM channel_messages
		WHERE channel_id = ?`, c.ID).Scan(&last); err != nil {
		return fmt.Errorf("last message of %s: %w", c.ID, err)
	}
	c.LastMessageAt = parseTS(last)
	if memberID == "" {
		return nil
	}

	var readAt string
	if err := db.conn.QueryRow(`SELECT last_read_at FROM channel_members
		WHERE channel_id = ? AND member_kind = ? AND member_id = ?`,
		c.ID, memberKind, memberID).Scan(&readAt); err != nil {
		return nil // not a member: no counters to fill
	}
	if err := db.conn.QueryRow(`SELECT count(*) FROM channel_messages
		WHERE channel_id = ? AND created_at > ? AND NOT (author_kind = ? AND author_id = ?)`,
		c.ID, readAt, memberKind, memberID).Scan(&c.Unread); err != nil {
		return fmt.Errorf("unread of %s: %w", c.ID, err)
	}
	if err := db.conn.QueryRow(`SELECT count(*) FROM channel_mentions n
		JOIN channel_messages m ON m.id = n.message_id
		WHERE m.channel_id = ? AND n.member_kind = ? AND n.member_id = ? AND n.read_at = ''`,
		c.ID, memberKind, memberID).Scan(&c.Mentions); err != nil {
		return fmt.Errorf("mentions of %s: %w", c.ID, err)
	}
	return nil
}

func (db *DB) channelMembers(channelID string) ([]Member, error) {
	rows, err := db.conn.Query(`SELECT member_kind, member_id, joined_at, last_read_at
		FROM channel_members WHERE channel_id = ? ORDER BY joined_at`, channelID)
	if err != nil {
		return nil, fmt.Errorf("members of %s: %w", channelID, err)
	}
	defer rows.Close()
	out := []Member{}
	for rows.Next() {
		var m Member
		var joined, read string
		if err := rows.Scan(&m.Kind, &m.ID, &joined, &read); err != nil {
			return nil, err
		}
		m.JoinedAt, m.LastReadAt = parseTS(joined), parseTS(read)
		out = append(out, m)
	}
	return out, rows.Err()
}

// mentionPattern matches @name. Agent ids are lowercase with digits, so
// "@bomclaw2" resolves whole rather than to "bomclaw" plus a stray 2.
var mentionPattern = regexp.MustCompile(`@([A-Za-z][A-Za-z0-9_-]{1,31})`)

// PostMessage writes one line and records who it named.
//
// It returns the agents that were mentioned and are not the author — the
// caller's cue to ring their doorbells. coord deliberately does not do that
// itself: the poke lives in gateway, and gateway imports coord.
//
// A mention of an agent that is not yet in the channel adds it. Pulling
// someone into a room by naming them is the whole point of a mention, and the
// alternative — silently dropping it — is the failure mode where an agent is
// asked for help and never hears about it.
func (db *DB) PostMessage(n NewChannelMessage) (*ChannelMessage, []string, error) {
	if strings.TrimSpace(n.Body) == "" {
		return nil, nil, fmt.Errorf("coord: message body is required")
	}
	if n.AuthorKind == "" {
		n.AuthorKind = MemberAgent
	}
	if _, err := db.GetChannel(n.ChannelID); err != nil {
		return nil, nil, err
	}
	if n.ThreadRoot != "" {
		root, err := db.getMessage(n.ThreadRoot)
		if err != nil {
			return nil, nil, err
		}
		if root.ThreadRoot != "" {
			// One level of threading, like Slack. A reply to a reply belongs
			// to the same thread, not a thread of its own.
			n.ThreadRoot = root.ThreadRoot
		}
		if n.TaskID != "" {
			return nil, nil, fmt.Errorf("coord: only a thread's root message carries a task binding")
		}
	}

	m := &ChannelMessage{
		ID:         "cm_" + uuid.NewString(),
		ChannelID:  n.ChannelID,
		ThreadRoot: n.ThreadRoot,
		AuthorKind: n.AuthorKind,
		AuthorID:   n.AuthorID,
		Body:       n.Body,
		TaskID:     n.TaskID,
		CreatedAt:  time.Now(),
	}
	if _, err := db.conn.Exec(`INSERT INTO channel_messages
		(id, channel_id, thread_root, author_kind, author_id, body, task_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.ChannelID, m.ThreadRoot, m.AuthorKind, m.AuthorID, m.Body, m.TaskID, ts(m.CreatedAt)); err != nil {
		return nil, nil, fmt.Errorf("post to %s: %w", n.ChannelID, err)
	}

	named, err := db.resolveMentions(n.Body)
	if err != nil {
		return nil, nil, err
	}
	// Writing your own name in a sentence is not a summons, so a parsed
	// mention of the author is dropped. An explicit Notify is not prose — it
	// is addressing, and an agent addressing itself means it, so that one is
	// kept. Either way nobody rings their own doorbell.
	addressed := named[:0:0]
	for _, who := range named {
		if who.Kind != n.AuthorKind || who.ID != n.AuthorID {
			addressed = append(addressed, who)
		}
	}
	for _, who := range n.Notify {
		if who.Kind == "" {
			who.Kind = MemberAgent
		}
		if !containsMember(addressed, who.Kind, who.ID) {
			addressed = append(addressed, who)
		}
	}

	// A person answering inside a thread is talking to the agents already in
	// it, and having to retype @bomclaw2 under its own reply is not a
	// conversation. Following a thread you have spoken in is Slack's rule and
	// the right one here.
	//
	// But only when nothing was named. Naming someone — in the prose or by
	// picking them — is a narrower statement than "whoever is in here", and
	// the narrower one has to win: a person who picks one agent and watches
	// three answer will stop picking. So this is a default for an unaddressed
	// reply, not an addition to an addressed one.
	//
	// Only a PERSON's reply does this. An agent's reply waking every other
	// agent in the thread is the loop the mention rule exists to prevent —
	// three agents in one room answering each other's answers. An agent that
	// means to summon a peer still writes @, which is a decision it made.
	if n.ThreadRoot != "" && n.AuthorKind == MemberUser && len(addressed) == 0 {
		followers, err := db.threadAgents(n.ThreadRoot)
		if err != nil {
			return nil, nil, err
		}
		for _, who := range followers {
			if !containsMember(addressed, who.Kind, who.ID) {
				addressed = append(addressed, who)
			}
		}
	}

	var wake []string
	for _, who := range addressed {
		if err := db.JoinChannel(m.ChannelID, who.Kind, who.ID); err != nil {
			return nil, nil, err
		}
		if _, err := db.conn.Exec(`INSERT INTO channel_mentions
			(message_id, member_kind, member_id, read_at, created_at)
			VALUES (?, ?, ?, '', ?) ON CONFLICT DO NOTHING`,
			m.ID, who.Kind, who.ID, ts(m.CreatedAt)); err != nil {
			return nil, nil, fmt.Errorf("record mention of %s: %w", who.ID, err)
		}
		m.Mentions = append(m.Mentions, who.ID)
		if who.Kind == MemberAgent && who.ID != n.AuthorID {
			wake = append(wake, who.ID)
		}
	}
	return m, wake, nil
}

// BindThreadToTask makes a thread the conversation about a task. The binding
// lives on the thread's root message, which is why a task can be attached to a
// conversation that started as a question — most work does, and until this the
// only way to bind was to already know the task id when the first line was
// written, which is never when a conversation becomes work.
//
// One thread, one task: a thread that discusses two pieces of work gives the
// agent finishing either one nowhere unambiguous to report back to.
func (db *DB) BindThreadToTask(rootID, taskID string) error {
	root, err := db.getMessage(rootID)
	if err != nil {
		return err
	}
	if root.ThreadRoot != "" {
		return fmt.Errorf("coord: %s is a reply, not a thread root — bind %s instead", rootID, root.ThreadRoot)
	}
	if root.TaskID != "" && root.TaskID != taskID {
		return fmt.Errorf("coord: that thread is already about task %s", root.TaskID)
	}
	if _, err := db.GetTask(taskID); err != nil {
		return err
	}
	if _, err := db.conn.Exec(`UPDATE channel_messages SET task_id = ? WHERE id = ?`, taskID, rootID); err != nil {
		return fmt.Errorf("bind %s to %s: %w", rootID, taskID, err)
	}
	return nil
}

// TaskThread finds the thread a task was opened from, or "" when it was not
// opened from a conversation. The channel comes back with it: a report has to
// know which room to go to, not just which message.
func (db *DB) TaskThread(taskID string) (rootID, channelID string, err error) {
	row := db.conn.QueryRow(`SELECT id, channel_id FROM channel_messages
		WHERE task_id = ? AND thread_root = '' ORDER BY created_at LIMIT 1`, taskID)
	switch err := row.Scan(&rootID, &channelID); {
	case err == sql.ErrNoRows:
		return "", "", nil
	case err != nil:
		return "", "", fmt.Errorf("thread of task %s: %w", taskID, err)
	}
	return rootID, channelID, nil
}

// MessageChannel is the room a message lives in. Callers that already named a
// message should not have to also name its channel — the database knows.
func (db *DB) MessageChannel(id string) (string, error) {
	m, err := db.getMessage(id)
	if err != nil {
		return "", err
	}
	return m.ChannelID, nil
}

// threadAgents lists the agents that have spoken in a thread — its root
// included, since the root is what started it.
func (db *DB) threadAgents(rootID string) ([]Member, error) {
	rows, err := db.conn.Query(`SELECT DISTINCT author_id FROM channel_messages
		WHERE (id = ? OR thread_root = ?) AND author_kind = ?`, rootID, rootID, MemberAgent)
	if err != nil {
		return nil, fmt.Errorf("agents in thread %s: %w", rootID, err)
	}
	defer rows.Close()
	var out []Member
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, Member{Kind: MemberAgent, ID: id})
	}
	return out, rows.Err()
}

// resolveMentions maps @names in a body to real members. An @name that is not
// a registered agent or the owner is left as plain text — agents write prose,
// and an email address or a Go struct tag must not summon anybody.
func (db *DB) resolveMentions(body string) ([]Member, error) {
	hits := mentionPattern.FindAllStringSubmatch(body, -1)
	if len(hits) == 0 {
		return nil, nil
	}
	agents, err := db.ListAgents()
	if err != nil {
		return nil, err
	}
	known := map[string]string{OwnerUserID: MemberUser, "user": MemberUser}
	for _, a := range agents {
		known[a.ID] = MemberAgent
	}
	seen := map[string]bool{}
	var out []Member
	for _, h := range hits {
		name := h[1]
		kind, ok := known[name]
		if !ok || seen[name] {
			continue
		}
		seen[name] = true
		if kind == MemberUser {
			name = OwnerUserID
			if seen[OwnerUserID] && name != h[1] {
				continue
			}
			seen[OwnerUserID] = true
		}
		out = append(out, Member{Kind: kind, ID: name})
	}
	return out, nil
}

// ChannelMessages returns the channel's main line — top-level messages only,
// newest first, each with its reply count so a thread announces itself.
func (db *DB) ChannelMessages(channelID string, limit int) ([]ChannelMessage, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := db.conn.Query(`SELECT id, channel_id, thread_root, author_kind, author_id, body, task_id, created_at
		FROM channel_messages WHERE channel_id = ? AND thread_root = ''
		ORDER BY created_at DESC LIMIT ?`, channelID, limit)
	if err != nil {
		return nil, fmt.Errorf("messages of %s: %w", channelID, err)
	}
	msgs, err := scanChannelMessages(rows)
	if err != nil {
		return nil, err
	}
	return db.decorate(msgs, true)
}

// ThreadMessages returns a thread: the root first, then its replies in order.
func (db *DB) ThreadMessages(rootID string) ([]ChannelMessage, error) {
	rows, err := db.conn.Query(`SELECT id, channel_id, thread_root, author_kind, author_id, body, task_id, created_at
		FROM channel_messages WHERE id = ? OR thread_root = ?
		ORDER BY created_at`, rootID, rootID)
	if err != nil {
		return nil, fmt.Errorf("thread %s: %w", rootID, err)
	}
	msgs, err := scanChannelMessages(rows)
	if err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return nil, fmt.Errorf("thread %s: no such message", rootID)
	}
	return db.decorate(msgs, false)
}

// decorate fills mentions on every message, and reply counts when the caller
// is showing a channel's main line.
func (db *DB) decorate(msgs []ChannelMessage, withReplies bool) ([]ChannelMessage, error) {
	for i := range msgs {
		rows, err := db.conn.Query(`SELECT member_id FROM channel_mentions WHERE message_id = ?`, msgs[i].ID)
		if err != nil {
			return nil, fmt.Errorf("mentions of %s: %w", msgs[i].ID, err)
		}
		for rows.Next() {
			var who string
			if err := rows.Scan(&who); err != nil {
				rows.Close()
				return nil, err
			}
			msgs[i].Mentions = append(msgs[i].Mentions, who)
		}
		rows.Close()

		if !withReplies {
			continue
		}
		var last string
		if err := db.conn.QueryRow(`SELECT count(*), COALESCE(MAX(created_at), '')
			FROM channel_messages WHERE thread_root = ?`, msgs[i].ID).Scan(&msgs[i].Replies, &last); err != nil {
			return nil, fmt.Errorf("replies of %s: %w", msgs[i].ID, err)
		}
		msgs[i].LastAt = parseTS(last)
		if msgs[i].Replies == 0 {
			continue
		}
		if err := db.conn.QueryRow(`SELECT author_id, body FROM channel_messages
			WHERE thread_root = ? ORDER BY created_at DESC LIMIT 1`, msgs[i].ID).
			Scan(&msgs[i].LastReplyBy, &msgs[i].LastReplyText); err != nil {
			return nil, fmt.Errorf("last reply of %s: %w", msgs[i].ID, err)
		}
		msgs[i].LastReplyText = truncateRunes(msgs[i].LastReplyText, ReplyPreviewRunes)
	}
	return msgs, nil
}

// MarkChannelRead moves a member's read cursor to now.
func (db *DB) MarkChannelRead(channelID, memberKind, memberID string) error {
	_, err := db.conn.Exec(`UPDATE channel_members SET last_read_at = ?
		WHERE channel_id = ? AND member_kind = ? AND member_id = ?`,
		ts(time.Now()), channelID, memberKind, memberID)
	if err != nil {
		return fmt.Errorf("mark %s read: %w", channelID, err)
	}
	return nil
}

// UnreadMentions is what an agent is obliged to look at: the lines that named
// it, newest first, across every channel.
func (db *DB) UnreadMentions(memberKind, memberID string, limit int) ([]ChannelMessage, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := db.conn.Query(`SELECT m.id, m.channel_id, m.thread_root, m.author_kind, m.author_id,
			m.body, m.task_id, m.created_at
		FROM channel_messages m JOIN channel_mentions n ON n.message_id = m.id
		WHERE n.member_kind = ? AND n.member_id = ? AND n.read_at = ''
		ORDER BY m.created_at DESC LIMIT ?`, memberKind, memberID, limit)
	if err != nil {
		return nil, fmt.Errorf("unread mentions of %s: %w", memberID, err)
	}
	return scanChannelMessages(rows)
}

// MarkMentionsRead clears the summons, for one member only: two agents named
// in the same line each answer for themselves.
func (db *DB) MarkMentionsRead(memberKind, memberID string, messageIDs []string) (int64, error) {
	if len(messageIDs) == 0 {
		return 0, nil
	}
	q := `UPDATE channel_mentions SET read_at = ?
		WHERE read_at = '' AND member_kind = ? AND member_id = ? AND message_id IN (`
	args := []any{ts(time.Now()), memberKind, memberID}
	for i, id := range messageIDs {
		if i > 0 {
			q += ","
		}
		q += "?"
		args = append(args, id)
	}
	q += ")"
	res, err := db.conn.Exec(q, args...)
	if err != nil {
		return 0, fmt.Errorf("mark mentions read: %w", err)
	}
	return res.RowsAffected()
}

// ThreadKey names the conversation a message belongs to: the thread it is in,
// or the thread it starts. Two threads in one room are two conversations, and
// an agent must not answer one while remembering the other.
//
// A top-level message keys on its own id rather than on the channel, because a
// reply to it goes into a thread rooted there — key it by the channel and the
// answer to a line and the follow-up under that same line land in two
// different conversations, which is a conversation with amnesia every other
// turn.
func ThreadKey(m ChannelMessage) string {
	if m.ThreadRoot != "" {
		return m.ThreadRoot
	}
	return m.ID
}

// ThreadSession is what an agent remembers of one conversation: the CLI
// session to resume, and how many turns it has already spent there.
type ThreadSession struct {
	ThreadKey string
	AgentID   string
	Provider  string
	SessionID string
	Turns     int
	UpdatedAt time.Time
}

// GetThreadSession returns the agent's memory of a thread. A thread nobody has
// answered yet is not an error — it is a zero-value row, which is what the
// first turn wants.
func (db *DB) GetThreadSession(threadKey, agentID string) (*ThreadSession, error) {
	row := db.conn.QueryRow(`SELECT thread_key, agent_id, provider, session_id, turns, updated_at
		FROM channel_sessions WHERE thread_key = ? AND agent_id = ?`, threadKey, agentID)
	var t ThreadSession
	var updated string
	switch err := row.Scan(&t.ThreadKey, &t.AgentID, &t.Provider, &t.SessionID, &t.Turns, &updated); {
	case err == sql.ErrNoRows:
		return &ThreadSession{ThreadKey: threadKey, AgentID: agentID}, nil
	case err != nil:
		return nil, fmt.Errorf("thread session %s/%s: %w", threadKey, agentID, err)
	}
	t.UpdatedAt = parseTS(updated)
	return &t, nil
}

// SaveThreadSession records the session the turn ran under and counts the turn.
// Turns is incremented here rather than by the caller so the cap cannot be
// skipped by a code path that forgets.
func (db *DB) SaveThreadSession(threadKey, agentID, provider, sessionID string) error {
	_, err := db.conn.Exec(`INSERT INTO channel_sessions
		(thread_key, agent_id, provider, session_id, turns, updated_at)
		VALUES (?, ?, ?, ?, 1, ?)
		ON CONFLICT(thread_key, agent_id) DO UPDATE SET
			provider   = excluded.provider,
			session_id = excluded.session_id,
			turns      = channel_sessions.turns + 1,
			updated_at = excluded.updated_at`,
		threadKey, agentID, provider, sessionID, ts(time.Now()))
	if err != nil {
		return fmt.Errorf("save thread session %s/%s: %w", threadKey, agentID, err)
	}
	return nil
}

// UpdateMessageBody rewrites a message in place.
//
// It exists for one thing: an agent working on an answer puts a line in the
// thread saying what it is doing, and that line becomes the answer when the
// answer arrives. Posting progress as separate messages would leave a room
// full of "I am reading the log" under every real reply — the progress is
// interesting while it is happening and noise the moment it is not.
//
// created_at is deliberately left alone. The message keeps its place in the
// thread: it was said when it was said, and a reply that jumped to the bottom
// on every edit would reorder a conversation as it was being read.
func (db *DB) UpdateMessageBody(id, body string) error {
	res, err := db.conn.Exec(`UPDATE channel_messages SET body = ? WHERE id = ?`, body, id)
	if err != nil {
		return fmt.Errorf("update %s: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("message %s: no such message", id)
	}
	return nil
}

// DeleteMessage removes a message and the mentions that pointed at it.
//
// Only for a progress line whose turn produced nothing: leaving "working on
// it…" in a thread forever is worse than never having said it. Real messages
// are not deleted — a room where things vanish cannot be read back.
func (db *DB) DeleteMessage(id string) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return fmt.Errorf("delete %s: %w", id, err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM channel_mentions WHERE message_id = ?`, id); err != nil {
		return fmt.Errorf("delete mentions of %s: %w", id, err)
	}
	if _, err := tx.Exec(`DELETE FROM channel_messages WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete %s: %w", id, err)
	}
	return tx.Commit()
}

// GetMessage is getMessage for callers outside this package: the gateway needs
// a thread's root to find the task, and through it what the work produced.
func (db *DB) GetMessage(id string) (*ChannelMessage, error) { return db.getMessage(id) }

func (db *DB) getMessage(id string) (*ChannelMessage, error) {
	row := db.conn.QueryRow(`SELECT id, channel_id, thread_root, author_kind, author_id, body, task_id, created_at
		FROM channel_messages WHERE id = ?`, id)
	var m ChannelMessage
	var created string
	if err := row.Scan(&m.ID, &m.ChannelID, &m.ThreadRoot, &m.AuthorKind, &m.AuthorID,
		&m.Body, &m.TaskID, &created); err != nil {
		return nil, fmt.Errorf("message %s: %w", id, err)
	}
	m.CreatedAt = parseTS(created)
	return &m, nil
}

func scanChannel(s scanner) (*Channel, error) {
	var c Channel
	var created, archived string
	if err := s.Scan(&c.ID, &c.Name, &c.Kind, &c.Purpose, &c.CreatedBy, &created, &archived); err != nil {
		return nil, err
	}
	c.CreatedAt, c.ArchivedAt = parseTS(created), parseTS(archived)
	return &c, nil
}

func scanChannelMessages(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}) ([]ChannelMessage, error) {
	defer rows.Close()
	out := []ChannelMessage{}
	for rows.Next() {
		var m ChannelMessage
		var created string
		if err := rows.Scan(&m.ID, &m.ChannelID, &m.ThreadRoot, &m.AuthorKind, &m.AuthorID,
			&m.Body, &m.TaskID, &created); err != nil {
			return nil, err
		}
		m.CreatedAt = parseTS(created)
		out = append(out, m)
	}
	return out, rows.Err()
}

var slugUnsafe = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	out := slugUnsafe.ReplaceAllString(strings.ToLower(s), "-")
	out = strings.Trim(out, "-")
	if out == "" {
		out = "channel"
	}
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}
