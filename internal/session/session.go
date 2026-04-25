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
	Label           string    `json:"label,omitempty"`
	Seq             int       `json:"seq"`

	mu       sync.Mutex `json:"-"`
	cancelFn func()     `json:"-"`

	// Live run state (not persisted) — populated while a request is executing
	// so /status can report what the agent is currently doing.
	runningSince time.Time `json:"-"`
	currentTask  string    `json:"-"`
	lastTool     string    `json:"-"`
	lastToolAt   time.Time `json:"-"`
	runToolCount int       `json:"-"`
}

// RunInfo is a snapshot of the live execution state for status reporting.
type RunInfo struct {
	Running      bool
	StartedAt    time.Time
	CurrentTask  string
	LastTool     string
	LastToolAt   time.Time
	ToolCount    int
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
	Label           string
	Seq             int
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

// NewWithSeq creates a session with a sequence number.
// ID format: chat_<chatID>_<seq> (e.g. chat_123_1).
func NewWithSeq(chatID int64, seq int) *Session {
	now := time.Now()
	return &Session{
		ID:        fmt.Sprintf("chat_%d_%d", chatID, seq),
		ChatID:    chatID,
		Seq:       seq,
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

// GetLabel returns the human-readable session label.
func (s *Session) GetLabel() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Label
}

// SetLabel sets the human-readable session label.
func (s *Session) SetLabel(label string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Label = label
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

// MarkRunning records that a request has started executing for this session.
// task is a short label describing the current user prompt.
func (s *Session) MarkRunning(task string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runningSince = time.Now()
	s.currentTask = task
	s.lastTool = ""
	s.lastToolAt = time.Time{}
	s.runToolCount = 0
}

// MarkIdle clears the live run state when a request finishes.
func (s *Session) MarkIdle() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runningSince = time.Time{}
	s.currentTask = ""
	s.lastTool = ""
	s.lastToolAt = time.Time{}
	s.runToolCount = 0
}

// IsRunning reports whether a request is currently executing.
func (s *Session) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.runningSince.IsZero()
}

// NoteTool records the most recent tool invocation during the live run.
func (s *Session) NoteTool(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runningSince.IsZero() {
		return
	}
	s.lastTool = name
	s.lastToolAt = time.Now()
	s.runToolCount++
}

// RunInfo returns a snapshot of the live execution state.
func (s *Session) RunInfo() RunInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return RunInfo{
		Running:     !s.runningSince.IsZero(),
		StartedAt:   s.runningSince,
		CurrentTask: s.currentTask,
		LastTool:    s.lastTool,
		LastToolAt:  s.lastToolAt,
		ToolCount:   s.runToolCount,
	}
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
		Label:           s.Label,
		Seq:             s.Seq,
	}
}

// NewFromDB creates a Session from database fields (used by SQLite store).
func NewFromDB(id string, chatID int64, created, updated time.Time, claudeSessionID string, msgCount, inTok, outTok int, compactSummary, label string, seq int) *Session {
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
		Label:           label,
		Seq:             seq,
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
