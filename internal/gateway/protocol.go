package gateway

import (
	"encoding/json"

	"github.com/ngocp/goterm-control/internal/browserbridge"
	"github.com/ngocp/goterm-control/internal/credentials"
)

// Request is a JSON-RPC-style request from a client.
type Request struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON-RPC-style response to a client.
type Response struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *RPCError       `json:"error,omitempty"`
}

// RPCError describes an error in a response.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// StreamEvent is sent during streaming responses.
type StreamEvent struct {
	ID    string `json:"id"`
	Type  string `json:"type"`            // "stream" or "response"
	Event string `json:"event,omitempty"` // "text", "tool", "error", "end"
	Data  string `json:"data,omitempty"`
}

// SendParams are the parameters for the "send" method.
type SendParams struct {
	Message   string `json:"message"`
	ChatID    string `json:"chat_id,omitempty"`
	ModelID   string `json:"model_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// StatusResult is returned by the "status" method.
type StatusResult struct {
	Running bool `json:"running"`

	// Who this gateway is. A machine runs several, and every consumer — the
	// menu bar, the dashboard, a peer — otherwise has to infer identity from
	// the port it happened to dial.
	AgentID   string `json:"agent_id,omitempty"`
	AgentName string `json:"agent_name,omitempty"`
	Workspace string `json:"workspace,omitempty"` // where this agent's files live

	Uptime         string                `json:"uptime"`
	DefaultModel   string                `json:"default_model"`
	ActiveSessions int                   `json:"active_sessions"`
	Channels       []string              `json:"channels"`
	Runs           []RunInfo             `json:"runs,omitempty"`    // in-flight agent runs (live)
	Browser        *browserbridge.Status `json:"browser,omitempty"` // Browser Bridge; nil when disabled

	// Accounts is the credential pool this agent rotates sessions across.
	// Empty when none is configured, which means the ambient credentials.
	Accounts []credentials.Status `json:"accounts,omitempty"`
}

// RunInfo describes one in-flight agent run for status consumers
// (dashboard, menu bar tray).
type RunInfo struct {
	ChatID    int64  `json:"chat_id"`
	SessionID string `json:"session_id"`
	Label     string `json:"label,omitempty"`
	Task      string `json:"task,omitempty"`
	LastTool  string `json:"last_tool,omitempty"`
	ToolCount int    `json:"tool_count"`
	StartedAt string `json:"started_at"`
}
