package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Store handles JSON persistence of sessions to disk.
type Store struct {
	path string
}

// NewStore creates a store that reads/writes sessions to the given file path.
func NewStore(path string) *Store {
	return &Store{path: path}
}

// sessionData is the serializable form (keyed by chatID as string).
type sessionData map[string]*Session

// Load reads sessions from disk. Returns empty map if file does not exist.
func (s *Store) Load() (map[int64]*Session, error) {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return make(map[int64]*Session), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read sessions: %w", err)
	}

	var raw sessionData
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse sessions: %w", err)
	}

	sessions := make(map[int64]*Session, len(raw))
	for _, sess := range raw {
		sessions[sess.ChatID] = sess
	}
	return sessions, nil
}

// Save writes sessions to disk atomically (write tmp then rename).
func (s *Store) Save(sessions map[int64]*Session) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	raw := make(sessionData, len(sessions))
	for _, sess := range sessions {
		sess.mu.Lock()
		raw[sess.ID] = sess
		sess.mu.Unlock()
	}

	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal sessions: %w", err)
	}

	tmp := fmt.Sprintf("%s.tmp.%d", s.path, time.Now().UnixNano())
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}
