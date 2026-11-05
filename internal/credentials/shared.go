package credentials

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// SharedFile is the state every agent on the machine reads and writes about
// the credentials they have in common, beside the token store.
const SharedFile = "credential-state.db"

// SharedStatePath is where it lives, given the coordination database path.
func SharedStatePath(coordDB string) string {
	return filepath.Join(filepath.Dir(coordDB), SharedFile)
}

// loadWindow is how far back "used least" looks: a claude cap is per five
// hours, so turns older than that no longer weigh on a token.
const loadWindow = 5 * time.Hour

// Shared is what one agent knows about a credential that the others must
// know too: that it is capped and until when, and how hard it has been used.
//
// Without it each agent found a cap by hitting it. On 2026-11-05 three agents
// burned one token in under an hour, each cooling it down on its own and each
// starting the next rotation on the same token again.
//
// What is NOT shared is the rotation cursor. Each switch costs every
// conversation on that agent an uncached turn; one cursor for three agents
// would switch three times as often and spend more of the quota it rations.
// Instead each agent keeps its own run of rotate_every turns, and when it
// moves on it moves to the token used least across all of them.
type Shared struct {
	db    *sql.DB
	agent string
}

// OpenShared opens (creating if needed) the shared state at path. agent names
// the writer in what it records, for whoever has to read it later.
func OpenShared(path, agent string) (*Shared, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS cooldowns (
  key    TEXT PRIMARY KEY,
  until  INTEGER NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  agent  TEXT NOT NULL DEFAULT '',
  at     INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS turns (
  key   TEXT NOT NULL,
  at    INTEGER NOT NULL,
  agent TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS turns_key_at ON turns(key, at);`); err != nil {
		db.Close()
		return nil, fmt.Errorf("credential state %s: %w", path, err)
	}
	return &Shared{db: db, agent: agent}, nil
}

// Close releases the database.
func (s *Shared) Close() error {
	if s == nil {
		return nil
	}
	return s.db.Close()
}

// Cooling is a shared cooldown: until when, and the error that caused it.
type Cooling struct {
	Until  time.Time
	Reason string
	Agent  string
}

// Cooldowns returns every credential still benched at now, by key.
func (s *Shared) Cooldowns(now time.Time) map[string]Cooling {
	out := map[string]Cooling{}
	if s == nil {
		return out
	}
	rows, err := s.db.Query(`SELECT key, until, reason, agent FROM cooldowns WHERE until > ?`, now.Unix())
	if err != nil {
		log.Printf("credentials: shared cooldowns: %v", err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var k, reason, agent string
		var until int64
		if rows.Scan(&k, &until, &reason, &agent) == nil {
			out[k] = Cooling{Until: time.Unix(until, 0), Reason: reason, Agent: agent}
		}
	}
	return out
}

// Bench records that key is unusable until `until`. A later `until` from
// another agent is kept: the longest known cap wins.
func (s *Shared) Bench(key string, until time.Time, reason string) {
	if s == nil || key == "" {
		return
	}
	now := time.Now().Unix()
	if _, err := s.db.Exec(`
INSERT INTO cooldowns(key, until, reason, agent, at) VALUES(?, ?, ?, ?, ?)
ON CONFLICT(key) DO UPDATE SET
  until  = MAX(cooldowns.until, excluded.until),
  reason = CASE WHEN excluded.until >= cooldowns.until THEN excluded.reason ELSE cooldowns.reason END,
  agent  = CASE WHEN excluded.until >= cooldowns.until THEN excluded.agent ELSE cooldowns.agent END,
  at     = excluded.at`, key, until.Unix(), reason, s.agent, now); err != nil {
		log.Printf("credentials: shared bench %s: %v", short(key), err)
	}
}

// Lift clears key's cooldown: a turn on it just succeeded, so it works.
func (s *Shared) Lift(key string) {
	if s == nil || key == "" {
		return
	}
	if _, err := s.db.Exec(`DELETE FROM cooldowns WHERE key = ?`, key); err != nil {
		log.Printf("credentials: shared lift %s: %v", short(key), err)
	}
}

// RecordTurn notes one turn on key, and forgets turns past the window.
func (s *Shared) RecordTurn(key string, at time.Time) {
	if s == nil || key == "" {
		return
	}
	if _, err := s.db.Exec(`INSERT INTO turns(key, at, agent) VALUES(?, ?, ?)`, key, at.Unix(), s.agent); err != nil {
		log.Printf("credentials: shared turn %s: %v", short(key), err)
		return
	}
	// Prune now and then, not on every turn.
	if at.Unix()%17 == 0 {
		_, _ = s.db.Exec(`DELETE FROM turns WHERE at < ?`, at.Add(-2*loadWindow).Unix())
	}
}

// Load counts each key's turns, on every agent, within the window before now.
func (s *Shared) Load(now time.Time) map[string]int {
	out := map[string]int{}
	if s == nil {
		return out
	}
	rows, err := s.db.Query(`SELECT key, COUNT(*) FROM turns WHERE at >= ? GROUP BY key`, now.Add(-loadWindow).Unix())
	if err != nil {
		log.Printf("credentials: shared load: %v", err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var n int
		if rows.Scan(&k, &n) == nil {
			out[k] = n
		}
	}
	return out
}

// Key identifies a credential across agents: the same token pasted into two
// agents is one quota, whatever each calls it. Logins are identified by their
// directory, API keys and tokens by a hash of the secret (never the secret).
func (a Account) Key() string {
	if c := a.credential(); c != "" {
		sum := sha256.Sum256([]byte(c))
		return a.Provider + ":sec:" + hex.EncodeToString(sum[:8])
	}
	dir := a.ConfigDir
	if dir == "" {
		dir = "~/.claude"
		if a.Provider == ProviderCodex {
			dir = "~/.codex"
		}
	}
	return a.Provider + ":dir:" + filepath.Clean(expandHome(dir))
}

func short(key string) string {
	if len(key) > 24 {
		return key[:24] + "…"
	}
	return key
}
