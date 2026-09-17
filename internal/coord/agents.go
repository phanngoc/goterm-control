package coord

import (
	"fmt"
	"strings"
	"time"
)

// StaleAfter is how long an agent may go without a heartbeat before the UI
// (and task reassignment) treats it as down.
const StaleAfter = 3 * time.Minute

// Agent is one gateway process registered in the shared database.
type Agent struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	WSAddr      string `json:"ws_addr"`
	Workspace   string `json:"workspace"`
	// TelegramBot is the @username of this agent's own bot, or "" when it has
	// none. Each agent here answers on a different bot, so "message this one
	// privately" is a different chat per agent and the screen has to say which.
	TelegramBot string    `json:"telegram_bot,omitempty"`
	StartedAt   time.Time `json:"started_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
	Online      bool      `json:"online"`
}

// RegisterAgent records an agent at startup. StartedAt is refreshed so the UI
// shows the current process, not the first one that ever ran.
func (db *DB) RegisterAgent(a Agent) error {
	now := time.Now()
	if a.StartedAt.IsZero() {
		a.StartedAt = now
	}
	_, err := db.conn.Exec(`INSERT INTO agents
		(id, display_name, provider, model, ws_addr, workspace, started_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			display_name = excluded.display_name,
			provider     = excluded.provider,
			model        = excluded.model,
			ws_addr      = excluded.ws_addr,
			workspace    = excluded.workspace,
			started_at   = excluded.started_at,
			last_seen_at = excluded.last_seen_at`,
		a.ID, a.DisplayName, a.Provider, a.Model, a.WSAddr, a.Workspace,
		ts(a.StartedAt), ts(now))
	if err != nil {
		return fmt.Errorf("register agent %s: %w", a.ID, err)
	}
	// Every agent lands in #general on startup. An agent that has to be
	// invited to the only room before it can be spoken to is an agent nobody
	// remembers to invite; joining is idempotent and does not touch the read
	// cursor, so a restart costs nothing.
	if err := db.JoinChannel(GeneralChannelID, MemberAgent, a.ID); err != nil {
		return fmt.Errorf("register agent %s: %w", a.ID, err)
	}
	return nil
}

// Heartbeat marks an agent alive. Cheap enough to call on a short timer.
func (db *DB) Heartbeat(agentID string) error {
	_, err := db.conn.Exec(`UPDATE agents SET last_seen_at = ? WHERE id = ?`,
		ts(time.Now()), agentID)
	return err
}

// SetAgentTelegramBot records which bot this agent answers on.
//
// Separate from RegisterAgent because the username is not known when the agent
// registers: it comes back from Telegram when the bot logs in, which happens
// after. RegisterAgent does not touch this column, so a restart keeps it even
// in the window before the bot is up.
func (db *DB) SetAgentTelegramBot(agentID, username string) error {
	_, err := db.conn.Exec(`UPDATE agents SET telegram_bot = ? WHERE id = ?`,
		strings.TrimPrefix(strings.TrimSpace(username), "@"), agentID)
	if err != nil {
		return fmt.Errorf("set telegram bot of %s: %w", agentID, err)
	}
	return nil
}

// ListAgents returns every known agent with liveness derived from the heartbeat.
func (db *DB) ListAgents() ([]Agent, error) {
	rows, err := db.conn.Query(`SELECT id, display_name, provider, model, ws_addr,
		workspace, telegram_bot, started_at, last_seen_at FROM agents ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	defer rows.Close()

	cutoff := time.Now().Add(-StaleAfter)
	out := []Agent{}
	for rows.Next() {
		var a Agent
		var started, seen string
		if err := rows.Scan(&a.ID, &a.DisplayName, &a.Provider, &a.Model,
			&a.WSAddr, &a.Workspace, &a.TelegramBot, &started, &seen); err != nil {
			return nil, fmt.Errorf("scan agent: %w", err)
		}
		a.StartedAt = parseTS(started)
		a.LastSeenAt = parseTS(seen)
		a.Online = a.LastSeenAt.After(cutoff)
		out = append(out, a)
	}
	return out, rows.Err()
}
