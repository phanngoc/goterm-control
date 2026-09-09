package coord

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MaxScratch caps an agent's heartbeat scratchpad. It is fed into a prompt
// every heartbeat, so it must stay a note, not a log.
const MaxScratch = 64 * 1024

// Scratch is what an agent's heartbeat looks at: the things it said it would
// follow up on. Empty means the heartbeat has nothing to do and spends no
// tokens. Agents write it with `bomclaw heartbeat scratch`; people may too.
func (db *DB) Scratch(agentID string) (string, error) {
	var s string
	err := db.conn.QueryRow(`SELECT scratch FROM agents WHERE id = ?`, agentID).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("coord: no agent %q is registered — its gateway has not started yet", agentID)
	}
	if err != nil {
		return "", fmt.Errorf("scratch %s: %w", agentID, err)
	}
	return s, nil
}

// SetScratch replaces the scratchpad. An empty text clears it.
func (db *DB) SetScratch(agentID, text string) error {
	text = strings.TrimSpace(text)
	if len(text) > MaxScratch {
		return fmt.Errorf("coord: scratch is %d bytes, the cap is %d — it is a note, not a log", len(text), MaxScratch)
	}
	res, err := db.conn.Exec(`UPDATE agents SET scratch = ? WHERE id = ?`, text, agentID)
	if err != nil {
		return fmt.Errorf("set scratch %s: %w", agentID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("coord: no agent %q is registered — its gateway has not started yet", agentID)
	}
	return nil
}

// AppendScratch adds one item on its own line, stamped with the date so an
// old note is recognisable as old.
func (db *DB) AppendScratch(agentID, line string, now time.Time) error {
	line = strings.TrimSpace(line)
	if line == "" {
		return fmt.Errorf("coord: nothing to append")
	}
	current, err := db.Scratch(agentID)
	if err != nil {
		return err
	}
	entry := fmt.Sprintf("- [%s] %s", now.Format("2006-01-02"), line)
	if current != "" {
		current += "\n"
	}
	return db.SetScratch(agentID, current+entry)
}
