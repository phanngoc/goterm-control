package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/ngocp/goterm-control/internal/agent"
	"github.com/ngocp/goterm-control/internal/models"
	"github.com/ngocp/goterm-control/internal/session"
	"github.com/ngocp/goterm-control/internal/transcript"
)

// Deps holds the dependencies needed by RPC method handlers.
type Deps struct {
	Sessions      *session.Manager
	Resolver      *models.Resolver
	Provider      agent.ModelProvider
	ToolExecutor  agent.ToolExecutor
	Tools         []agent.ToolDef
	System        string // system prompt
	DataDir       string // data directory for transcripts
	Uptime        func() time.Duration
}

// NewMethodHandler creates a MethodHandler that routes to the appropriate handler.
func NewMethodHandler(deps Deps) MethodHandler {
	return func(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
		switch method {
		case "status":
			return handleStatus(deps)
		case "models.list":
			return handleModelsList(deps)
		case "sessions.list":
			return handleSessionsList(deps)
		case "sessions.get":
			return handleSessionsGet(deps, params)
		case "sessions.reset":
			return handleSessionsReset(deps, params)
		case "transcript.get":
			return handleTranscriptGet(deps, params)
		case "send":
			return handleSend(ctx, deps, params)
		default:
			return nil, fmt.Errorf("unknown method: %s", method)
		}
	}
}

func handleStatus(deps Deps) (json.RawMessage, error) {
	sessions := deps.Sessions.List()
	result := StatusResult{
		Running:        true,
		Uptime:         deps.Uptime().Round(time.Second).String(),
		DefaultModel:   deps.Resolver.Default(),
		ActiveSessions: len(sessions),
		Channels:       []string{"telegram", "gateway", "dashboard"},
	}
	return json.Marshal(result)
}

func handleModelsList(deps Deps) (json.RawMessage, error) {
	return json.Marshal(deps.Resolver.List())
}

// --- Session methods ---

type SessionInfo struct {
	ID              string `json:"id"`
	ChatID          int64  `json:"chat_id"`
	ClaudeSessionID string `json:"claude_session_id,omitempty"`
	MessageCount    int    `json:"message_count"`
	InputTokens     int    `json:"input_tokens"`
	OutputTokens    int    `json:"output_tokens"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

func sessionToInfo(s *session.Session) SessionInfo {
	return SessionInfo{
		ID:              s.ID,
		ChatID:          s.ChatID,
		ClaudeSessionID: s.GetSessionID(),
		MessageCount:    s.GetMessageCount(),
		InputTokens:     s.InputTokens,
		OutputTokens:    s.OutputTokens,
		CreatedAt:       s.CreatedAt.Format(time.RFC3339),
		UpdatedAt:       s.UpdatedAt.Format(time.RFC3339),
	}
}

func handleSessionsList(deps Deps) (json.RawMessage, error) {
	// Reload sessions from disk to pick up sessions created by other processes (Telegram bot)
	deps.Sessions.ReloadFromDisk()

	all := deps.Sessions.List()

	// Also scan transcript directory for sessions not in the store
	transcriptDir := filepath.Join(deps.DataDir, "transcripts")
	files, _ := transcript.ListTranscripts(transcriptDir)
	known := make(map[string]bool, len(all))
	for _, s := range all {
		known[s.ID] = true
	}
	for _, f := range files {
		base := filepath.Base(f)
		sessionID := base[:len(base)-len(".jsonl")]
		if !known[sessionID] {
			// Create a stub session from transcript metadata
			events, err := transcript.ReadLast(f, 1)
			s := &session.Session{ID: sessionID}
			if err == nil && len(events) > 0 {
				s.ChatID = events[0].ChatID
				s.UpdatedAt = events[0].Timestamp
			}
			all = append(all, s)
		}
	}

	infos := make([]SessionInfo, 0, len(all))
	for _, s := range all {
		infos = append(infos, sessionToInfo(s))
	}
	return json.Marshal(infos)
}

type sessionIDParam struct {
	ID     string `json:"id"`
	ChatID int64  `json:"chat_id"`
}

func handleSessionsGet(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	var p sessionIDParam
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	s := deps.Sessions.Get(p.ChatID)
	return json.Marshal(sessionToInfo(s))
}

func handleSessionsReset(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	var p sessionIDParam
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	deps.Sessions.Reset(p.ChatID)
	return json.Marshal(map[string]string{"status": "reset"})
}

// --- Transcript methods ---

type transcriptParam struct {
	SessionID string `json:"session_id"`
	Last      int    `json:"last"` // 0 = all
}

func handleTranscriptGet(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	var p transcriptParam
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("session_id is required")
	}

	path := filepath.Join(deps.DataDir, "transcripts", p.SessionID+".jsonl")

	var events []transcript.Event
	var err error
	if p.Last > 0 {
		events, err = transcript.ReadLast(path, p.Last)
	} else {
		events, err = transcript.ReadAll(path)
	}
	if err != nil {
		return json.Marshal([]transcript.Event{})
	}
	return json.Marshal(events)
}

// --- Streaming Send ---

// NewStreamSendHandler creates a StreamSendHandler that runs the agent with
// realtime events emitted to the WS client.
func NewStreamSendHandler(deps Deps) StreamSendHandler {
	return func(ctx context.Context, req Request, emit func(StreamEvent)) {
		var p SendParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			emit(StreamEvent{Type: "stream", Event: "error", Data: err.Error()})
			return
		}
		if p.Message == "" {
			emit(StreamEvent{Type: "stream", Event: "error", Data: "message is required"})
			return
		}

		sess := deps.Sessions.Get(dashboardChatID)

		modelID := deps.Resolver.Default()
		if p.ModelID != "" {
			if m := deps.Resolver.Lookup(p.ModelID); m != nil {
				modelID = m.ID
			}
		}
		m := deps.Resolver.Lookup(modelID)
		maxTokens := 8192
		if m != nil {
			maxTokens = m.MaxTokens
		}

		result, err := agent.RunAgent(ctx, agent.RunParams{
			Provider:     deps.Provider,
			ToolExecutor: deps.ToolExecutor,
			ModelID:      modelID,
			SystemPrompt: deps.System,
			UserMessage:  p.Message,
			Tools:        deps.Tools,
			MaxTokens:    maxTokens,
			OnText: func(text string) {
				emit(StreamEvent{Type: "stream", Event: "text", Data: text})
			},
			OnToolCall: func(name, input string) {
				emit(StreamEvent{Type: "stream", Event: "tool", Data: name})
			},
		})

		// Persist transcript
		tw := transcript.NewWriter(filepath.Join(deps.DataDir, "transcripts"))
		now := time.Now()
		tw.Append(sess.ID, transcript.Event{
			Type: transcript.EventUserMessage, Timestamp: now,
			SessionID: sess.ID, Content: p.Message,
		})

		responseText := ""
		if result != nil {
			responseText = result.Text
		}
		if responseText != "" {
			tw.Append(sess.ID, transcript.Event{
				Type: transcript.EventAssistantText, Timestamp: now,
				SessionID: sess.ID, Content: responseText,
			})
		}
		sess.IncrementMessages()
		deps.Sessions.MarkDirty()

		// Send final response
		if err != nil {
			emit(StreamEvent{Type: "stream", Event: "error", Data: err.Error()})
		}

		finalResult, _ := json.Marshal(map[string]any{
			"text":       responseText,
			"session_id": sess.ID,
			"iterations": 0,
		})
		// Write as a proper Response (not StreamEvent)
		emit(StreamEvent{Type: "response", Data: string(finalResult)})
	}
}

// --- Send (non-streaming fallback, for CLI) ---

// dashboardChatID is a fixed chatID for dashboard sessions.
const dashboardChatID int64 = 1

func handleSend(ctx context.Context, deps Deps, params json.RawMessage) (json.RawMessage, error) {
	var p SendParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.Message == "" {
		return nil, fmt.Errorf("message is required")
	}

	// Get or create dashboard session
	sess := deps.Sessions.Get(dashboardChatID)

	modelID := deps.Resolver.Default()
	if p.ModelID != "" {
		if m := deps.Resolver.Lookup(p.ModelID); m != nil {
			modelID = m.ID
		}
	}

	m := deps.Resolver.Lookup(modelID)
	maxTokens := 8192
	if m != nil {
		maxTokens = m.MaxTokens
	}

	result, err := agent.RunAgent(ctx, agent.RunParams{
		Provider:     deps.Provider,
		ToolExecutor: deps.ToolExecutor,
		ModelID:      modelID,
		SystemPrompt: deps.System,
		UserMessage:  p.Message,
		Tools:        deps.Tools,
		MaxTokens:    maxTokens,
	})
	if err != nil {
		return nil, err
	}

	// Persist transcript
	tw := transcript.NewWriter(filepath.Join(deps.DataDir, "transcripts"))
	now := time.Now()
	tw.Append(sess.ID, transcript.Event{
		Type: transcript.EventUserMessage, Timestamp: now,
		SessionID: sess.ID, Content: p.Message,
	})
	tw.Append(sess.ID, transcript.Event{
		Type: transcript.EventAssistantText, Timestamp: now,
		SessionID: sess.ID, Content: result.Text,
	})

	// Update session counters
	sess.IncrementMessages()
	deps.Sessions.MarkDirty()

	return json.Marshal(map[string]any{
		"text":       result.Text,
		"session_id": sess.ID,
		"iterations": result.Iterations,
		"usage":      result.Usage,
	})
}
