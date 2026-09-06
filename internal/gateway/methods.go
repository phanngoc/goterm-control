package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ngocp/goterm-control/internal/agent"
	"github.com/ngocp/goterm-control/internal/browserbridge"
	"github.com/ngocp/goterm-control/internal/chat"
	"github.com/ngocp/goterm-control/internal/coord"
	"github.com/ngocp/goterm-control/internal/credentials"
	"github.com/ngocp/goterm-control/internal/execution"
	"github.com/ngocp/goterm-control/internal/models"
	"github.com/ngocp/goterm-control/internal/session"
	"github.com/ngocp/goterm-control/internal/storage"
	"github.com/ngocp/goterm-control/internal/trace"
	"github.com/ngocp/goterm-control/internal/transcript"
)

// Deps holds the dependencies needed by RPC method handlers.
type Deps struct {
	Sessions     *session.Manager
	Resolver     *models.Resolver
	Provider     agent.ModelProvider
	ToolExecutor agent.ToolExecutor
	Tools        []agent.ToolDef
	System       string // system prompt
	DataDir      string // data directory for transcripts
	Uptime       func() time.Duration
	Runs         func() []RunInfo // live in-flight runs (nil when no bot attached)

	// Coord is the shared coordination database (traces, tasks, inter-agent
	// messages). Nil when coordination is disabled — every admin.* method
	// then reports that rather than failing obscurely.
	Coord        *coord.DB
	AgentID      string
	AgentName    string          // display name, for the menu bar and dashboard
	Workspace    string          // this agent's workspace directory
	ProviderName string          // "claude" | "codex", for trace metadata
	Trace        *trace.Recorder // nil-safe: nil disables tracing on this path

	// PokeTasks asks the local task runner to check the queue immediately.
	// Nil when this agent does not claim tasks.
	PokeTasks func()

	// NotesFile is the markdown rendering of shared notes, rewritten whenever
	// a note is added so agents can read it with their file tools.
	NotesFile string

	// Conversations maps channels to conversation keys. Nil falls back to the
	// old fixed dashboard chat, so tests and minimal setups keep working.
	Conversations *storage.ConversationStore

	// Turn is the bot's turn engine. When set, dashboard messages run through
	// it and therefore behave exactly like Telegram messages: the CLI owns the
	// tool loop, memory is injected, the messages table is written, and the
	// trace has tool spans. Nil only when the Telegram bot failed to start —
	// the handler then falls back to the standalone agent loop and says so.
	Turn TurnRunner

	// Browser relays actions to the user's own browser through the Browser
	// Bridge extension. Nil when the bridge is disabled in config.
	Browser *browserbridge.Hub

	// Accounts is the credential pool sessions rotate across. Nil or empty
	// means the ambient credentials.
	Accounts *credentials.Pool
}

// TurnRunner is the slice of *bot.Handler the gateway needs. Declared here so
// gateway does not have to import bot.
type TurnRunner interface {
	RunTurn(ctx context.Context, sess *session.Session, chatID int64, modelID, userText string, sink TurnSink) (*execution.RunResult, error)
}

// TurnSink is the shared channel-output contract; wsSink implements it over a
// WebSocket. Must be the same named type bot uses, or *bot.Handler would not
// satisfy TurnRunner — Go matches method parameter types by identity.
type TurnSink = chat.TurnSink

// wsSink delivers a turn to a dashboard client as stream events. It is the
// dashboard's counterpart of the Telegram Streamer: same interface, different
// wire. It also keeps the assembled reply so the caller can hand it back in
// the final response frame.
type wsSink struct {
	emit func(StreamEvent)
	mu   sync.Mutex
	text strings.Builder
}

func (w *wsSink) Write(chunk string) {
	w.mu.Lock()
	w.text.WriteString(chunk)
	w.mu.Unlock()
	w.emit(StreamEvent{Type: "stream", Event: "text", Data: chunk})
}

func (w *wsSink) NoteTool(label string) {
	w.emit(StreamEvent{Type: "stream", Event: "tool", Data: label})
}

// Flush and Finalize exist for Telegram, where output is batched into message
// edits. WebSocket delivery is already immediate.
func (w *wsSink) Flush()    {}
func (w *wsSink) Finalize() {}

// SendPhoto has no dashboard equivalent yet; surface the path so the user can
// open it, instead of silently dropping the screenshot.
func (w *wsSink) SendPhoto(path, caption string) {
	w.emit(StreamEvent{Type: "stream", Event: "tool", Data: caption + ": " + path})
}

func (w *wsSink) Text() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.text.String()
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
		case "browser.call":
			return handleBrowserCall(ctx, deps, params)

		// --- admin / observability ---
		case "admin.overview":
			return handleAdminOverview(deps)
		case "traces.list":
			return handleTracesList(deps, params)
		case "traces.get":
			return handleTraceGet(deps, params)
		case "tasks.list":
			return handleTasksList(deps, params)
		case "tasks.get":
			return handleTaskGet(deps, params)
		case "tasks.create":
			return handleTaskCreate(deps, params)
		case "tasks.cancel":
			return handleTaskCancel(deps, params)
		case "messages.list":
			return handleMessagesList(deps, params)
		case "messages.send":
			return handleMessageSend(deps, params)
		case "notes.list":
			return handleNotesList(deps, params)
		case "notes.add":
			return handleNoteAdd(deps, params)
		case "tasks.poke":
			if deps.PokeTasks != nil {
				deps.PokeTasks()
			}
			return pokeResult, nil
		default:
			return nil, fmt.Errorf("unknown method: %s", method)
		}
	}
}

// providerLabel names the model backend in a span; falls back to something
// readable when the gateway was built without a provider name.
func providerLabel(deps Deps) string {
	if deps.ProviderName != "" {
		return deps.ProviderName
	}
	return "model"
}

func handleStatus(deps Deps) (json.RawMessage, error) {
	sessions := deps.Sessions.List()
	result := StatusResult{
		Running:        true,
		AgentID:        deps.AgentID,
		AgentName:      deps.AgentName,
		Workspace:      deps.Workspace,
		Uptime:         deps.Uptime().Round(time.Second).String(),
		DefaultModel:   deps.Resolver.Default(),
		ActiveSessions: len(sessions),
		Channels:       []string{"telegram", "gateway", "dashboard"},
	}
	if deps.Runs != nil {
		result.Runs = deps.Runs()
	}
	if deps.Browser != nil {
		st := deps.Browser.Status()
		result.Browser = &st
	}
	result.Accounts = deps.Accounts.Status()
	return json.Marshal(result)
}

// handleBrowserCall runs one action in the user's browser through the Browser
// Bridge: params are {"action": "...", "params": {...}}, the same shape the
// `bomclaw browser` CLI posts to /api/browser/call.
func handleBrowserCall(ctx context.Context, deps Deps, params json.RawMessage) (json.RawMessage, error) {
	if deps.Browser == nil {
		return nil, fmt.Errorf("browser bridge is disabled (browser.extension.enabled)")
	}
	var p struct {
		Action string          `json:"action"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	return deps.Browser.Call(ctx, p.Action, p.Params)
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
	Label           string `json:"label,omitempty"`
	Seq             int    `json:"seq"`
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
		Label:           s.GetLabel(),
		Seq:             s.Seq,
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

	// Newest sessions first (stubs without timestamps sink to the bottom).
	sort.Slice(all, func(i, j int) bool {
		return all[i].UpdatedAt.After(all[j].UpdatedAt)
	})

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

		// With no session named, the message goes to the ACTIVE session of the
		// conversation the dashboard is bound to — normally the very session
		// Telegram is on, which is what makes the two channels one
		// conversation. A named session is used as-is; one that resolves to
		// nothing stays unresolved rather than being silently redirected into
		// the default conversation, so the message lands in the transcript
		// the caller asked for.
		var sess *session.Session
		sessionID := p.SessionID
		if sessionID == "" {
			sess = deps.Sessions.Get(webConversation(deps))
			sessionID = sess.ID
		} else {
			sess = deps.Sessions.GetByID(sessionID)
		}

		// One execution path for both channels. The turn engine persists the
		// user message, the reply, the transcript, the session counters and
		// the trace itself — the same way it does for Telegram — so this
		// handler only has to move bytes to the socket and send the final
		// response frame.
		if deps.Turn != nil && sess != nil {
			modelID := deps.Resolver.Default()
			if p.ModelID != "" {
				if m := deps.Resolver.Lookup(p.ModelID); m != nil {
					modelID = m.ID
				}
			}
			agentCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()

			sink := &wsSink{emit: emit}
			result, err := deps.Turn.RunTurn(agentCtx, sess, sess.ChatID, modelID, p.Message, sink)
			if err != nil {
				errMsg := err.Error()
				if agentCtx.Err() != nil {
					errMsg = "Request timed out (5 min limit). Partial response saved."
				}
				emit(StreamEvent{Type: "stream", Event: "error", Data: errMsg})
			} else if result != nil && result.Status == execution.RunFailed && result.Error != nil {
				emit(StreamEvent{Type: "stream", Event: "error", Data: result.Error.Error()})
			}

			finalResult, _ := json.Marshal(map[string]any{
				"text":       sink.Text(),
				"session_id": sess.ID,
				"iterations": 0,
			})
			emit(StreamEvent{Type: "response", Data: string(finalResult)})
			return
		}
		log.Printf("gateway: turn engine unavailable — running dashboard message through the standalone agent loop (no memory, no CLI tool loop)")

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

		// Track streamed text for persistence
		var streamedText strings.Builder
		var streamMu sync.Mutex

		// Snapshot the in-progress reply every 10s so a dashboard reload
		// mid-run shows current progress instead of a blank bubble.
		partialDone := make(chan struct{})
		go func() {
			ticker := time.NewTicker(10 * time.Second)
			defer ticker.Stop()
			lastLen := 0
			for {
				select {
				case <-partialDone:
					return
				case <-ticker.C:
					streamMu.Lock()
					snap := streamedText.String()
					streamMu.Unlock()
					if snap == "" || len(snap) == lastLen {
						continue
					}
					lastLen = len(snap)
					tw.Append(sessionID, transcript.Event{
						Type: transcript.EventAssistantPartial, Timestamp: time.Now(),
						SessionID: sessionID, Content: snap,
					})
				}
			}
		}()
		stopPartial := sync.OnceFunc(func() { close(partialDone) })
		defer stopPartial()

		// Trace this command. This is the path the dashboard chat box, the
		// `bomclaw send` CLI and a peer agent all take, so it is the one the
		// admin page most needs to explain.
		span := deps.Trace.StartTrace("gateway.send", coord.RunTypeChain, trace.Meta{
			SessionID: sessionID,
			Model:     modelID,
			Provider:  deps.ProviderName,
		})
		span.SetInputs(p.Message)

		// Tool callbacks carry only the tool name, so pair call with result
		// FIFO per name — the agent loop executes tools in order.
		var spanMu sync.Mutex
		var llmSpan *trace.Span
		openTools := map[string][]*trace.Span{}

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
				streamMu.Lock()
				streamedText.WriteString(text)
				streamMu.Unlock()
				emit(StreamEvent{Type: "stream", Event: "text", Data: text})
			},
			OnIteration: func(iteration int) func(string, *agent.Usage, error) {
				spanMu.Lock()
				llmSpan = span.Child(fmt.Sprintf("%s #%d", providerLabel(deps), iteration+1), coord.RunTypeLLM)
				call := llmSpan
				spanMu.Unlock()
				return func(text string, u *agent.Usage, err error) {
					in, out := 0, 0
					if u != nil {
						in, out = u.InputTokens, u.OutputTokens
					}
					call.EndWithTokens(text, err, in, out)
				}
			},
			OnToolCall: func(name, input string) {
				summary := toolSummary(name, input)
				emit(StreamEvent{Type: "stream", Event: "tool", Data: summary})

				spanMu.Lock()
				defer spanMu.Unlock()
				sp := llmSpan.Child(name, coord.RunTypeTool)
				if sp == nil {
					return
				}
				sp.SetInputs(input)
				openTools[name] = append(openTools[name], sp)
			},
			OnToolResult: func(name, content string, isErr bool) {
				spanMu.Lock()
				defer spanMu.Unlock()
				q := openTools[name]
				if len(q) == 0 {
					return
				}
				sp := q[0]
				openTools[name] = q[1:]
				var toolErr error
				if isErr {
					toolErr = fmt.Errorf("%s failed", name)
				}
				sp.End(content, toolErr)
			},
		})

		// Stop snapshotting before the final assistant_text is written.
		stopPartial()

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

		// Fallback path only: the standalone loop does not touch session
		// counters, so record the turn by hand. On the shared path above the
		// engine already did this — counting here too would double it.
		if sess != nil {
			sess.IncrementMessages()
			if result != nil {
				sess.AddTokens(result.Usage.InputTokens, result.Usage.OutputTokens)
			}
			if sess.GetLabel() == "" {
				sess.SetLabel(labelFromMessage(p.Message))
			}
			deps.Sessions.MarkDirty()
		}

		if err != nil {
			errMsg := err.Error()
			if agentCtx.Err() != nil {
				errMsg = "Request timed out (5 min limit). Partial response saved."
			}
			emit(StreamEvent{Type: "stream", Event: "error", Data: errMsg})
		}

		// The root carries no tokens of its own: each model call already
		// recorded its usage, and a trace's totals are the sum over its tree,
		// so counting them again here would double every number on screen.
		span.End(responseText, err)

		finalResult, _ := json.Marshal(map[string]any{
			"text":       responseText,
			"session_id": sessionID,
			"iterations": 0,
		})
		// Write as a proper Response (not StreamEvent)
		emit(StreamEvent{Type: "response", Data: string(finalResult)})
	}
}

// transcriptToMessages converts transcript events to agent.Message for context
// injection. Partial snapshots are superseded by the final assistant_text of
// their turn; only a trailing partial (interrupted run) is kept.
func transcriptToMessages(events []transcript.Event) []agent.Message {
	var msgs []agent.Message
	var lastPartial string
	flushPartial := func() {
		if lastPartial != "" {
			msgs = append(msgs, agent.Message{Role: "assistant", Content: lastPartial})
			lastPartial = ""
		}
	}
	for _, ev := range events {
		switch ev.Type {
		case transcript.EventUserMessage:
			flushPartial()
			if ev.Content != "" {
				msgs = append(msgs, agent.Message{Role: "user", Content: ev.Content})
			}
		case transcript.EventAssistantText:
			lastPartial = "" // final supersedes this turn's partials
			if ev.Content != "" {
				msgs = append(msgs, agent.Message{Role: "assistant", Content: ev.Content})
			}
		case transcript.EventAssistantPartial:
			if ev.Content != "" {
				lastPartial = ev.Content
			}
		}
	}
	flushPartial()
	return msgs
}

func handleCancel(deps Deps) (json.RawMessage, error) {
	sess := deps.Sessions.Get(webConversation(deps))
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

// labelFromMessage names a session after its opening message, so the sessions
// list shows something readable instead of a bare id.
func labelFromMessage(msg string) string {
	const max = 40
	label := strings.TrimSpace(strings.ReplaceAll(msg, "\n", " "))
	if label == "" {
		return ""
	}
	if r := []rune(label); len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return label
}

// dashboardChatID is the conversation the dashboard falls back to when no
// conversation store is wired (tests, or a gateway built without one). With a
// store, webConversation resolves the web account's binding instead — which,
// for a single user, is the Telegram conversation.
const dashboardChatID = storage.DashboardConversationID

// webConversation returns the conversation key the dashboard should use.
func webConversation(deps Deps) int64 {
	if deps.Conversations != nil {
		return deps.Conversations.ResolveWeb()
	}
	return dashboardChatID
}

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

	// Trace this command the same way a Telegram turn is traced, so the admin
	// page shows one timeline whether the instruction came from the dashboard,
	// the CLI, or a peer agent.
	span := deps.Trace.StartTrace("gateway.send", coord.RunTypeChain, trace.Meta{
		SessionID: sessionID,
		Model:     modelID,
		Provider:  deps.ProviderName,
	})
	span.SetInputs(p.Message)

	// Tool calls and results arrive as separate callbacks carrying only the
	// tool name, so pair them FIFO per name — the loop runs tools in order.
	var toolMu sync.Mutex
	openTools := map[string][]*trace.Span{}
	var current *trace.Span

	result, err := agent.RunAgent(ctx, agent.RunParams{
		Provider:     deps.Provider,
		ToolExecutor: deps.ToolExecutor,
		ModelID:      modelID,
		SystemPrompt: deps.System,
		UserMessage:  p.Message,
		Tools:        deps.Tools,
		MaxTokens:    maxTokens,

		OnIteration: func(iteration int) func(string, *agent.Usage, error) {
			toolMu.Lock()
			current = span.Child(fmt.Sprintf("%s #%d", deps.ProviderName, iteration+1), coord.RunTypeLLM)
			call := current
			toolMu.Unlock()
			return func(text string, u *agent.Usage, err error) {
				in, out := 0, 0
				if u != nil {
					in, out = u.InputTokens, u.OutputTokens
				}
				call.EndWithTokens(text, err, in, out)
			}
		},
		OnToolCall: func(name, input string) {
			toolMu.Lock()
			defer toolMu.Unlock()
			sp := current.Child(name, coord.RunTypeTool)
			if sp == nil {
				return
			}
			sp.SetInputs(input)
			openTools[name] = append(openTools[name], sp)
		},
		OnToolResult: func(name, content string, isErr bool) {
			toolMu.Lock()
			defer toolMu.Unlock()
			q := openTools[name]
			if len(q) == 0 {
				return
			}
			sp := q[0]
			openTools[name] = q[1:]
			var toolErr error
			if isErr {
				toolErr = fmt.Errorf("%s failed", name)
			}
			sp.End(content, toolErr)
		},
	})
	if err != nil {
		span.End("", err)
		return nil, err
	}
	// No tokens on the root — the per-call children already carry them and the
	// trace rollup sums the whole tree.
	span.End(result.Text, nil)

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
