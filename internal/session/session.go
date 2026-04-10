package session

import (
	"fmt"
	"sync"
	"time"
)

// Session holds per-chat conversation state for the claude CLI subprocess.
type Session struct {
	ID              string    `json:"id"`
	ChatID          int64     `json:"chat_id"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	ClaudeSessionID string    `json:"claude_session_id,omitempty"`
	MessageCount    int       `json:"message_count"`
	InputTokens     int       `json:"input_tokens"`
	OutputTokens    int       `json:"output_tokens"`
	CompactSummary  string    `json:"compact_summary,omitempty"`

	mu       sync.Mutex `json:"-"`
	cancelFn func()     `json:"-"`
}

// SessionSnapshot is a mutex-free copy of session fields for persistence.
type SessionSnapshot struct {
	ID              string
	ChatID          int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
	ClaudeSessionID string
	MessageCount    int
	InputTokens     int
	OutputTokens    int
	CompactSummary  string
}

func New(chatID int64) *Session {
	now := time.Now()
	return &Session{
		ID:        fmt.Sprintf("chat_%d", chatID),
		ChatID:    chatID,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// GetSessionID returns the claude CLI session ID, or "" if not started yet.
func (s *Session) GetSessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ClaudeSessionID
}

// SetSessionID stores the claude CLI session ID returned on first message.
func (s *Session) SetSessionID(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ClaudeSessionID = id
	s.UpdatedAt = time.Now()
}

// IncrementMessages increments the turn counter after each exchange.
func (s *Session) IncrementMessages() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.MessageCount++
	s.UpdatedAt = time.Now()
}

// GetMessageCount returns the number of turns exchanged.
func (s *Session) GetMessageCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.MessageCount
}

// AddTokens records token usage from a run.
func (s *Session) AddTokens(input, output int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.InputTokens += input
	s.OutputTokens += output
	s.UpdatedAt = time.Now()
}

// Reset clears conversation history by forgetting the CLI session ID.
// The next message will start a fresh claude session.
func (s *Session) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ClaudeSessionID = ""
	s.MessageCount = 0
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

// Snapshot returns a mutex-free copy of all session fields for safe persistence.
func (s *Session) Snapshot() SessionSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SessionSnapshot{
		ID:              s.ID,
		ChatID:          s.ChatID,
		CreatedAt:       s.CreatedAt,
		UpdatedAt:       s.UpdatedAt,
		ClaudeSessionID: s.ClaudeSessionID,
		MessageCount:    s.MessageCount,
		InputTokens:     s.InputTokens,
		OutputTokens:    s.OutputTokens,
		CompactSummary:  s.CompactSummary,
	}
}

// NewFromDB creates a Session from database fields (used by SQLite store).
func NewFromDB(id string, chatID int64, created, updated time.Time, claudeSessionID string, msgCount, inTok, outTok int, compactSummary string) *Session {
	return &Session{
		ID:              id,
		ChatID:          chatID,
		CreatedAt:       created,
		UpdatedAt:       updated,
		ClaudeSessionID: claudeSessionID,
		MessageCount:    msgCount,
		InputTokens:     inTok,
		OutputTokens:    outTok,
		CompactSummary:  compactSummary,
	}
}

// GetCompactSummary returns the persisted compaction summary.
func (s *Session) GetCompactSummary() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.CompactSummary
}

// SetCompactSummary stores a compaction summary for persistence.
func (s *Session) SetCompactSummary(summary string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.CompactSummary = summary
	s.UpdatedAt = time.Now()
}
