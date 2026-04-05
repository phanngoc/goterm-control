package gateway

import "encoding/json"

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
	Type  string `json:"type"`  // "stream" or "response"
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
	Running       bool     `json:"running"`
	Uptime        string   `json:"uptime"`
	DefaultModel  string   `json:"default_model"`
	ActiveSessions int     `json:"active_sessions"`
	Channels      []string `json:"channels"`
}
