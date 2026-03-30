package session

import (
	"fmt"
	"sync"
	"time"
)

// Session holds per-chat conversation state for the claude CLI subprocess.
type Session struct {
	ID        string
	ChatID    int64
	CreatedAt time.Time
	UpdatedAt time.Time
	mu        sync.Mutex

	// claudeSessionID is the session_id returned by the claude CLI on first use.
	// Subsequent messages pass --resume <claudeSessionID> to continue the conversation.
	claudeSessionID string

	// messageCount tracks how many turns have been exchanged.
	messageCount int

	// cancelFn cancels any in-flight Claude request for this session.
	cancelFn func()
}

func New(chatID int64) *Session {
	return &Session{
		ID:        fmt.Sprintf("chat_%d", chatID),
		ChatID:    chatID,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
}

// GetSessionID returns the claude CLI session ID, or "" if not started yet.
func (s *Session) GetSessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claudeSessionID
}

// SetSessionID stores the claude CLI session ID returned on first message.
func (s *Session) SetSessionID(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claudeSessionID = id
	s.UpdatedAt = time.Now()
}

// IncrementMessages increments the turn counter after each exchange.
func (s *Session) IncrementMessages() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messageCount++
	s.UpdatedAt = time.Now()
}

// MessageCount returns the number of turns exchanged.
func (s *Session) MessageCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.messageCount
}

// Reset clears conversation history by forgetting the CLI session ID.
// The next message will start a fresh claude session.
func (s *Session) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claudeSessionID = ""
	s.messageCount = 0
	s.UpdatedAt = time.Now()
}

// SetCancel stores a cancel function for the current in-flight request.
func (s *Session) SetCancel(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelFn = fn
}

// Cancel cancels the current in-flight request if any.
func (s *Session) Cancel() {
	s.mu.Lock()
	fn := s.cancelFn
	s.cancelFn = nil
	s.mu.Unlock()
	if fn != nil {
		fn()
	}
}
