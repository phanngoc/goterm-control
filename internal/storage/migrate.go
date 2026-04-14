package storage

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

// legacySession mirrors the JSON structure in sessions.json.
type legacySession struct {
	ID              string    `json:"id"`
	ChatID          int64     `json:"chat_id"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	ClaudeSessionID string    `json:"claude_session_id,omitempty"`
	MessageCount    int       `json:"message_count"`
	InputTokens     int       `json:"input_tokens"`
	OutputTokens    int       `json:"output_tokens"`
}

// migrateFromLegacy imports sessions.json if it exists.
func (db *DB) migrateFromLegacy() error {
	sessionsPath := filepath.Join(db.dataDir, "sessions.json")

	if n, err := db.importSessions(sessionsPath); err != nil {
		log.Printf("storage: migrate sessions: %v", err)
	} else if n > 0 {
		log.Printf("storage: imported %d sessions from %s", n, sessionsPath)
		log.Println("storage: legacy data import complete (original files kept for rollback)")
	}

	return nil
}

func (db *DB) importSessions(path string) (int, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", path, err)
	}

	var raw map[string]*legacySession
	if err := json.Unmarshal(data, &raw); err != nil {
		return 0, fmt.Errorf("parse %s: %w", path, err)
	}

	if len(raw) == 0 {
		return 0, nil
	}

	tx, err := db.conn.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	sessStmt, err := tx.Prepare(`INSERT OR IGNORE INTO sessions
		(id, chat_id, created_at, updated_at, claude_session_id, message_count, input_tokens, output_tokens, label, seq)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, '', 0)`)
	if err != nil {
		return 0, err
	}
	defer sessStmt.Close()

	csStmt, err := tx.Prepare(`INSERT OR IGNORE INTO chat_state
		(chat_id, active_session_id, next_seq)
		VALUES (?, ?, 1)`)
	if err != nil {
		return 0, err
	}
	defer csStmt.Close()

	count := 0
	for _, s := range raw {
		_, err := sessStmt.Exec(
			s.ID, s.ChatID,
			s.CreatedAt.Format(time.RFC3339), s.UpdatedAt.Format(time.RFC3339),
			s.ClaudeSessionID, s.MessageCount, s.InputTokens, s.OutputTokens,
		)
		if err != nil {
			log.Printf("storage: skip session %s: %v", s.ID, err)
			continue
		}
		csStmt.Exec(s.ChatID, s.ID)
		count++
	}

	return count, tx.Commit()
}
