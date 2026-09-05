package codex

import "encoding/json"

// event is one line of `codex exec --json` output.
type event struct {
	Type     string        `json:"type"`
	ThreadID string        `json:"thread_id"`
	Item     *threadItem   `json:"item"`
	Usage    *usage        `json:"usage"`
	Error    *errorPayload `json:"error"`
	Message  string        `json:"message"`
}

// threadItem is the union of every item payload codex emits. Fields not
// relevant to an item's type are simply absent.
type threadItem struct {
	ID   string `json:"id"`
	Type string `json:"type"`

	// agent_message, reasoning
	Text string `json:"text"`

	// command_execution
	Command          string `json:"command"`
	AggregatedOutput string `json:"aggregated_output"`
	ExitCode         *int   `json:"exit_code"`

	// file_change
	Changes []fileChange `json:"changes"`

	// mcp_tool_call
	Server    string          `json:"server"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
	Result    json.RawMessage `json:"result"`
	Error     string          `json:"error"`

	// web_search
	Query string `json:"query"`

	// error (item-level warning)
	Message string `json:"message"`

	// command_execution, file_change, mcp_tool_call
	Status string `json:"status"` // in_progress | completed | failed
}

type fileChange struct {
	Path string `json:"path"`
	Kind string `json:"kind"` // add | delete | update
}

type usage struct {
	InputTokens       int `json:"input_tokens"`
	CachedInputTokens int `json:"cached_input_tokens"`
	OutputTokens      int `json:"output_tokens"`
}

type errorPayload struct {
	Message string `json:"message"`
}
