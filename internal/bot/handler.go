package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/ngocp/goterm-control/internal/agent"
	"github.com/ngocp/goterm-control/internal/chat"
	"github.com/ngocp/goterm-control/internal/config"
	"github.com/ngocp/goterm-control/internal/coord"
	"github.com/ngocp/goterm-control/internal/execution"
	"github.com/ngocp/goterm-control/internal/memory"
	"github.com/ngocp/goterm-control/internal/models"
	"github.com/ngocp/goterm-control/internal/msgqueue"
	"github.com/ngocp/goterm-control/internal/session"
	"github.com/ngocp/goterm-control/internal/titler"
	"github.com/ngocp/goterm-control/internal/tools"
	"github.com/ngocp/goterm-control/internal/trace"
	"github.com/ngocp/goterm-control/internal/transcript"
)

// MessageStore is an optional interface for persisting conversation messages to SQLite.
type MessageStore interface {
	Append(sessionID string, msg agent.Message) error
	LoadHistory(sessionID string, limit int) ([]agent.Message, error)
}

// Handler processes Telegram updates.
type Handler struct {
	bot        *tgbotapi.BotAPI
	sessions   *session.Manager
	llm        chat.Client // claude or codex CLI, chosen by cfg.Provider
	cfg        *config.Config
	engine     *execution.Engine
	transcript *transcript.Writer
	messages   MessageStore // optional SQLite message store
	resolver   *models.Resolver
	queue      *msgqueue.Queue // debounce + collect layer
	indicator  *NameIndicator
	typing     *TypingIndicator
	memory     *memory.Manager // markdown persistent memory (nil-safe)
	titler     *titler.Titler  // async session auto-naming (nil-safe)
	trace      *trace.Recorder // run/trace recorder (nil-safe)
	agentID    string          // identity in the shared coordination database

	// approvalRequests maps callbackData → channel to signal approval/cancel
	approvalMu       sync.Mutex
	approvalRequests map[string]chan bool
}

func NewHandler(
	bot *tgbotapi.BotAPI,
	sessions *session.Manager,
	llm chat.Client,
	cfg *config.Config,
	engine *execution.Engine,
	transcriptWriter *transcript.Writer,
	messages MessageStore,
	resolver *models.Resolver,
	queue *msgqueue.Queue,
	mem *memory.Manager,
	rec *trace.Recorder,
	agentID string,
) *Handler {
	return &Handler{
		bot:              bot,
		sessions:         sessions,
		llm:              llm,
		cfg:              cfg,
		engine:           engine,
		transcript:       transcriptWriter,
		messages:         messages,
		resolver:         resolver,
		queue:            queue,
		memory:           mem,
		trace:            rec,
		agentID:          agentID,
		approvalRequests: make(map[string]chan bool),
	}
}

// Handle routes a Telegram update.
func (h *Handler) Handle(update tgbotapi.Update) {
	if update.CallbackQuery != nil {
		h.handleCallback(update.CallbackQuery)
		return
	}
	if update.Message == nil {
		return
	}

	msg := update.Message

	// Auth check
	if !h.cfg.Security.IsAllowed(msg.From.ID) {
		h.sendText(msg.Chat.ID, "⛔ Access denied.")
		return
	}

	// Command routing
	if msg.IsCommand() {
		h.handleCommand(msg)
		return
	}

	// Allow "status" / "cancel" as plain text (no "/" prefix) so they
	// work even when the agent is busy and the queue would block them.
	switch strings.ToLower(strings.TrimSpace(msg.Text)) {
	case "status":
		h.showStatus(msg.Chat.ID)
		return
	case "cancel":
		sess := h.sessions.Get(msg.Chat.ID)
		sess.Cancel()
		h.sendText(msg.Chat.ID, "🛑 Request cancelled.")
		return
	}

	// Media (photos, documents, audio, voice, video) → download into the
	// workspace and hand the saved file path(s) to Claude, which reads them
	// via its Read tool. Any caption travels along as the prompt.
	if hasMedia(msg) {
		h.handleMedia(msg)
		return
	}

	// Regular message → Claude (via execution queue)
	if msg.Text != "" {
		h.handleMessage(msg)
	}
}

func (h *Handler) handleCommand(msg *tgbotapi.Message) {
	chatID := msg.Chat.ID

	switch msg.Command() {
	case "start":
		h.sendText(chatID,
			"👋 *GoTerm Control*\n\n"+
				"I'm your Mac AI assistant powered by Claude.\n\n"+
				"Commands:\n"+
				"• /sessions — list & switch sessions\n"+
				"• /new — start a new session\n"+
				"• /reset — clear current session\n"+
				"• /status — show session info\n"+
				"• /models — list available models\n"+
				"• /model `<name>` — switch model\n"+
				"• /memory — show persistent memory\n"+
				"• /remember `<fact>` — save a note to memory\n"+
				"• /cancel — cancel current request\n\n"+
				"📎 Send me a photo or file (with an optional caption) and I'll read and process it.\n\n"+
				"Just send me any message and I'll help you control your Mac!",
		)

	case "reset":
		if h.willFlush(chatID) {
			h.sendText(chatID, "💾 Saving memory...")
		}
		h.flushMemory(chatID, "reset")
		h.sessions.Reset(chatID)
		h.resolver.ClearOverride(chatID)
		h.sendText(chatID, "🔄 Conversation history cleared.")

	case "sessions", "session":
		h.showSessionList(chatID)

	case "new":
		h.handleNewSession(chatID)

	case "status":
		h.showStatus(chatID)

	case "models":
		h.handleModelsCommand(chatID)

	case "model":
		h.handleModelCommand(chatID, msg.CommandArguments())

	case "memory":
		h.showMemory(chatID)

	case "remember":
		h.handleRemember(chatID, msg.CommandArguments())

	case "cancel":
		sess := h.sessions.Get(chatID)
		sess.Cancel()
		h.sendText(chatID, "🛑 Request cancelled.")

	default:
		// Unknown slash commands are forwarded to Claude as-is.
		// This lets users invoke custom Claude Code skills like /retro,
		// /vops:plan, etc. defined in .claude/commands/.
		h.handleMessage(msg)
	}
}

func (h *Handler) showStatus(chatID int64) {
	sess := h.sessions.Get(chatID)
	sessionID := sess.GetSessionID()
	if sessionID == "" {
		sessionID = "none"
	} else if len(sessionID) > 8 {
		sessionID = sessionID[:8] + "..."
	}
	m := h.resolver.Resolve(chatID)
	modelName := "unknown"
	if m != nil {
		modelName = m.ID
	}
	label := sess.GetLabel()
	if label == "" {
		label = sess.ID
	}
	sessionCount := len(h.sessions.ListForChat(chatID))

	var sb strings.Builder
	sb.WriteString("📊 *Session Status*\n\n")

	// Live run line — shows what the agent is doing right now so a long task
	// doesn't look like a frozen bot. Mirrors openclaw's taskLine pattern.
	info := sess.RunInfo()
	collected := 0
	if h.queue != nil {
		collected = h.queue.PendingCount(chatID)
	}
	queueDepth := h.engine.QueueDepth(chatID)

	switch {
	case info.Running:
		elapsed := time.Since(info.StartedAt).Truncate(time.Second)
		sb.WriteString(fmt.Sprintf("🔄 *Running* · %s · tools: %d", elapsed, info.ToolCount))
		if info.LastTool != "" {
			sb.WriteString(fmt.Sprintf("\nLast tool: `%s`", info.LastTool))
			if !info.LastToolAt.IsZero() {
				sb.WriteString(fmt.Sprintf(" (%s ago)", time.Since(info.LastToolAt).Truncate(time.Second)))
			}
		}
		if info.CurrentTask != "" {
			sb.WriteString(fmt.Sprintf("\nTask: _%s_", info.CurrentTask))
		}
		if collected > 0 || queueDepth > 0 {
			sb.WriteString(fmt.Sprintf("\nWaiting: %d collected · %d queued", collected, queueDepth))
		}
		sb.WriteString("\n\n")
	case collected > 0 || queueDepth > 0:
		sb.WriteString(fmt.Sprintf("⏳ *Pending* · %d collected · %d queued\n\n", collected, queueDepth))
	default:
		sb.WriteString("✅ *Idle*\n\n")
	}

	sb.WriteString(fmt.Sprintf(
		"Chat ID: `%d`\n"+
			"Active: %s\n"+
			"Sessions: %d total\n"+
			"Turns: %d\n"+
			"Claude: `%s`\n"+
			"Model: `%s`\n"+
			"Tokens: %d in / %d out",
		chatID, label, sessionCount,
		sess.GetMessageCount(), sessionID, modelName,
		sess.InputTokens, sess.OutputTokens,
	))

	h.sendText(chatID, sb.String())
}

func (h *Handler) handleModelsCommand(chatID int64) {
	active := h.resolver.Resolve(chatID)
	all := h.resolver.List()

	var sb strings.Builder
	sb.WriteString("🤖 *Available Models*\n\n")

	for i := range all {
		m := &all[i]
		isActive := active != nil && m.ID == active.ID
		sb.WriteString(models.FormatModelInfo(m, isActive))
		sb.WriteString("\n\n")
	}

	sb.WriteString("Switch: `/model <name or alias>`\n")
	sb.WriteString("Reset to default: `/model default`")

	h.sendText(chatID, sb.String())
}

func (h *Handler) handleModelCommand(chatID int64, arg string) {
	arg = strings.TrimSpace(arg)

	if arg == "" {
		// Show current model
		m := h.resolver.Resolve(chatID)
		if m != nil {
			h.sendText(chatID, fmt.Sprintf("Current model: `%s` (%s)\n\nUse `/model <name>` to switch.", m.ID, m.Name))
		}
		return
	}

	if arg == "default" || arg == "reset" {
		// Flush with the outgoing model — the old session is model-bound.
		oldModel := h.currentModelID(chatID)
		if h.willFlush(chatID) {
			h.sendText(chatID, "💾 Saving memory...")
		}
		h.flushMemoryWithModel(chatID, "model-switch", oldModel)
		h.resolver.ClearOverride(chatID)
		// Reset session so new model takes effect cleanly
		h.sessions.Reset(chatID)
		m := h.resolver.Resolve(chatID)
		name := "default"
		if m != nil {
			name = m.ID
		}
		h.sendText(chatID, fmt.Sprintf("🔄 Reset to default model: `%s`\nSession cleared for clean start.", name))
		return
	}

	// Capture the outgoing model before the override changes — the flush must
	// resume the old session with the model it was created with.
	oldModel := h.currentModelID(chatID)

	m, err := h.resolver.SetOverride(chatID, arg)
	if err != nil {
		h.sendText(chatID, fmt.Sprintf("❌ %v", err))
		return
	}

	if h.willFlush(chatID) {
		h.sendText(chatID, "💾 Saving memory...")
	}
	h.flushMemoryWithModel(chatID, "model-switch", oldModel)

	// Reset session so the new model starts fresh (Claude CLI sessions are model-bound)
	h.sessions.Reset(chatID)
	h.sendText(chatID, fmt.Sprintf(
		"✅ Switched to `%s` (%s)\n"+
			"Context: %dk tokens · Cost: $%.1f/$%.1f per 1M\n"+
			"Session cleared for clean start.",
		m.ID, m.Name, m.ContextWindow/1000, m.Cost.Input, m.Cost.Output,
	))
}

// showMemory renders persistent-memory stats and a MEMORY.md preview.
func (h *Handler) showMemory(chatID int64) {
	if !h.memory.Enabled() {
		h.sendText(chatID, "💾 Memory is disabled (set `memory.enabled: true` in config).")
		return
	}
	st, err := h.memory.Stats(time.Now())
	if err != nil {
		h.sendText(chatID, fmt.Sprintf("❌ %v", err))
		return
	}

	var sb strings.Builder
	sb.WriteString("💾 *Persistent Memory*\n\n")
	sb.WriteString(fmt.Sprintf(
		"Dir: `%s`\n"+
			"MEMORY.md: %d bytes\n"+
			"Daily notes: %d files (today: %d bytes)\n",
		st.Dir, st.MemoryMDBytes, st.DailyNoteCount, st.TodayBytes,
	))
	if st.MemoryMDPreview != "" {
		sb.WriteString("\n*MEMORY.md preview:*\n")
		sb.WriteString(st.MemoryMDPreview)
	}
	h.sendText(chatID, sb.String())
}

// handleRemember appends a fact to today's daily note directly — no Claude
// turn needed, so it is instant and free.
func (h *Handler) handleRemember(chatID int64, arg string) {
	if !h.memory.Enabled() {
		h.sendText(chatID, "💾 Memory is disabled (set `memory.enabled: true` in config).")
		return
	}
	fact := strings.TrimSpace(arg)
	if fact == "" {
		h.sendText(chatID, "Usage: `/remember <fact>` — e.g. `/remember deploy freeze until Friday`")
		return
	}
	now := time.Now()
	if err := h.memory.AppendNote(now, fact); err != nil {
		h.sendText(chatID, fmt.Sprintf("❌ %v", err))
		return
	}
	h.sendText(chatID, fmt.Sprintf("📝 Noted in `memory/%s.md`", now.Format("2006-01-02")))
}

// --- Memory flush (openclaw pre-compaction pattern) ---

// willFlush reports whether a memory flush would actually run for this chat,
// so callers can show a "saving..." notice before a blocking flush.
func (h *Handler) willFlush(chatID int64) bool {
	if !h.memory.Enabled() {
		return false
	}
	sess := h.sessions.Get(chatID)
	return sess.GetSessionID() != "" && sess.GetMessageCount() > 0
}

// currentModelID resolves the active model for a chat ("" if unknown).
func (h *Handler) currentModelID(chatID int64) string {
	if m := h.resolver.Resolve(chatID); m != nil {
		return m.ID
	}
	return ""
}

// flushMemory runs a silent turn on the active session prompting the agent to
// write durable notes to memory files before the session context is lost.
// It blocks until the flush finishes; errors are logged and swallowed —
// losing a flush must never block the user's reset.
func (h *Handler) flushMemory(chatID int64, reason string) {
	h.flushMemoryWithModel(chatID, reason, h.currentModelID(chatID))
}

// flushMemoryWithModel is flushMemory with an explicit model — used by
// /model, where the flush must resume the old session with its old model.
func (h *Handler) flushMemoryWithModel(chatID int64, reason, modelID string) {
	if !h.memory.Enabled() {
		return
	}
	sess := h.sessions.Get(chatID)
	if sess.GetSessionID() == "" || sess.GetMessageCount() == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), h.memory.FlushTimeout())
	defer cancel()

	log.Printf("memory: flush start (chat=%d reason=%s)", chatID, reason)
	start := time.Now()
	_, err := h.engine.Enqueue(ctx, chatID, func(ctx context.Context) (*execution.RunResult, error) {
		return h.runFlush(ctx, sess, modelID)
	})
	if err != nil {
		log.Printf("memory: flush failed (chat=%d reason=%s): %v", chatID, reason, err)
		return
	}
	log.Printf("memory: flush done (chat=%d reason=%s took=%s)",
		chatID, reason, time.Since(start).Truncate(time.Millisecond))
}

// runFlush executes the silent flush turn. Nothing is streamed to Telegram;
// the agent's reply (normally the NO_REPLY sentinel) is only logged.
func (h *Handler) runFlush(ctx context.Context, sess *session.Session, modelID string) (*execution.RunResult, error) {
	sess.MarkRunning("memory flush")
	defer sess.MarkIdle()

	var reply strings.Builder
	cb := chat.StreamCallbacks{
		OnText: func(chunk string) { reply.WriteString(chunk) },
		OnToolCall: func(name, inputJSON string) {
			sess.NoteTool(name)
			log.Printf("memory: flush tool %s", toolLabel(name, inputJSON))
		},
	}

	result := &execution.RunResult{
		SessionID: sess.ID,
		StartedAt: time.Now(),
		Status:    execution.RunSuccess,
	}
	err := h.llm.SendMessage(ctx, sess, modelID, h.memory.FlushPrompt(time.Now()), "", cb)
	result.EndedAt = time.Now()
	if err != nil {
		result.Status = execution.RunFailed
		result.Error = err
		return result, err
	}
	if h.memory.IsNoReply(reply.String()) {
		log.Printf("memory: flush ok (NO_REPLY)")
	} else {
		log.Printf("memory: flush replied with text (len=%d)", len(reply.String()))
	}
	return result, nil
}

func (h *Handler) handleMessage(msg *tgbotapi.Message) {
	h.queue.Submit(msg.Chat.ID, msg.Text)
}

// executeMessage is the Queue callback that runs the full Claude pipeline.
// Called by the queue after debouncing and collection.
func (h *Handler) executeMessage(chatID int64, text string) {
	h.indicator.Start()
	h.typing.Start(chatID)
	defer h.indicator.Done()
	defer h.typing.Stop(chatID)

	sess := h.sessions.Get(chatID)

	// Cancel any in-flight request for this session
	sess.Cancel()

	// Daily/idle session rotation (openclaw pattern): flush memory, then
	// reset so this message starts a fresh CLI session with re-injected
	// context. Recent-history injection keeps the rotation invisible.
	if sess.GetSessionID() != "" {
		idle := time.Duration(h.cfg.Session.Reset.IdleMinutes) * time.Minute
		if rotate, reason := memory.ShouldRotate(sess.LastActivity(), time.Now(), h.cfg.Session.Reset.DailyAt, idle); rotate {
			log.Printf("session: rotating (chat=%d reason=%s)", chatID, reason)
			h.flushMemory(chatID, reason)
			h.sessions.Reset(chatID)
		}
	}

	// Track live run state so /status can report what the agent is doing.
	sess.MarkRunning(truncateLabel(text, 60))
	defer sess.MarkIdle()

	// Configurable timeout prevents a stuck Claude CLI from blocking the queue
	// lane forever. The user can still /cancel manually for shorter waits.
	execTimeout := time.Duration(h.cfg.Claude.ExecutionTimeout) * time.Minute
	ctx, cancel := context.WithTimeout(context.Background(), execTimeout)
	sess.SetCancel(cancel)

	resolvedModel := h.resolver.Resolve(chatID)
	modelID := ""
	if resolvedModel != nil {
		modelID = resolvedModel.ID
	}

	placeholder := h.sendText(chatID, "⏳ _Thinking..._")
	if placeholder == 0 {
		cancel()
		sess.SetCancel(nil)
		return
	}

	// Inject persistent memory (MEMORY.md + recent daily notes) and recent
	// conversation history when starting a brand-new session (first message
	// or after a reset) so Claude has durable facts plus short-term context.
	newSessionContext := ""
	if sess.GetSessionID() == "" {
		newSessionContext = h.memory.BuildContext(time.Now()) + h.buildHistoryContext(sess.ID, 8)
	}

	_, err := h.engine.Enqueue(ctx, chatID, func(ctx context.Context) (*execution.RunResult, error) {
		return h.runClaude(ctx, sess, chatID, modelID, text, newSessionContext, placeholder)
	})

	if err != nil {
		log.Printf("handler: enqueue error: %v", err)
		return
	}

	// Token-threshold memory flush: once the session context grows past the
	// soft threshold, persist durable notes early. Fires at most once per
	// session (flag cleared on reset); does not reset the session itself.
	if h.memory.Enabled() {
		if th := h.memory.SoftThresholdTokens(); th > 0 &&
			sess.LastContextTokens() >= th && !sess.IsMemoryFlushed() {
			sess.MarkMemoryFlushed()
			h.flushMemory(chatID, "token-threshold")
		}
	}
}

// runClaude executes a single Claude CLI call with streaming, transcript recording, and memory extraction.
func (h *Handler) runClaude(ctx context.Context, sess *session.Session, chatID int64, modelID, userText, memoryContext string, placeholderMsgID int) (*execution.RunResult, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	result := &execution.RunResult{
		SessionID: sess.ID,
		StartedAt: time.Now(),
		Status:    execution.RunSuccess,
	}

	streamer := NewStreamer(h.bot, chatID, placeholderMsgID)

	// Open the trace for this turn. Every model call and every tool the model
	// runs hangs off this root, so the admin page can replay the turn as a
	// waterfall. Recording is nil-safe and never fails the turn.
	turnSpan := h.trace.StartTrace("turn", coord.RunTypeChain, trace.Meta{
		SessionID: sess.ID,
		ChatID:    chatID,
		Model:     modelID,
		Provider:  h.llm.Name(),
	})
	turnSpan.SetInputs(userText)

	// Collect transcript events
	var events []transcript.Event
	var eventsMu sync.Mutex
	addEvent := func(ev transcript.Event) {
		ev.Timestamp = time.Now()
		ev.SessionID = sess.ID
		ev.ChatID = chatID
		eventsMu.Lock()
		events = append(events, ev)
		eventsMu.Unlock()
	}

	// Persist the user message immediately (transcript + SQLite) so it
	// survives a crash mid-run instead of waiting for the reply to finish.
	if h.transcript != nil {
		err := h.transcript.Append(sess.ID, transcript.Event{
			Type: transcript.EventUserMessage, Timestamp: time.Now(),
			SessionID: sess.ID, ChatID: chatID, Content: userText,
		})
		if err != nil {
			log.Printf("handler: transcript write error: %v", err)
		}
	}
	if h.messages != nil {
		if err := h.messages.Append(sess.ID, agent.Message{Role: "user", Content: userText}); err != nil {
			log.Printf("handler: message store user error: %v", err)
		}
	}

	// assistantText accumulates ALL turns (for storage); turnText resets per
	// iteration so the auto-continue heuristic only inspects the latest turn.
	var assistantText strings.Builder
	var turnText strings.Builder
	var textMu sync.Mutex

	// Close the turn's trace on every exit path, including the early returns
	// for cancellation and timeout.
	defer func() {
		var turnErr error
		switch result.Status {
		case execution.RunFailed:
			turnErr = result.Error
			if turnErr == nil {
				turnErr = fmt.Errorf("run failed")
			}
		case execution.RunCanceled:
			turnErr = fmt.Errorf("canceled or timed out")
		}
		textMu.Lock()
		out := assistantText.String()
		textMu.Unlock()
		turnSpan.End(out, turnErr)
	}()

	// Snapshot the in-progress reply every 10s so a dashboard reload mid-run
	// shows how far the bot has gotten (and a crash keeps the last prefix).
	stopPartial := h.startPartialSaver(sess.ID, chatID, func() string {
		textMu.Lock()
		defer textMu.Unlock()
		return assistantText.String()
	})
	defer stopPartial()

	// llmSpan is the span for the model call currently in flight; tool spans
	// hang off it. It is replaced on each auto-continue iteration, so both it
	// and the open tool spans live behind one mutex.
	var spanMu sync.Mutex
	var llmSpan *trace.Span
	openTools := map[string][]*trace.Span{}

	// pushTool/popTool pair a tool call with its result. The callbacks carry
	// only the tool name, not the CLI's own call id, so pairing is FIFO per
	// name — correct for these CLIs, which run tools one at a time.
	pushTool := func(name, inputJSON string) {
		spanMu.Lock()
		defer spanMu.Unlock()
		sp := llmSpan.Child(name, coord.RunTypeTool)
		if sp == nil {
			return
		}
		sp.SetInputs(inputJSON)
		openTools[name] = append(openTools[name], sp)
	}
	popTool := func(name string, res tools.ToolResult) {
		spanMu.Lock()
		defer spanMu.Unlock()
		queue := openTools[name]
		if len(queue) == 0 {
			return // a result with no matching call: nothing to close
		}
		sp := queue[0]
		openTools[name] = queue[1:]
		var err error
		if res.IsError {
			err = fmt.Errorf("%s failed", name)
		}
		sp.End(res.Output, err)
	}

	// latestTodos holds the most recent TodoWrite snapshot from the model.
	// Updated under textMu since OnToolCall and the loop body that reads it
	// are technically separate happens-before edges (the CLI scanner goroutine
	// has fully exited by the time we read between SendMessage calls, but we
	// still take the lock for consistency with the other shared state).
	var latestTodos []todoItem

	cb := chat.StreamCallbacks{
		OnText: func(chunk string) {
			textMu.Lock()
			assistantText.WriteString(chunk)
			turnText.WriteString(chunk)
			textMu.Unlock()
			streamer.Write(chunk)
		},
		OnToolCall: func(name string, inputJSON string) {
			addEvent(transcript.Event{Type: transcript.EventToolCall, ToolName: name, ToolInput: inputJSON})
			pushTool(name, inputJSON)
			label := toolLabel(name, inputJSON)
			// Compact tool progress with short snippet: Bash(cd stock_d) → Read(main.go)
			streamer.NoteTool(label)
			// Mirror to session so /status can show what's running right now.
			sess.NoteTool(label)
			// Snapshot the latest TodoWrite state for the auto-continue check.
			if name == "TodoWrite" {
				if todos := parseTodoWriteInput(inputJSON); todos != nil {
					textMu.Lock()
					latestTodos = todos
					textMu.Unlock()
				}
			}
		},
		OnToolResult: func(name string, toolResult tools.ToolResult) {
			// Log to transcript only — tool results not shown to user
			addEvent(transcript.Event{Type: transcript.EventToolResult, ToolName: name, Content: toolResult.Output, IsError: toolResult.IsError})
			popTool(name, toolResult)

			// Exception: screenshots still sent as photos
			if toolResult.IsImage {
				streamer.Flush()
				path := toolResult.ImagePath
				for i := 0; i < 6; i++ {
					if _, err := os.Stat(path); err == nil {
						break
					}
					time.Sleep(500 * time.Millisecond)
				}
				if _, err := os.Stat(path); err == nil {
					streamer.SendPhoto(path, "📸 Screenshot")
				}
			}
		},
	}

	currentText := userText
	currentMemory := memoryContext
	attempts := 0
	for {
		textMu.Lock()
		turnText.Reset()
		textMu.Unlock()

		spanMu.Lock()
		llmSpan = turnSpan.Child(h.llm.Name(), coord.RunTypeLLM)
		call := llmSpan
		spanMu.Unlock()
		call.SetInputs(currentText)
		// The CLI clients report usage by adding to the session counters, so
		// this call's own usage is the delta across SendMessage.
		inBefore, outBefore := sess.Tokens()

		sendErr := h.llm.SendMessage(ctx, sess, modelID, currentText, currentMemory, cb)

		textMu.Lock()
		reply := turnText.String()
		textMu.Unlock()
		inAfter, outAfter := sess.Tokens()
		call.EndWithTokens(reply, sendErr, inAfter-inBefore, outAfter-outBefore)

		if err := sendErr; err != nil {
			if ctx.Err() != nil {
				if ctx.Err() == context.DeadlineExceeded {
					streamer.Write("\n\n⏰ Task timed out.")
				}
				result.Status = execution.RunCanceled
				streamer.Finalize()
				return result, nil
			}
			log.Printf("%s error: %v", h.llm.Name(), err)
			streamer.Write(fmt.Sprintf("\n\n❌ Error: %v", err))
			result.Status = execution.RunFailed
			result.Error = err
			break
		}

		if attempts >= maxAutoContinue {
			break
		}

		textMu.Lock()
		pending := pendingTodos(latestTodos)
		lastTurn := turnText.String()
		textMu.Unlock()

		if !shouldAutoContinue(lastTurn, pending) {
			break
		}

		// Visible marker so the user sees a continuation rather than a
		// silent extra round of tool calls.
		marker := fmt.Sprintf("\n\n_…auto-continue (%d todo còn lại)…_\n\n", len(pending))
		streamer.Write(marker)
		textMu.Lock()
		assistantText.WriteString(marker)
		textMu.Unlock()
		addEvent(transcript.Event{
			Type:    transcript.EventUserMessage,
			Content: fmt.Sprintf("[auto-continue %d/%d, %d pending] %s", attempts+1, maxAutoContinue, len(pending), strings.Join(pending, " | ")),
		})

		currentText = autoContinuePrompt
		currentMemory = "" // memory injection is first-call only
		attempts++
	}

	streamer.Finalize()

	// Stop the partial saver before writing the final assistant_text so no
	// partial snapshot lands after the event that supersedes it.
	stopPartial()

	// Record assistant response
	respText := assistantText.String()
	if respText != "" {
		addEvent(transcript.Event{Type: transcript.EventAssistantText, Content: respText})
	}

	// Flush transcript to disk (JSONL audit trail)
	if h.transcript != nil {
		if err := h.transcript.AppendAll(sess.ID, events); err != nil {
			log.Printf("handler: transcript write error: %v", err)
		}
	}

	// Persist assistant response to SQLite (the user message was already
	// stored at the start of the run).
	if h.messages != nil && respText != "" {
		if err := h.messages.Append(sess.ID, agent.Message{Role: "assistant", Content: respText}); err != nil {
			log.Printf("handler: message store assistant error: %v", err)
		}
	}

	// Label the session: instant fallback from the first user message, then
	// an async rename to a summary of the whole conversation.
	if sess.GetLabel() == "" && userText != "" {
		sess.SetLabel(truncateLabel(userText, 40))
	}
	h.refreshSessionLabel(sess)

	// Mark session dirty for persistence
	h.sessions.MarkDirty()

	result.EndedAt = time.Now()
	return result, nil
}

// partialSaveInterval is how often an in-progress reply is snapshotted to
// the transcript so reloads mid-run show current progress.
const partialSaveInterval = 10 * time.Second

// startPartialSaver periodically appends an assistant_partial snapshot of
// the streaming reply to the transcript. snapshot must be safe to call from
// another goroutine. The returned stop function is idempotent and must be
// called before the final assistant_text is written.
func (h *Handler) startPartialSaver(sessionID string, chatID int64, snapshot func() string) func() {
	if h.transcript == nil {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(partialSaveInterval)
		defer ticker.Stop()
		lastLen := 0
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				snap := snapshot()
				if snap == "" || len(snap) == lastLen {
					continue
				}
				lastLen = len(snap)
				err := h.transcript.Append(sessionID, transcript.Event{
					Type: transcript.EventAssistantPartial, Timestamp: time.Now(),
					SessionID: sessionID, ChatID: chatID, Content: snap,
				})
				if err != nil {
					log.Printf("handler: partial save error: %v", err)
				}
			}
		}
	}()
	return sync.OnceFunc(func() { close(done) })
}

// refreshSessionLabel regenerates the session label as a short summary of
// recent conversation content. Runs async; keeps the old label on failure.
func (h *Handler) refreshSessionLabel(sess *session.Session) {
	if h.titler == nil || h.messages == nil {
		return
	}
	msgs, err := h.messages.LoadHistory(sess.ID, 8)
	if err != nil || len(msgs) == 0 {
		return
	}
	h.titler.Refresh(sess.ID, msgs, func(title string) {
		sess.SetLabel(title)
		h.sessions.MarkDirty()
		log.Printf("titler: session %s renamed to %q", sess.ID, title)
	})
}

func (h *Handler) handleCallback(cb *tgbotapi.CallbackQuery) {
	data := cb.Data
	chatID := cb.Message.Chat.ID

	answer := tgbotapi.NewCallback(cb.ID, "")
	_, _ = h.bot.Request(answer)

	// Session selection callback
	if strings.HasPrefix(data, "sess:") {
		sessionID := strings.TrimPrefix(data, "sess:")
		h.handleSessionSwitch(chatID, sessionID, cb.Message.MessageID)
		return
	}

	// Approval callback
	h.approvalMu.Lock()
	ch, ok := h.approvalRequests[data]
	if ok {
		delete(h.approvalRequests, data)
	}
	h.approvalMu.Unlock()

	if !ok {
		edit := tgbotapi.NewEditMessageText(chatID, cb.Message.MessageID, "<i>(expired)</i>")
		edit.ParseMode = "HTML"
		_, _ = h.bot.Send(edit)
		return
	}

	approved := strings.HasSuffix(data, ":approve")
	ch <- approved

	label := "✅ Approved"
	if !approved {
		label = "❌ Cancelled"
	}
	edit := tgbotapi.NewEditMessageText(chatID, cb.Message.MessageID, label)
	_, _ = h.bot.Send(edit)
}

// --- Session management ---

func (h *Handler) showSessionList(chatID int64) {
	sessions := h.sessions.ListForChat(chatID)
	if len(sessions) == 0 {
		h.sendText(chatID, "No sessions yet. Send a message to start.")
		return
	}

	activeID := h.sessions.ActiveSessionID(chatID)

	var rows [][]tgbotapi.InlineKeyboardButton
	for _, s := range sessions {
		label := s.GetLabel()
		if label == "" {
			label = fmt.Sprintf("Session %d", s.Seq)
		}

		btnText := fmt.Sprintf("%s (%d msgs)", label, s.GetMessageCount())
		if s.ID == activeID {
			btnText = "✅ " + btnText
		}

		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(btnText, "sess:"+s.ID),
		))
	}

	keyboard := tgbotapi.NewInlineKeyboardMarkup(rows...)
	msg := tgbotapi.NewMessage(chatID, "📋 *Sessions* — tap to switch:")
	msg.ParseMode = "Markdown"
	msg.ReplyMarkup = keyboard
	h.bot.Send(msg)
}

func (h *Handler) handleNewSession(chatID int64) {
	// Flush the outgoing session's memory before switching to a fresh one.
	if h.willFlush(chatID) {
		h.sendText(chatID, "💾 Saving memory...")
	}
	h.flushMemory(chatID, "new-session")
	sess, err := h.sessions.NewSession(chatID)
	if err != nil {
		h.sendText(chatID, fmt.Sprintf("❌ %v", err))
		return
	}
	h.sendText(chatID, fmt.Sprintf("✨ New session created (Session %d). Send a message to start.", sess.Seq))
}

func (h *Handler) handleSessionSwitch(chatID int64, sessionID string, msgID int) {
	if err := h.sessions.SwitchActive(chatID, sessionID); err != nil {
		h.sendText(chatID, fmt.Sprintf("❌ %v", err))
		return
	}
	sess := h.sessions.Get(chatID)
	label := sess.GetLabel()
	if label == "" {
		label = fmt.Sprintf("Session %d", sess.Seq)
	}

	text := fmt.Sprintf("Switched to: *%s* (%d messages)", label, sess.GetMessageCount())
	edit := tgbotapi.NewEditMessageText(chatID, msgID, text)
	edit.ParseMode = "Markdown"
	h.bot.Send(edit)
}

// truncateLabel returns first line of text, truncated to maxRunes.
func truncateLabel(text string, maxRunes int) string {
	if idx := strings.IndexByte(text, '\n'); idx >= 0 {
		text = text[:idx]
	}
	text = strings.TrimSpace(text)
	r := []rune(text)
	if len(r) > maxRunes {
		return string(r[:maxRunes]) + "..."
	}
	return text
}

// toolLabel creates a short label like Bash(cd stock_d) or Read(bot/handler.go)
func toolLabel(name, inputJSON string) string {
	var m map[string]any
	if json.Unmarshal([]byte(inputJSON), &m) != nil {
		return name
	}

	// Path keys get tail-truncated (show meaningful end); others get head-truncated.
	pathKeys := map[string]bool{"path": true, "file_path": true}

	for _, key := range []string{"command", "path", "file_path", "url", "query", "pattern", "script", "expression", "name", "ref", "text", "glob", "regex"} {
		if v, ok := m[key]; ok {
			s := fmt.Sprintf("%v", v)
			if s == "" {
				continue
			}
			if pathKeys[key] {
				s = shortenPath(s, 25)
			} else if key == "command" {
				s = shortenBashCommand(s, 25)
			} else {
				r := []rune(s)
				if len(r) > 20 {
					s = string(r[:20])
				}
			}
			return name + "(" + s + ")"
		}
	}
	return name
}

// shortenBashCommand extracts the first segment of a shell command (before
// &&, ||, |, ;) and shortens any path-like argument while keeping the
// command prefix (cd, ls, grep, etc.).
//
//	"cd /Users/ngocp/Documents/projects/meClaw/goterm-control" → "cd ../goterm-control"
//	"ls -la /very/long/path/to/dir"                            → "ls ../dir"
//	"echo hello world"                                         → "echo hello world"
func shortenBashCommand(s string, maxRunes int) string {
	if len([]rune(s)) <= maxRunes {
		return s
	}

	// Take the first command segment (before &&, ||, |, ;).
	seg := s
	for _, sep := range []string{" && ", " || ", " | ", "; "} {
		if idx := strings.Index(seg, sep); idx >= 0 {
			seg = seg[:idx]
		}
	}

	// Split into tokens; find the command prefix and the first path argument.
	tokens := strings.Fields(seg)
	if len(tokens) == 0 {
		return headTruncate(s, maxRunes)
	}

	cmd := tokens[0] // e.g. "cd", "ls", "grep"
	var pathIdx int   // index of the first path-like token
	var foundPath bool
	for i := 1; i < len(tokens); i++ {
		t := tokens[i]
		if strings.HasPrefix(t, "/") || strings.HasPrefix(t, "./") ||
			strings.HasPrefix(t, "~/") || strings.HasPrefix(t, "../") {
			pathIdx = i
			foundPath = true
			break
		}
	}

	if !foundPath {
		return headTruncate(s, maxRunes)
	}

	// Budget for the path: maxRunes minus "cmd " prefix.
	prefix := cmd
	pathBudget := maxRunes - len([]rune(prefix)) - 1 // -1 for space
	if pathBudget < 6 {
		return headTruncate(s, maxRunes)
	}

	shortened := shortenPath(tokens[pathIdx], pathBudget)
	return prefix + " " + shortened
}

// headTruncate keeps the first maxRunes runes of s.
func headTruncate(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes])
}

// shortenPath keeps the last path components that fit within maxRunes,
// so "/Users/ngocp/Documents/projects/meClaw/goterm-control/internal/bot/handler.go"
// becomes "../bot/handler.go" instead of the useless "/Users/ngocp/Do".
func shortenPath(s string, maxRunes int) string {
	if len([]rune(s)) <= maxRunes {
		return s
	}
	parts := strings.Split(s, "/")
	// Build from the tail, accumulating components.
	var tail string
	for i := len(parts) - 1; i >= 0; i-- {
		candidate := parts[i]
		if tail != "" {
			candidate = parts[i] + "/" + tail
		}
		if len([]rune(candidate))+3 > maxRunes { // +3 for "../"
			break
		}
		tail = candidate
	}
	if tail == "" {
		// Filename alone exceeds budget — truncate the filename.
		r := []rune(parts[len(parts)-1])
		if len(r) > maxRunes-3 {
			tail = string(r[:maxRunes-3])
		} else {
			tail = string(r)
		}
	}
	if tail == s {
		return s
	}
	return "../" + tail
}

// sendText converts markdown to Telegram HTML and sends the message.
func (h *Handler) sendText(chatID int64, text string) int {
	html := markdownToTelegramHTML(text)
	msg := tgbotapi.NewMessage(chatID, html)
	msg.ParseMode = "HTML"
	sent, err := h.bot.Send(msg)
	if err != nil {
		msg2 := tgbotapi.NewMessage(chatID, stripHTML(html))
		sent, err = h.bot.Send(msg2)
		if err != nil {
			log.Printf("sendText: %v", err)
			return 0
		}
	}
	return sent.MessageID
}

// buildHistoryContext loads recent messages from the store and formats them
// as a conversation summary for context injection into brand-new sessions
// (first message or after explicit /reset).
func (h *Handler) buildHistoryContext(sessionID string, limit int) string {
	if h.messages == nil {
		return ""
	}
	msgs, err := h.messages.LoadHistory(sessionID, limit)
	if err != nil || len(msgs) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n\n## Recent Conversation History\n")
	sb.WriteString("(Context from previous messages in this chat — use to understand follow-up references)\n\n")

	for _, m := range msgs {
		role := "User"
		if m.Role == "assistant" {
			role = "Assistant"
		}
		content := m.Content
		r := []rune(content)
		if len(r) > 200 {
			content = string(r[:200]) + "..."
		}
		sb.WriteString(fmt.Sprintf("**%s**: %s\n", role, content))
	}

	return sb.String()
}
