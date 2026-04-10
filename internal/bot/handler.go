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
	"github.com/ngocp/goterm-control/internal/claude"
	"github.com/ngocp/goterm-control/internal/config"
	"github.com/ngocp/goterm-control/internal/execution"
	"github.com/ngocp/goterm-control/internal/memory"
	"github.com/ngocp/goterm-control/internal/models"
	"github.com/ngocp/goterm-control/internal/session"
	"github.com/ngocp/goterm-control/internal/tools"
	"github.com/ngocp/goterm-control/internal/transcript"
)

// Handler processes Telegram updates.
type Handler struct {
	bot        *tgbotapi.BotAPI
	sessions   *session.Manager
	claude     *claude.Client
	cfg        *config.Config
	engine     *execution.Engine
	transcript *transcript.Writer
	memory     *memory.Store
	resolver   *models.Resolver

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
	memoryStore *memory.Store,
	resolver *models.Resolver,
) *Handler {
	return &Handler{
		bot:              bot,
		sessions:         sessions,
		claude:           claudeClient,
		cfg:              cfg,
		engine:           engine,
		transcript:       transcriptWriter,
		memory:           memoryStore,
		resolver:         resolver,
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
				"• /reset — clear conversation history\n"+
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

	case "status":
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
		h.sendText(chatID, fmt.Sprintf(
			"📊 *Session Status*\n\n"+
				"Chat ID: `%d`\n"+
				"Turns: %d\n"+
				"Session: `%s`\n"+
				"Model: `%s`\n"+
				"Tokens: %d in / %d out\n"+
				"Queue: %d pending",
			chatID, sess.GetMessageCount(), sessionID, modelName,
			sess.InputTokens, sess.OutputTokens,
			h.engine.QueueDepth(chatID),
		))

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
	chatID := msg.Chat.ID
	sess := h.sessions.Get(chatID)
	userText := msg.Text

	// Cancel any in-flight or queued request for this session
	sess.Cancel()

	// Create a cancellable context BEFORE enqueuing
	ctx, cancel := context.WithCancel(context.Background())
	sess.SetCancel(cancel)

	// Resolve model for this chat
	resolvedModel := h.resolver.Resolve(chatID)
	modelID := ""
	if resolvedModel != nil {
		modelID = resolvedModel.ID
	}

	// Send placeholder "thinking" message
	placeholder := h.sendText(chatID, "⏳ _Thinking..._")
	if placeholder == 0 {
		cancel()
		sess.SetCancel(nil)
		return
	}

	// Build memory context for injection
	memoryContext := ""
	if h.memory != nil {
		memoryContext = memory.BuildMemoryContext(h.memory, userText, h.cfg.Memory.MaxEntries)
	}

	// Enqueue the Claude call through the execution engine.
	_, err := h.engine.Enqueue(ctx, chatID, func(ctx context.Context) (*execution.RunResult, error) {
		return h.runClaude(ctx, sess, chatID, modelID, userText, memoryContext, placeholder)
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

	// Flush transcript to disk
	if h.transcript != nil {
		if err := h.transcript.AppendAll(sess.ID, events); err != nil {
			log.Printf("handler: transcript write error: %v", err)
		}
	}

	// Extract and store memory
	if h.memory != nil && respText != "" {
		entry := memory.ExtractFacts(sess.ID, chatID, userText, respText)
		if len(entry.Keywords) > 0 || len(entry.Facts) > 0 {
			if err := h.memory.Append(entry); err != nil {
				log.Printf("handler: memory append error: %v", err)
			}
		}
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

// toolLabel creates a short label like Bash(cd stock_d) or Read(main.go)
func toolLabel(name, inputJSON string) string {
	var m map[string]any
	if json.Unmarshal([]byte(inputJSON), &m) != nil {
		return name
	}
	for _, key := range []string{"command", "path", "file_path", "url", "query", "pattern", "script", "expression", "name", "ref", "text", "glob", "regex"} {
		if v, ok := m[key]; ok {
			s := fmt.Sprintf("%v", v)
			if s != "" {
				r := []rune(s)
				if len(r) > 15 {
					s = string(r[:15])
				}
				return name + "(" + s + ")"
			}
		}
	}
	return name
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
