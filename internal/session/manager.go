package session

import (
	"log"
	"sync"
	"time"
)

// Manager stores sessions keyed by chat ID with disk persistence and idle reset.
type Manager struct {
	mu          sync.RWMutex
	sessions    map[int64]*Session
	store       *Store
	idleTimeout time.Duration

	// Debounced save: after any mutation, schedule a save after saveCooldown.
	saveMu    sync.Mutex
	saveTimer *time.Timer
	dirty     bool
}

// NewManager creates a manager with persistence and idle timeout.
// If store is nil, sessions are in-memory only.
func NewManager(store *Store, idleTimeout time.Duration) *Manager {
	m := &Manager{
		sessions:    make(map[int64]*Session),
		store:       store,
		idleTimeout: idleTimeout,
	}

	if store != nil {
		loaded, err := store.Load()
		if err != nil {
			log.Printf("session: failed to load from disk: %v", err)
		} else {
			m.sessions = loaded
			log.Printf("session: loaded %d sessions from disk", len(loaded))
		}
	}

	return m
}

// Get returns existing session or creates a new one.
// Automatically resets sessions that have been idle longer than idleTimeout.
func (m *Manager) Get(chatID int64) *Session {
	m.mu.RLock()
	s, ok := m.sessions[chatID]
	m.mu.RUnlock()

	if ok {
		if m.idleTimeout > 0 && time.Since(s.UpdatedAt) > m.idleTimeout {
			log.Printf("session: idle reset chat_%d (idle %s)", chatID, time.Since(s.UpdatedAt).Round(time.Second))
			s.Reset()
			m.scheduleSave()
		}
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
	m.scheduleSave()
	return s
}

// Reset clears history for a chat.
func (m *Manager) Reset(chatID int64) {
	m.mu.RLock()
	s, ok := m.sessions[chatID]
	m.mu.RUnlock()
	if ok {
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
	sessions := m.sessions
	m.mu.RUnlock()

	if err := m.store.Save(sessions); err != nil {
		log.Printf("session: save error: %v", err)
	} else {
		log.Printf("session: saved %d sessions to disk", len(sessions))
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
