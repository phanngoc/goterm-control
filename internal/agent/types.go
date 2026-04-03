package agent

import (
	"context"
	"encoding/json"
)

// Message represents a conversation message (user, assistant, or tool result).
type Message struct {
	Role        string        `json:"role"` // "user", "assistant"
	Content     string        `json:"content,omitempty"`
	ToolCalls   []ToolCall    `json:"tool_calls,omitempty"`
	ToolResults []ToolResult  `json:"tool_results,omitempty"`
}

// ToolCall represents a model's request to use a tool.
type ToolCall struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// ToolResult represents the output of a tool execution.
type ToolResult struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	IsError bool   `json:"is_error,omitempty"`
}

// ToolDef defines a tool the model can use.
type ToolDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"input_schema"`
}

// Usage tracks token consumption for a model call.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	CacheRead    int `json:"cache_read,omitempty"`
	CacheWrite   int `json:"cache_write,omitempty"`
}

// StreamEvent is emitted during model streaming.
type StreamEvent struct {
	Type      string          // "text", "tool_use", "end", "error"
	Text      string          // for "text"
	ToolID    string          // for "tool_use"
	ToolName  string          // for "tool_use"
	ToolInput json.RawMessage // for "tool_use"
	Error     error           // for "error"
	Usage     *Usage          // for "end"
	StopReason string         // for "end": "end_turn", "tool_use"
}

// ModelProvider abstracts over different model backends (direct API, CLI subprocess).
type ModelProvider interface {
	Stream(ctx context.Context, params StreamParams) (<-chan StreamEvent, error)
}

// StreamParams configures a model streaming call.
type StreamParams struct {
	Model        string
	SystemPrompt string
	Messages     []Message
	Tools        []ToolDef
	MaxTokens    int
}

// ToolExecutor runs tool calls and returns results.
type ToolExecutor interface {
	Execute(ctx context.Context, name string, input json.RawMessage) ToolResult
}

// OnTextFunc is called with each text chunk during streaming.
type OnTextFunc func(text string)

// OnToolCallFunc is called when the model invokes a tool.
type OnToolCallFunc func(name string, input string)

// OnToolResultFunc is called after a tool executes.
type OnToolResultFunc func(name string, output string, isError bool)

// RunParams configures an agent loop execution.
type RunParams struct {
	Provider     ModelProvider
	ToolExecutor ToolExecutor
	ModelID      string
	SystemPrompt string
	UserMessage  string
	Messages     []Message     // existing conversation history
	Tools        []ToolDef
	MaxTokens    int
	MaxIterations int          // safety valve (default 50)

	// Streaming callbacks (optional)
	OnText       OnTextFunc
	OnToolCall   OnToolCallFunc
	OnToolResult OnToolResultFunc
}

// RunResult is returned after the agent loop completes.
type RunResult struct {
	Text         string
	Messages     []Message // full conversation including new messages
	Usage        Usage     // total usage across all iterations
	Iterations   int       // how many model calls were made
	StopReason   string    // "end_turn", "max_iterations", "error"
	Error        error
}
