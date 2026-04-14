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

// chatStateData is the serializable form for multi-session storage.
type chatStateData struct {
	ActiveSessionID string              `json:"active_session_id"`
	NextSeq         int                 `json:"next_seq"`
	Sessions        map[string]*Session `json:"sessions"`
}

// Load reads sessions from disk. Returns empty map if file does not exist.
// Backward compatible: detects old flat-session format and wraps in ChatState.
func (s *Store) Load() (map[int64]*ChatState, error) {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return make(map[int64]*ChatState), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read sessions: %w", err)
	}

	// Try new format first: map[string]chatStateData
	var newRaw map[string]*chatStateData
	if err := json.Unmarshal(data, &newRaw); err == nil {
		// Check if this is actually the new format (has "sessions" key in values)
		if isNewFormat(newRaw) {
			return convertNewFormat(newRaw), nil
		}
	}

	// Fall back to old format: map[string]*Session (flat)
	var oldRaw map[string]*Session
	if err := json.Unmarshal(data, &oldRaw); err != nil {
		return nil, fmt.Errorf("parse sessions: %w", err)
	}

	chats := make(map[int64]*ChatState, len(oldRaw))
	for _, sess := range oldRaw {
		chats[sess.ChatID] = &ChatState{
			ActiveSessionID: sess.ID,
			NextSeq:         1,
			Sessions:        map[string]*Session{sess.ID: sess},
		}
	}
	return chats, nil
}

// Save writes sessions to disk atomically (write tmp then rename).
func (s *Store) Save(chats map[int64]*ChatState) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	raw := make(map[string]*chatStateData, len(chats))
	for chatID, cs := range chats {
		key := fmt.Sprintf("%d", chatID)
		sessions := make(map[string]*Session, len(cs.Sessions))
		for id, sess := range cs.Sessions {
			sess.mu.Lock()
			sessions[id] = sess
			sess.mu.Unlock()
		}
		raw[key] = &chatStateData{
			ActiveSessionID: cs.ActiveSessionID,
			NextSeq:         cs.NextSeq,
			Sessions:        sessions,
		}
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

// isNewFormat checks if the parsed data has the ChatState structure.
func isNewFormat(raw map[string]*chatStateData) bool {
	for _, v := range raw {
		if v != nil && v.Sessions != nil {
			return true
		}
		return false
	}
	return false
}

// convertNewFormat converts parsed new-format data to the ChatState map.
func convertNewFormat(raw map[string]*chatStateData) map[int64]*ChatState {
	chats := make(map[int64]*ChatState, len(raw))
	for _, csd := range raw {
		if csd == nil || len(csd.Sessions) == 0 {
			continue
		}
		// Get chatID from the first session
		var chatID int64
		for _, sess := range csd.Sessions {
			chatID = sess.ChatID
			break
		}
		chats[chatID] = &ChatState{
			ActiveSessionID: csd.ActiveSessionID,
			NextSeq:         csd.NextSeq,
			Sessions:        csd.Sessions,
		}
	}
	return chats
}
