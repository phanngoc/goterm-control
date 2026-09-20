package coord

import (
	"database/sql"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// Carrying a room out to the places it speaks into, and the reply back.
//
// A room lived only in the dashboard: to know what three agents had just said,
// someone had to open a browser. A gateway gives one channel somewhere outside
// to speak into, and a reply that comes back lands in the thread it answers.
//
// Until v11 that somewhere was a single Telegram chat, by construction:
// channel_telegram.channel_id was its primary key, and whether a line had been
// sent was three columns on the message row. One room, one destination, one
// delivery, forever. Now a room has as many gateways as it is given, and a
// delivery is a row per (line, destination).
//
// This file holds the state — who is bound, what has already gone out, and what
// each line became out there. The sending itself is gateway's, which is where
// the bot and the HTTP client live.

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
	// ForwardOff keeps the gateway but stops the traffic.
	ForwardOff = "off"
)

// ForwardModes is every accepted mode, for error messages and the CLI.
var ForwardModes = []string{ForwardAll, ForwardMentions, ForwardOff}

// Gateway kinds — the transports a room can speak into.
const (
	// GatewayTelegram is one agent's bot, speaking into one chat. The only
	// kind with a return path: a reply quotes a message id and finds its
	// thread.
	GatewayTelegram = "telegram"
	// GatewayWebhook is an HTTP POST of the line as JSON. One-way by nature,
	// and the body is shaped so a Slack or Discord incoming-webhook URL works
	// with no adapter at all.
	GatewayWebhook = "webhook"
)

// GatewayKinds is every accepted kind, for error messages and the CLI.
var GatewayKinds = []string{GatewayTelegram, GatewayWebhook}

// What became of one line at one destination.
const (
	// DeliverySent means it went out; ExternalID is what it became there.
	DeliverySent = "sent"
	// DeliverySkipped means the mode declined it. Recorded rather than left
	// alone because the answer can never change, and re-asking it on every
	// sweep is how a poll loop becomes a treadmill.
	DeliverySkipped = "skipped"
)

// ChannelGateway is one place a channel speaks into.
//
// AgentID is not bookkeeping. Every gateway process runs the same sweep, and a
// row is only ever seen by the process named here — that is what divides the
// work, and it is the whole reason several gateways on one room cannot send the
// same line twice. Removing it would bring back 6ac6462: three watchers seeing
// one pending line, two of them losing the write after their send had already
// left.
//
// Since is the cut-off and is not the same thing as CreatedAt. Binding a room
// that has been busy all week must not empty that week onto a phone, and
// switching a paused gateway back on must not empty the pause.
type ChannelGateway struct {
	ID        string `json:"id"`
	ChannelID string `json:"channel_id"`
	Kind      string `json:"kind"`
	AgentID   string `json:"agent_id"`
	Target    string `json:"target"`
	// Secret never crosses the RPC boundary; HasSecret carries the fact that
	// there is one, which is all a screen needs to show.
	Secret    string    `json:"-"`
	HasSecret bool      `json:"has_secret"`
	Mode      string    `json:"mode"`
	Label     string    `json:"label"`
	Since     time.Time `json:"since"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Forward is a line waiting to leave the room, for one destination.
//
// There is no ChatID: Kind and Target are the address now, and a Telegram chat
// id is just what Target holds when Kind is telegram.
type Forward struct {
	MessageID   string
	ChannelID   string
	ChannelName string
	ThreadRoot  string
	AuthorID    string
	Body        string
	CreatedAt   time.Time

	GatewayID string
	Kind      string
	Target    string
	Secret    string
	Mode      string
	AgentID   string
}

// BindModeValid reports whether mode is one this package understands.
func BindModeValid(mode string) bool {
	switch mode {
	case ForwardAll, ForwardMentions, ForwardOff:
		return true
	}
	return false
}

// GatewayKindValid reports whether kind is a transport this package knows.
func GatewayKindValid(kind string) bool {
	switch kind {
	case GatewayTelegram, GatewayWebhook:
		return true
	}
	return false
}

// DefaultMode is the mode a kind gets when nobody chose one.
//
// They differ because the far ends differ. A phone that buzzes all day gets
// muted, and a muted chat loses the mentions too — so Telegram starts narrow. A
// webhook wakes nobody, and a machine-readable feed that drops most of what it
// is fed is close to useless — so it starts wide.
func DefaultMode(kind string) string {
	if kind == GatewayWebhook {
		return ForwardAll
	}
	return ForwardMentions
}

// AddChannelGateway registers a place this channel speaks into.
//
// Re-registering the same destination changes the mode, the label, the carrier
// and the secret but keeps Since, which is the cut-off: resetting it would
// replay the room's backlog onto somebody's phone.
func (db *DB) AddChannelGateway(g ChannelGateway) (ChannelGateway, error) {
	if g.Mode == "" {
		g.Mode = DefaultMode(g.Kind)
	}
	if !GatewayKindValid(g.Kind) {
		return ChannelGateway{}, fmt.Errorf("coord: gateway kind must be telegram or webhook (got %q)", g.Kind)
	}
	if !BindModeValid(g.Mode) {
		return ChannelGateway{}, fmt.Errorf("coord: forward mode must be all, mentions or off (got %q)", g.Mode)
	}
	if g.AgentID == "" {
		return ChannelGateway{}, fmt.Errorf("coord: a gateway must name the agent whose process carries it")
	}
	if err := db.validateTarget(g.Kind, g.Target, g.AgentID); err != nil {
		return ChannelGateway{}, err
	}
	if _, err := db.GetChannel(g.ChannelID); err != nil {
		return ChannelGateway{}, err
	}

	now := time.Now()
	nowTS := ts(now)
	id := "cg_" + uuid.NewString()
	_, err := db.conn.Exec(`INSERT INTO channel_gateways
			(id, channel_id, kind, agent_id, target, secret, mode, label, since, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(channel_id, kind, target) DO UPDATE SET
			agent_id   = excluded.agent_id,
			secret     = excluded.secret,
			mode       = excluded.mode,
			label      = excluded.label,
			updated_at = excluded.updated_at`,
		id, g.ChannelID, g.Kind, g.AgentID, g.Target, g.Secret, g.Mode, g.Label, nowTS, nowTS, nowTS)
	if err != nil {
		return ChannelGateway{}, fmt.Errorf("bind %s to %s: %w", g.ChannelID, g.Kind, err)
	}
	// Read it back rather than returning what was passed in: on the conflict
	// path the row that exists is older than this call, and its id and Since
	// are the ones everything downstream keys on.
	row, err := db.gatewayByDest(g.ChannelID, g.Kind, g.Target)
	if err != nil {
		return ChannelGateway{}, err
	}
	return *row, nil
}

// UpdateChannelGateway changes an existing gateway by id.
//
// target and secret are only applied when non-empty — a screen that edits the
// mode must not have to resend a secret it was never shown.
func (db *DB) UpdateChannelGateway(id, mode, label, target, secret string) (ChannelGateway, error) {
	old, err := db.ChannelGateway(id)
	if err != nil {
		return ChannelGateway{}, err
	}
	if old == nil {
		return ChannelGateway{}, fmt.Errorf("coord: no gateway %s", id)
	}
	if mode == "" {
		mode = old.Mode
	}
	if !BindModeValid(mode) {
		return ChannelGateway{}, fmt.Errorf("coord: forward mode must be all, mentions or off (got %q)", mode)
	}
	if target == "" {
		target = old.Target
	}
	if err := db.validateTarget(old.Kind, target, old.AgentID); err != nil {
		return ChannelGateway{}, err
	}
	if secret == "" {
		secret = old.Secret
	}

	now := time.Now()
	since := ts(old.Since)
	// Switching a paused gateway back on moves its cut-off forward. A gateway
	// that was off for a day has a day of lines with no delivery row behind
	// them — PendingForwards filters mode='off' rather than settling them —
	// so without this, turning it back on empties that day onto the
	// destination in one sweep. That is the same flood Since exists to prevent
	// on a fresh bind.
	if old.Mode == ForwardOff && mode != ForwardOff {
		since = ts(now)
	}
	_, err = db.conn.Exec(`UPDATE channel_gateways
		SET mode = ?, label = ?, target = ?, secret = ?, since = ?, updated_at = ?
		WHERE id = ?`, mode, label, target, secret, since, ts(now), id)
	if err != nil {
		return ChannelGateway{}, fmt.Errorf("update gateway %s: %w", id, err)
	}
	row, err := db.ChannelGateway(id)
	if err != nil {
		return ChannelGateway{}, err
	}
	return *row, nil
}

// RemoveChannelGateway drops a gateway entirely. Distinct from mode=off: off is
// a pause that remembers the destination and the cut-off, this forgets both.
//
// The deliveries stay. They are the record of what this room actually said to
// the outside, and a Telegram reply to a line sent before the unbind still
// resolves — the return path asks the delivery, not the gateway.
func (db *DB) RemoveChannelGateway(id string) error {
	if _, err := db.conn.Exec(`DELETE FROM channel_gateways WHERE id = ?`, id); err != nil {
		return fmt.Errorf("unbind %s: %w", id, err)
	}
	return nil
}

// ChannelGateway is one gateway by id, or nil when there is none.
func (db *DB) ChannelGateway(id string) (*ChannelGateway, error) {
	return db.scanGateway(db.conn.QueryRow(gatewaySelect+` WHERE id = ?`, id))
}

// ChannelGateways lists a channel's gateways, or every gateway when channelID
// is empty — which is what `bomclaw ch bind` with no arguments prints, and what
// the dashboard loads once for the whole sidebar.
func (db *DB) ChannelGateways(channelID string) ([]ChannelGateway, error) {
	q := gatewaySelect + ` ORDER BY channel_id, kind, target`
	args := []any{}
	if channelID != "" {
		q = gatewaySelect + ` WHERE channel_id = ? ORDER BY kind, target`
		args = append(args, channelID)
	}
	rows, err := db.conn.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("gateways: %w", err)
	}
	defer rows.Close()
	out := []ChannelGateway{}
	for rows.Next() {
		g, err := scanGatewayRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *g)
	}
	return out, rows.Err()
}

// PendingForwards returns the lines that have settled and have not yet been
// delivered to the gateways this agent carries, oldest first.
//
// Five conditions carry the whole design:
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
// Nothing older than the gateway's cut-off. Binding a room that has been busy
// all week should not empty that week onto a phone.
//
// No delivery row yet. This is the anti-join that replaced a column on the
// message. The old shape could only say "this line has been forwarded", full
// stop; the question now is "has this line been forwarded TO THIS DESTINATION",
// and that is a question a column on the message cannot answer.
//
// And only this agent's gateways. Three gateway processes run this loop; if
// they all saw every pending line they would race for it, and the loser's send
// would already have gone out — two notifications for one line, from two
// different bots. Scoping by carrier removes the race instead of guarding it.
//
// limit is per gateway, not per sweep. A line whose send failed is deliberately
// left unsettled, so it comes back at the head of the next sweep; with one
// binding that is just a retry, but with a shared budget a dead webhook's stuck
// lines would eat the whole allowance and a healthy Telegram gateway on the
// same room would never get a turn. Per gateway, a dead destination only
// starves itself.
func (db *DB) PendingForwards(agentID, progressPrefix string, limit int) ([]Forward, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	gateways, err := db.carriedGateways(agentID)
	if err != nil {
		return nil, err
	}
	out := []Forward{}
	for _, g := range gateways {
		lines, err := db.pendingFor(g, progressPrefix, limit)
		if err != nil {
			return nil, err
		}
		out = append(out, lines...)
	}
	return out, nil
}

func (db *DB) pendingFor(g ChannelGateway, progressPrefix string, limit int) ([]Forward, error) {
	rows, err := db.conn.Query(`SELECT m.id, m.thread_root, m.author_id, m.body, m.created_at, c.name
		FROM channel_messages m
		JOIN channels c ON c.id = m.channel_id
		LEFT JOIN channel_deliveries d ON d.message_id = m.id AND d.gateway_id = ?
		WHERE m.channel_id = ?
		  AND d.message_id IS NULL
		  AND m.author_kind = ?
		  AND m.body NOT LIKE ? || '%'
		  AND m.created_at > ?
		ORDER BY m.created_at
		LIMIT ?`, g.ID, g.ChannelID, MemberAgent, progressPrefix, ts(g.Since), limit)
	if err != nil {
		return nil, fmt.Errorf("pending forwards for %s: %w", g.ID, err)
	}
	defer rows.Close()
	out := []Forward{}
	for rows.Next() {
		f := Forward{
			ChannelID: g.ChannelID,
			GatewayID: g.ID,
			Kind:      g.Kind,
			Target:    g.Target,
			Secret:    g.Secret,
			Mode:      g.Mode,
			AgentID:   g.AgentID,
		}
		var created string
		if err := rows.Scan(&f.MessageID, &f.ThreadRoot, &f.AuthorID, &f.Body, &created, &f.ChannelName); err != nil {
			return nil, err
		}
		f.CreatedAt = parseTS(created)
		out = append(out, f)
	}
	return out, rows.Err()
}

// carriedGateways is the gateways this process is responsible for and that are
// not paused.
func (db *DB) carriedGateways(agentID string) ([]ChannelGateway, error) {
	rows, err := db.conn.Query(gatewaySelect+` WHERE agent_id = ? AND mode <> ? ORDER BY channel_id, kind`,
		agentID, ForwardOff)
	if err != nil {
		return nil, fmt.Errorf("gateways carried by %s: %w", agentID, err)
	}
	defer rows.Close()
	out := []ChannelGateway{}
	for rows.Next() {
		g, err := scanGatewayRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *g)
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

// RecordDelivery settles one line at one destination, once and for all.
//
// externalID is what it became out there, or empty when the decision was not to
// send it and when the transport has no ids at all. Both outcomes are recorded
// the same way on purpose: a line the mode filtered out must not be
// reconsidered on every later sweep, because the answer would never change and
// the work would repeat forever.
//
// Inserting is how the delivery is claimed, and a second attempt is a no-op.
// That is an idempotency latch, not a lock: it cannot recall a message that has
// already left, which is why the carrier scope in PendingForwards is what
// actually keeps two processes from sending the same line.
func (db *DB) RecordDelivery(f Forward, state, externalID string) error {
	switch state {
	case DeliverySent, DeliverySkipped:
	default:
		return fmt.Errorf("coord: delivery state must be sent or skipped (got %q)", state)
	}
	_, err := db.conn.Exec(`INSERT INTO channel_deliveries
			(message_id, gateway_id, state, external_id, agent_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(message_id, gateway_id) DO NOTHING`,
		f.MessageID, f.GatewayID, state, externalID, f.AgentID, ts(time.Now()))
	if err != nil {
		return fmt.Errorf("record delivery of %s to %s: %w", f.MessageID, f.GatewayID, err)
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
	row := db.conn.QueryRow(`SELECT d.message_id
		FROM channel_deliveries d
		JOIN channel_gateways g ON g.id = d.gateway_id
		WHERE d.agent_id = ? AND d.external_id = ? AND d.state = ? AND g.kind = ?
		LIMIT 1`, agentID, strconv.FormatInt(tgMessageID, 10), DeliverySent, GatewayTelegram)
	switch err := row.Scan(&id); {
	case err == sql.ErrNoRows:
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("telegram message %d: %w", tgMessageID, err)
	}
	return db.getMessage(id)
}

// --- plumbing ---------------------------------------------------------------

const gatewaySelect = `SELECT id, channel_id, kind, agent_id, target, secret, mode, label,
	since, created_at, updated_at FROM channel_gateways`

func (db *DB) scanGateway(row scanner) (*ChannelGateway, error) {
	g, err := scanGatewayRow(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return g, err
}

func scanGatewayRow(row scanner) (*ChannelGateway, error) {
	var g ChannelGateway
	var since, created, updated string
	if err := row.Scan(&g.ID, &g.ChannelID, &g.Kind, &g.AgentID, &g.Target, &g.Secret,
		&g.Mode, &g.Label, &since, &created, &updated); err != nil {
		if err == sql.ErrNoRows {
			return nil, err
		}
		return nil, fmt.Errorf("scan gateway: %w", err)
	}
	g.HasSecret = g.Secret != ""
	g.Since = parseTS(since)
	g.CreatedAt = parseTS(created)
	g.UpdatedAt = parseTS(updated)
	return &g, nil
}

func (db *DB) gatewayByDest(channelID, kind, target string) (*ChannelGateway, error) {
	g, err := db.scanGateway(db.conn.QueryRow(
		gatewaySelect+` WHERE channel_id = ? AND kind = ? AND target = ?`, channelID, kind, target))
	if err != nil {
		return nil, err
	}
	if g == nil {
		return nil, fmt.Errorf("coord: gateway for %s vanished after being written", channelID)
	}
	return g, nil
}

// validateTarget rejects an address the transport could never use, at bind time
// rather than ten seconds later in a log nobody reads.
func (db *DB) validateTarget(kind, target, agentID string) error {
	if target == "" {
		return fmt.Errorf("coord: a %s gateway needs a target", kind)
	}
	switch kind {
	case GatewayTelegram:
		n, err := strconv.ParseInt(target, 10, 64)
		if err != nil || n == 0 {
			return fmt.Errorf("coord: a telegram target is a chat id (got %q)", target)
		}
		// A room carried by a process with no bot is a row nobody can deliver.
		// The dashboard offers a list of agents and has no other way to know
		// which of them can actually carry Telegram, so the check belongs here
		// where both it and the CLI pass through.
		ok, err := db.agentHasBot(agentID)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("coord: %s has no Telegram bot, so it cannot carry a telegram gateway", agentID)
		}
	case GatewayWebhook:
		u, err := url.Parse(target)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("coord: a webhook target is an http(s) URL (got %q)", target)
		}
	}
	return nil
}

func (db *DB) agentHasBot(agentID string) (bool, error) {
	var bot string
	err := db.conn.QueryRow(`SELECT telegram_bot FROM agents WHERE id = ?`, agentID).Scan(&bot)
	switch {
	case err == sql.ErrNoRows:
		return false, fmt.Errorf("coord: no agent %s", agentID)
	case err != nil:
		return false, fmt.Errorf("telegram bot of %s: %w", agentID, err)
	}
	return bot != "", nil
}

// migrateBindingsToGateways turns the one-per-channel Telegram binding into a
// gateway row, and every already-decided message into a delivery row.
//
// The second half is the part that matters. Without it the first sweep after
// the upgrade would look at a week of already-forwarded lines, find no delivery
// rows behind them, and send the lot to somebody's phone.
//
// The old table and the old columns are left alone. If something here is wrong
// the data is still where it was.
func (db *DB) migrateBindingsToGateways() error {
	const key = "channel_gateways_migrated"
	var done string
	err := db.conn.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&done)
	if err == nil && done != "" {
		return nil
	}
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("check gateway migration: %w", err)
	}

	tx, err := db.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin gateway migration: %w", err)
	}
	defer tx.Rollback()

	// Re-check inside the write lock: another gateway may have just finished.
	if err := tx.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&done); err == nil && done != "" {
		return nil
	} else if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("re-check gateway migration: %w", err)
	}

	// The id is derived rather than random so this is a no-op on a re-run and
	// so the second statement can find the row it just wrote.
	//
	// `WHERE true` is load-bearing: in INSERT..SELECT, SQLite cannot tell an
	// ON CONFLICT clause from part of the SELECT without a WHERE to close it.
	if _, err := tx.Exec(`INSERT INTO channel_gateways
			(id, channel_id, kind, agent_id, target, secret, mode, label, since, created_at, updated_at)
		SELECT 'cg_tg_' || channel_id, channel_id, ?, agent_id, CAST(chat_id AS TEXT), '',
		       mode, '', created_at, created_at, updated_at
		FROM channel_telegram WHERE true
		ON CONFLICT DO NOTHING`, GatewayTelegram); err != nil {
		return fmt.Errorf("migrate telegram bindings: %w", err)
	}

	// Both outcomes travel. tg_message_id = 0 meant "the mode declined this
	// line", and it has to stay declined, or every sweep re-asks a question
	// whose answer cannot change. forwarded_by falls back to the gateway's
	// carrier for rows written before that column existed.
	if _, err := tx.Exec(`INSERT INTO channel_deliveries
			(message_id, gateway_id, state, external_id, agent_id, created_at)
		SELECT m.id, g.id,
		       CASE WHEN m.tg_message_id = 0 THEN ? ELSE ? END,
		       CASE WHEN m.tg_message_id = 0 THEN '' ELSE CAST(m.tg_message_id AS TEXT) END,
		       CASE WHEN m.forwarded_by = '' THEN g.agent_id ELSE m.forwarded_by END,
		       m.forwarded_at
		FROM channel_messages m
		JOIN channel_gateways g ON g.channel_id = m.channel_id AND g.kind = ?
		WHERE m.forwarded_at <> ''
		ON CONFLICT DO NOTHING`,
		DeliverySkipped, DeliverySent, GatewayTelegram); err != nil {
		return fmt.Errorf("migrate forwarded messages: %w", err)
	}

	if _, err := tx.Exec(`INSERT INTO meta (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, ts(time.Now())); err != nil {
		return fmt.Errorf("stamp gateway migration: %w", err)
	}
	return tx.Commit()
}
