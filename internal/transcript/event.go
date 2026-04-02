package transcript

import "time"

// EventType identifies the kind of transcript event.
type EventType string

const (
	EventUserMessage   EventType = "user_message"
	EventAssistantText EventType = "assistant_text"
	EventToolCall      EventType = "tool_call"
	EventToolResult    EventType = "tool_result"
	EventSessionStart  EventType = "session_start"
	EventSessionReset  EventType = "session_reset"
)

// Event is a single transcript entry, serialized as one JSON line.
type Event struct {
	Type      EventType `json:"type"`
	Timestamp time.Time `json:"ts"`
	SessionID string    `json:"session_id,omitempty"`
	ChatID    int64     `json:"chat_id,omitempty"`
	Content   string    `json:"content,omitempty"`
	ToolName  string    `json:"tool_name,omitempty"`
	ToolInput string    `json:"tool_input,omitempty"`
	IsError   bool      `json:"is_error,omitempty"`
}
