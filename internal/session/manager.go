package session

import (
	"fmt"
	"log"
	"sort"
	"sync"
	"time"
)

// MaxSessionsPerChat is the maximum number of sessions allowed per chat.
const MaxSessionsPerChat = 20

// ChatState tracks all sessions and the active session for a single chat.
type ChatState struct {
	ActiveSessionID string
	NextSeq         int
	Sessions        map[string]*Session // keyed by session ID
}

// SessionPersister is the interface for session persistence backends.
type SessionPersister interface {
	Load() (map[int64]*ChatState, error)
	Save(chats map[int64]*ChatState) error
}

// Manager stores sessions keyed by chat ID with disk persistence.
type Manager struct {
	mu    sync.RWMutex
	chats map[int64]*ChatState
	store SessionPersister

	// Debounced save: after any mutation, schedule a save after saveCooldown.
	saveMu    sync.Mutex
	saveTimer *time.Timer
	dirty     bool
}

// NewManager creates a manager with persistence.
// If store is nil, sessions are in-memory only.
func NewManager(store SessionPersister) *Manager {
	m := &Manager{
		chats: make(map[int64]*ChatState),
		store: store,
	}

	if store != nil {
		loaded, err := store.Load()
		if err != nil {
			log.Printf("session: failed to load from disk: %v", err)
		} else {
			m.chats = loaded
			total := 0
			for _, cs := range loaded {
				total += len(cs.Sessions)
			}
			log.Printf("session: loaded %d chats (%d sessions) from disk", len(loaded), total)
		}
	}

	return m
}

// Get returns the active session for a chat, creating a new ChatState if needed.
// This is the hot path — all existing callers continue to work unchanged.
func (m *Manager) Get(chatID int64) *Session {
	m.mu.RLock()
	cs, ok := m.chats[chatID]
	m.mu.RUnlock()

	if ok {
		if s, exists := cs.Sessions[cs.ActiveSessionID]; exists {
			return s
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	// Double-check after acquiring write lock
	if cs, ok = m.chats[chatID]; ok {
		if s, exists := cs.Sessions[cs.ActiveSessionID]; exists {
			return s
		}
		// ChatState exists but ActiveSessionID is stale — recover by picking
		// the most recently updated session instead of wiping all sessions.
		if len(cs.Sessions) > 0 {
			cs.ActiveSessionID = pickMostRecentSession(cs.Sessions)
			m.scheduleSave()
			return cs.Sessions[cs.ActiveSessionID]
		}
		// No sessions left in this ChatState — create a new one in place.
		s := New(chatID)
		cs.Sessions[s.ID] = s
		cs.ActiveSessionID = s.ID
		if cs.NextSeq < 1 {
			cs.NextSeq = 1
		}
		m.scheduleSave()
		return s
	}

	// Brand new chat — create ChatState with first session.
	s := New(chatID)
	m.chats[chatID] = &ChatState{
		ActiveSessionID: s.ID,
		NextSeq:         1,
		Sessions:        map[string]*Session{s.ID: s},
	}
	m.scheduleSave()
	return s
}

// pickMostRecentSession returns the ID of the most recently updated session.
func pickMostRecentSession(sessions map[string]*Session) string {
	var bestID string
	var bestTime time.Time
	for id, s := range sessions {
		if s.UpdatedAt.After(bestTime) {
			bestTime = s.UpdatedAt
			bestID = id
		}
	}
	return bestID
}

// GetByID returns a specific session by its ID, or nil if not found.
func (m *Manager) GetByID(sessionID string) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, cs := range m.chats {
		if s, ok := cs.Sessions[sessionID]; ok {
			return s
		}
	}
	return nil
}

// ListForChat returns all sessions for a chat, sorted by UpdatedAt descending.
func (m *Manager) ListForChat(chatID int64) []*Session {
	m.mu.RLock()
	defer m.mu.RUnlock()

	cs, ok := m.chats[chatID]
	if !ok {
		return nil
	}

	out := make([]*Session, 0, len(cs.Sessions))
	for _, s := range cs.Sessions {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out
}

// NewSession creates a new session for a chat and makes it active.
// Returns error if the session limit is exceeded.
func (m *Manager) NewSession(chatID int64) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	cs, ok := m.chats[chatID]
	if !ok {
		// No chat state yet — create one with the first session.
		s := New(chatID)
		m.chats[chatID] = &ChatState{
			ActiveSessionID: s.ID,
			NextSeq:         1,
			Sessions:        map[string]*Session{s.ID: s},
		}
		m.scheduleSave()
		return s, nil
	}

	if len(cs.Sessions) >= MaxSessionsPerChat {
		return nil, fmt.Errorf("session limit reached (%d)", MaxSessionsPerChat)
	}

	seq := cs.NextSeq
	s := NewWithSeq(chatID, seq)
	cs.Sessions[s.ID] = s
	cs.ActiveSessionID = s.ID
	cs.NextSeq = seq + 1
	m.scheduleSave()
	return s, nil
}

// SwitchActive changes the active session for a chat.
func (m *Manager) SwitchActive(chatID int64, sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	cs, ok := m.chats[chatID]
	if !ok {
		return fmt.Errorf("no sessions for chat %d", chatID)
	}
	if _, exists := cs.Sessions[sessionID]; !exists {
		return fmt.Errorf("session %s not found in chat %d", sessionID, chatID)
	}
	cs.ActiveSessionID = sessionID
	m.scheduleSave()
	return nil
}

// ActiveSessionID returns the active session ID for a chat, or "".
func (m *Manager) ActiveSessionID(chatID int64) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if cs, ok := m.chats[chatID]; ok {
		return cs.ActiveSessionID
	}
	return ""
}

// ReloadFromDisk re-reads stored data, merging any new sessions from other processes.
func (m *Manager) ReloadFromDisk() {
	if m.store == nil {
		return
	}
	loaded, err := m.store.Load()
	if err != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for chatID, lcs := range loaded {
		if _, ok := m.chats[chatID]; !ok {
			m.chats[chatID] = lcs
		}
	}
}

// List returns all sessions across all chats.
func (m *Manager) List() []*Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*Session
	for _, cs := range m.chats {
		for _, s := range cs.Sessions {
			out = append(out, s)
		}
	}
	return out
}

// Reset clears history for the active session in a chat.
func (m *Manager) Reset(chatID int64) {
	m.mu.RLock()
	cs, ok := m.chats[chatID]
	m.mu.RUnlock()
	if !ok {
		return
	}
	if s, exists := cs.Sessions[cs.ActiveSessionID]; exists {
		s.Reset()
		m.scheduleSave()
	}
}

// MarkDirty signals that session state has changed and should be persisted.
func (m *Manager) MarkDirty() {
	m.scheduleSave()
}

// SaveNow forces an immediate save to disk. Call on graceful shutdown.
func (m *Manager) SaveNow() {
	if m.store == nil {
		return
	}
	m.saveMu.Lock()
	if m.saveTimer != nil {
		m.saveTimer.Stop()
		m.saveTimer = nil
	}
	m.dirty = false
	m.saveMu.Unlock()

	m.mu.RLock()
	chats := m.chats
	m.mu.RUnlock()

	if err := m.store.Save(chats); err != nil {
		log.Printf("session: save error: %v", err)
	} else {
		total := 0
		for _, cs := range chats {
			total += len(cs.Sessions)
		}
		log.Printf("session: saved %d chats (%d sessions) to disk", len(chats), total)
	}
}

// scheduleSave debounces disk writes — saves 1 second after the last mutation.
func (m *Manager) scheduleSave() {
	if m.store == nil {
		return
	}
	m.saveMu.Lock()
	defer m.saveMu.Unlock()

	m.dirty = true
	if m.saveTimer != nil {
		m.saveTimer.Stop()
	}
	m.saveTimer = time.AfterFunc(1*time.Second, func() {
		m.SaveNow()
	})
}
