package session

import (
	"sync"
)

// Manager stores sessions keyed by chat ID.
type Manager struct {
	mu       sync.RWMutex
	sessions map[int64]*Session
}

func NewManager() *Manager {
	return &Manager{
		sessions: make(map[int64]*Session),
	}
}

// Get returns existing session or creates a new one.
func (m *Manager) Get(chatID int64) *Session {
	m.mu.RLock()
	s, ok := m.sessions[chatID]
	m.mu.RUnlock()
	if ok {
		return s
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	// Double-check after acquiring write lock
	if s, ok = m.sessions[chatID]; ok {
		return s
	}
	s = New(chatID)
	m.sessions[chatID] = s
	return s
}

// Reset clears history for a chat.
func (m *Manager) Reset(chatID int64) {
	m.mu.RLock()
	s, ok := m.sessions[chatID]
	m.mu.RUnlock()
	if ok {
		s.Reset()
	}
}
