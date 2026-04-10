package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ngocp/goterm-control/internal/agent"
	"github.com/ngocp/goterm-control/internal/memory"
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
	Memory        memory.MemoryBackend
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
		case "cancel":
			return handleCancel(deps)
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

		// Use session_id from client, or create a default
		sessionID := p.SessionID
		if sessionID == "" {
			sessionID = fmt.Sprintf("chat_%d", dashboardChatID)
		}

		tw := transcript.NewWriter(filepath.Join(deps.DataDir, "transcripts"))

		// Persist user message IMMEDIATELY so it survives reload
		tw.Append(sessionID, transcript.Event{
			Type: transcript.EventUserMessage, Timestamp: time.Now(),
			SessionID: sessionID, Content: p.Message,
		})
		deps.Sessions.MarkDirty()

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

		// 5-minute timeout for agent execution
		agentCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()

		// --- 1. Load session history from transcript ---
		var sessionMessages []agent.Message
		transcriptPath := filepath.Join(deps.DataDir, "transcripts", sessionID+".jsonl")
		if events, err := transcript.ReadAll(transcriptPath); err == nil {
			sessionMessages = transcriptToMessages(events)
			// Keep last 20 turns max to fit context window
			if len(sessionMessages) > 40 {
				sessionMessages = sessionMessages[len(sessionMessages)-40:]
			}
		}

		// --- 2. Build rich system prompt ---
		home, _ := os.UserHomeDir()
		workspace := home + "/goterm-workspace"
		os.MkdirAll(workspace, 0755)

		systemPrompt := deps.System

		// Workspace context
		systemPrompt += fmt.Sprintf("\n\n## Workspace\n"+
			"- Working directory: %s\n"+
			"- Always `cd` here before running commands.\n"+
			"- User's projects live in subdirectories of this workspace.\n", workspace)

		// Timezone + identity
		systemPrompt += fmt.Sprintf("\n\n## Runtime\n"+
			"- Current time: %s\n"+
			"- User: %s\n"+
			"- Session: %s\n"+
			"- Platform: macOS\n",
			time.Now().Format("2006-01-02 15:04:05 MST"),
			os.Getenv("USER"),
			sessionID)

		// --- 3. Memory injection ---
		if deps.Memory != nil {
			memCtx := memory.BuildMemoryContext(deps.Memory, p.Message, 5)
			if memCtx != "" {
				systemPrompt += memCtx
			}
		}

		// Track streamed text for persistence
		var streamedText strings.Builder

		result, err := agent.RunAgent(agentCtx, agent.RunParams{
			Provider:     deps.Provider,
			ToolExecutor: deps.ToolExecutor,
			ModelID:      modelID,
			SystemPrompt: systemPrompt,
			UserMessage:  p.Message,
			Messages:     sessionMessages, // ← session history injected!
			Tools:        deps.Tools,
			MaxTokens:    maxTokens,
			OnText: func(text string) {
				streamedText.WriteString(text)
				emit(StreamEvent{Type: "stream", Event: "text", Data: text})
			},
			OnToolCall: func(name, input string) {
				summary := toolSummary(name, input)
				emit(StreamEvent{Type: "stream", Event: "tool", Data: summary})
			},
		})

		// Persist assistant response
		responseText := ""
		if result != nil && result.Text != "" {
			responseText = result.Text
		} else if streamedText.Len() > 0 {
			responseText = streamedText.String()
		}
		if responseText != "" {
			tw.Append(sessionID, transcript.Event{
				Type: transcript.EventAssistantText, Timestamp: time.Now(),
				SessionID: sessionID, Content: responseText,
			})
		}

		// Extract and save memory for future sessions
		if deps.Memory != nil && responseText != "" {
			entry := memory.ExtractFacts(sessionID, 0, p.Message, responseText)
			if len(entry.Keywords) > 0 || len(entry.Facts) > 0 {
				deps.Memory.Append(entry)
			}
		}

		if err != nil {
			errMsg := err.Error()
			if agentCtx.Err() != nil {
				errMsg = "Request timed out (5 min limit). Partial response saved."
			}
			emit(StreamEvent{Type: "stream", Event: "error", Data: errMsg})
		}

		finalResult, _ := json.Marshal(map[string]any{
			"text":       responseText,
			"session_id": sessionID,
			"iterations": 0,
		})
		// Write as a proper Response (not StreamEvent)
		emit(StreamEvent{Type: "response", Data: string(finalResult)})
	}
}

// transcriptToMessages converts transcript events to agent.Message for context injection.
func transcriptToMessages(events []transcript.Event) []agent.Message {
	var msgs []agent.Message
	for _, ev := range events {
		switch ev.Type {
		case transcript.EventUserMessage:
			if ev.Content != "" {
				msgs = append(msgs, agent.Message{Role: "user", Content: ev.Content})
			}
		case transcript.EventAssistantText:
			if ev.Content != "" {
				msgs = append(msgs, agent.Message{Role: "assistant", Content: ev.Content})
			}
		}
	}
	return msgs
}

func handleCancel(deps Deps) (json.RawMessage, error) {
	sess := deps.Sessions.Get(dashboardChatID)
	sess.Cancel()
	return json.Marshal(map[string]string{"status": "cancelled"})
}

// toolSummary extracts a short label from tool name + input, max 15 chars in parentheses.
// e.g. Bash(cd stock_deb) or Read(main.go) or WebSearch(crewai)
func toolSummary(name, input string) string {
	snippet := extractSnippet(name, input)
	if snippet == "" {
		return name
	}
	if len([]rune(snippet)) > 15 {
		snippet = string([]rune(snippet)[:15])
	}
	return name + "(" + snippet + ")"
}

func extractSnippet(name, input string) string {
	var m map[string]any
	if json.Unmarshal([]byte(input), &m) != nil {
		return ""
	}
	// Try common fields in priority order
	for _, key := range []string{"command", "path", "file_path", "url", "query", "pattern", "script", "expression", "name", "ref", "text", "message", "prompt", "description", "glob", "regex"} {
		if v, ok := m[key]; ok {
			s := fmt.Sprintf("%v", v)
			if s != "" {
				return s
			}
		}
	}
	return ""
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

	sessionID := p.SessionID
	if sessionID == "" {
		sessionID = fmt.Sprintf("chat_%d", dashboardChatID)
	}

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
	tw.Append(sessionID, transcript.Event{
		Type: transcript.EventUserMessage, Timestamp: now,
		SessionID: sessionID, Content: p.Message,
	})
	tw.Append(sessionID, transcript.Event{
		Type: transcript.EventAssistantText, Timestamp: now,
		SessionID: sessionID, Content: result.Text,
	})
	deps.Sessions.MarkDirty()

	return json.Marshal(map[string]any{
		"text":       result.Text,
		"session_id": sessionID,
		"iterations": result.Iterations,
		"usage":      result.Usage,
	})
}
