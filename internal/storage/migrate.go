package storage

import (
	"bufio"
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

// legacyMemoryEntry mirrors the JSONL structure in memory.jsonl.
type legacyMemoryEntry struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	SessionID string    `json:"session_id"`
	ChatID    int64     `json:"chat_id,omitempty"`
	Facts     []string  `json:"facts"`
	Keywords  []string  `json:"keywords"`
	Summary   string    `json:"summary"`
}

// migrateFromLegacy imports sessions.json and memory/memory.jsonl if they exist.
func (db *DB) migrateFromLegacy() error {
	sessionsPath := filepath.Join(db.dataDir, "sessions.json")
	memoryPath := filepath.Join(db.dataDir, "memory", "memory.jsonl")

	imported := false

	if n, err := db.importSessions(sessionsPath); err != nil {
		log.Printf("storage: migrate sessions: %v", err)
	} else if n > 0 {
		log.Printf("storage: imported %d sessions from %s", n, sessionsPath)
		imported = true
	}

	if n, err := db.importMemory(memoryPath); err != nil {
		log.Printf("storage: migrate memory: %v", err)
	} else if n > 0 {
		log.Printf("storage: imported %d memory entries from %s", n, memoryPath)
		imported = true
	}

	if imported {
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

	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO sessions
		(id, chat_id, created_at, updated_at, claude_session_id, message_count, input_tokens, output_tokens)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	count := 0
	for _, s := range raw {
		_, err := stmt.Exec(
			s.ID, s.ChatID,
			s.CreatedAt.Format(time.RFC3339), s.UpdatedAt.Format(time.RFC3339),
			s.ClaudeSessionID, s.MessageCount, s.InputTokens, s.OutputTokens,
		)
		if err != nil {
			log.Printf("storage: skip session %s: %v", s.ID, err)
			continue
		}
		count++
	}

	return count, tx.Commit()
}

func (db *DB) importMemory(path string) (int, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	tx, err := db.conn.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO memory
		(id, created_at, session_id, chat_id, facts, keywords, summary)
		VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)

	count := 0
	for scanner.Scan() {
		var e legacyMemoryEntry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue
		}

		factsJSON, _ := json.Marshal(e.Facts)
		keywordsJSON, _ := json.Marshal(e.Keywords)

		_, err := stmt.Exec(
			e.ID, e.CreatedAt.Format(time.RFC3339),
			e.SessionID, e.ChatID,
			string(factsJSON), string(keywordsJSON), e.Summary,
		)
		if err != nil {
			continue
		}
		count++
	}

	if err := scanner.Err(); err != nil {
		return count, fmt.Errorf("scan %s: %w", path, err)
	}
	return count, tx.Commit()
}
