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
	"github.com/ngocp/goterm-control/internal/claude"
	"github.com/ngocp/goterm-control/internal/config"
	"github.com/ngocp/goterm-control/internal/execution"
	"github.com/ngocp/goterm-control/internal/models"
	"github.com/ngocp/goterm-control/internal/msgqueue"
	"github.com/ngocp/goterm-control/internal/session"
	"github.com/ngocp/goterm-control/internal/tools"
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
	claude     *claude.Client
	cfg        *config.Config
	engine     *execution.Engine
	transcript *transcript.Writer
	messages   MessageStore // optional SQLite message store
	resolver   *models.Resolver
	queue      *msgqueue.Queue // debounce + collect layer
	indicator  *NameIndicator
	typing     *TypingIndicator

	// approvalRequests maps callbackData → channel to signal approval/cancel
	approvalMu       sync.Mutex
	approvalRequests map[string]chan bool
}

func NewHandler(
	bot *tgbotapi.BotAPI,
	sessions *session.Manager,
	claudeClient *claude.Client,
	cfg *config.Config,
	engine *execution.Engine,
	transcriptWriter *transcript.Writer,
	messages MessageStore,
	resolver *models.Resolver,
	queue *msgqueue.Queue,
) *Handler {
	return &Handler{
		bot:              bot,
		sessions:         sessions,
		claude:           claudeClient,
		cfg:              cfg,
		engine:           engine,
		transcript:       transcriptWriter,
		messages:         messages,
		resolver:         resolver,
		queue:            queue,
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
				"• /cancel — cancel current request\n\n"+
				"Just send me any message and I'll help you control your Mac!",
		)

	case "reset":
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

	case "cancel":
		sess := h.sessions.Get(chatID)
		sess.Cancel()
		h.sendText(chatID, "🛑 Request cancelled.")

	default:
		h.sendText(chatID, fmt.Sprintf("Unknown command: /%s\n\nTry /start for help.", msg.Command()))
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
	h.sendText(chatID, fmt.Sprintf(
		"📊 *Session Status*\n\n"+
			"Chat ID: `%d`\n"+
			"Active: %s\n"+
			"Sessions: %d total\n"+
			"Turns: %d\n"+
			"Claude: `%s`\n"+
			"Model: `%s`\n"+
			"Tokens: %d in / %d out\n"+
			"Queue: %d pending",
		chatID, label, sessionCount,
		sess.GetMessageCount(), sessionID, modelName,
		sess.InputTokens, sess.OutputTokens,
		h.engine.QueueDepth(chatID),
	))
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

	m, err := h.resolver.SetOverride(chatID, arg)
	if err != nil {
		h.sendText(chatID, fmt.Sprintf("❌ %v", err))
		return
	}

	// Reset session so the new model starts fresh (Claude CLI sessions are model-bound)
	h.sessions.Reset(chatID)
	h.sendText(chatID, fmt.Sprintf(
		"✅ Switched to `%s` (%s)\n"+
			"Context: %dk tokens · Cost: $%.1f/$%.1f per 1M\n"+
			"Session cleared for clean start.",
		m.ID, m.Name, m.ContextWindow/1000, m.Cost.Input, m.Cost.Output,
	))
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

	// Inject recent conversation history when starting a brand-new session
	// (first message or after explicit /reset) so Claude has some context.
	historyContext := ""
	if sess.GetSessionID() == "" {
		historyContext = h.buildHistoryContext(sess.ID, 8)
	}

	_, err := h.engine.Enqueue(ctx, chatID, func(ctx context.Context) (*execution.RunResult, error) {
		return h.runClaude(ctx, sess, chatID, modelID, text, historyContext, placeholder)
	})

	if err != nil {
		log.Printf("handler: enqueue error: %v", err)
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

	// Record user message
	addEvent(transcript.Event{Type: transcript.EventUserMessage, Content: userText})

	// Track assistant text for memory extraction
	var assistantText strings.Builder
	var textMu sync.Mutex

	cb := claude.StreamCallbacks{
		OnText: func(chunk string) {
			textMu.Lock()
			assistantText.WriteString(chunk)
			textMu.Unlock()
			streamer.Write(chunk)
		},
		OnToolCall: func(name string, inputJSON string) {
			addEvent(transcript.Event{Type: transcript.EventToolCall, ToolName: name, ToolInput: inputJSON})
			// Compact tool progress with short snippet: Bash(cd stock_d) → Read(main.go)
			streamer.NoteTool(toolLabel(name, inputJSON))
		},
		OnToolResult: func(name string, toolResult tools.ToolResult) {
			// Log to transcript only — tool results not shown to user
			addEvent(transcript.Event{Type: transcript.EventToolResult, ToolName: name, Content: toolResult.Output, IsError: toolResult.IsError})

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

	if err := h.claude.SendMessage(ctx, sess, modelID, userText, memoryContext, cb); err != nil {
		if ctx.Err() != nil {
			if ctx.Err() == context.DeadlineExceeded {
				streamer.Write("\n\n⏰ Task timed out (10 min limit).")
			}
			result.Status = execution.RunCanceled
			streamer.Finalize()
			return result, nil
		}
		log.Printf("claude error: %v", err)
		streamer.Write(fmt.Sprintf("\n\n❌ Error: %v", err))
		result.Status = execution.RunFailed
		result.Error = err
	}

	streamer.Finalize()

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

	// Persist messages to SQLite (queryable history)
	if h.messages != nil {
		if err := h.messages.Append(sess.ID, agent.Message{Role: "user", Content: userText}); err != nil {
			log.Printf("handler: message store user error: %v", err)
		}
		if respText != "" {
			if err := h.messages.Append(sess.ID, agent.Message{Role: "assistant", Content: respText}); err != nil {
				log.Printf("handler: message store assistant error: %v", err)
			}
		}
	}

	// Auto-label session from first user message
	if sess.GetLabel() == "" && userText != "" {
		sess.SetLabel(truncateLabel(userText, 40))
	}

	// Mark session dirty for persistence
	h.sessions.MarkDirty()

	result.EndedAt = time.Now()
	return result, nil
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
