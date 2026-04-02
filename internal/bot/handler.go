package bot

import (
	"context"
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
) *Handler {
	return &Handler{
		bot:              bot,
		sessions:         sessions,
		claude:           claudeClient,
		cfg:              cfg,
		engine:           engine,
		transcript:       transcriptWriter,
		memory:           memoryStore,
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
	switch msg.Command() {
	case "start":
		h.sendText(msg.Chat.ID, fmt.Sprintf(
			"👋 *GoTerm Control*\n\nI'm your Mac AI assistant powered by Claude.\n\nCommands:\n• /reset — clear conversation history\n• /status — show session info\n• /cancel — cancel current request\n\nJust send me any message and I'll help you control your Mac!",
		))
	case "reset":
		h.sessions.Reset(msg.Chat.ID)
		h.sendText(msg.Chat.ID, "🔄 Conversation history cleared.")
	case "status":
		sess := h.sessions.Get(msg.Chat.ID)
		sessionID := sess.GetSessionID()
		if sessionID == "" {
			sessionID = "none"
		} else if len(sessionID) > 8 {
			sessionID = sessionID[:8] + "..."
		}
		h.sendText(msg.Chat.ID, fmt.Sprintf(
			"📊 *Session Status*\n\nChat ID: `%d`\nTurns: %d\nSession: `%s`\nModel: `%s`\nTokens: %d in / %d out\nQueue: %d pending",
			msg.Chat.ID, sess.GetMessageCount(), sessionID, h.cfg.Claude.Model,
			sess.InputTokens, sess.OutputTokens,
			h.engine.QueueDepth(msg.Chat.ID),
		))
	case "cancel":
		sess := h.sessions.Get(msg.Chat.ID)
		sess.Cancel()
		h.sendText(msg.Chat.ID, "🛑 Request cancelled.")
	default:
		h.sendText(msg.Chat.ID, fmt.Sprintf("Unknown command: /%s\n\nTry /start for help.", msg.Command()))
	}
}

func (h *Handler) handleMessage(msg *tgbotapi.Message) {
	chatID := msg.Chat.ID
	sess := h.sessions.Get(chatID)
	userText := msg.Text

	// Cancel any in-flight or queued request for this session
	sess.Cancel()

	// Create a cancellable context BEFORE enqueuing so that queued-but-not-yet-started
	// requests can be cancelled via /cancel. Without this, context.Background() would
	// make queued requests uncancellable.
	ctx, cancel := context.WithCancel(context.Background())
	sess.SetCancel(cancel)

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
	// ctx is already cancellable via sess.Cancel().
	_, err := h.engine.Enqueue(ctx, chatID, func(ctx context.Context) (*execution.RunResult, error) {
		return h.runClaude(ctx, sess, chatID, userText, memoryContext, placeholder)
	})

	if err != nil {
		log.Printf("handler: enqueue error: %v", err)
	}
}

// runClaude executes a single Claude CLI call with streaming, transcript recording, and memory extraction.
// ctx is already cancellable via sess.Cancel() (set in handleMessage before enqueue).
func (h *Handler) runClaude(ctx context.Context, sess *session.Session, chatID int64, userText, memoryContext string, placeholderMsgID int) (*execution.RunResult, error) {
	// Derive a child context so cleanup is scoped to this run.
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
			notice := FormatToolCall(name, inputJSON)
			streamer.Append(notice)
		},
		OnToolResult: func(name string, toolResult tools.ToolResult) {
			addEvent(transcript.Event{Type: transcript.EventToolResult, ToolName: name, Content: toolResult.Output, IsError: toolResult.IsError})

			if toolResult.IsImage {
				streamer.Flush()
				path := toolResult.ImagePath
				log.Printf("handler: screenshot detected path=%q", path)
				for i := 0; i < 6; i++ {
					if _, err := os.Stat(path); err == nil {
						break
					}
					log.Printf("handler: waiting for screenshot file (attempt %d)…", i+1)
					time.Sleep(500 * time.Millisecond)
				}
				if _, err := os.Stat(path); err != nil {
					log.Printf("handler: screenshot file not ready: %v", err)
					streamer.Append(fmt.Sprintf("\n❌ _Screenshot file not found: %s_\n", path))
					return
				}
				streamer.SendPhoto(path, "📸 Screenshot")
				streamer.Append("\n📸 _Screenshot sent above._\n")
			} else {
				notice := FormatToolResult(name, toolResult.Output, toolResult.IsError)
				streamer.Append(notice)
			}
		},
	}

	if err := h.claude.SendMessage(ctx, sess, userText, memoryContext, cb); err != nil {
		if ctx.Err() != nil {
			result.Status = execution.RunCanceled
			streamer.Finalize()
			return result, nil
		}
		log.Printf("claude error: %v", err)
		streamer.Append(fmt.Sprintf("\n\n❌ Error: %v", err))
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

	// Answer the callback immediately (removes loading spinner)
	answer := tgbotapi.NewCallback(cb.ID, "")
	_, _ = h.bot.Request(answer)

	h.approvalMu.Lock()
	ch, ok := h.approvalRequests[data]
	if ok {
		delete(h.approvalRequests, data)
	}
	h.approvalMu.Unlock()

	if !ok {
		// Stale button
		edit := tgbotapi.NewEditMessageText(chatID, cb.Message.MessageID, "_(expired)_")
		edit.ParseMode = "Markdown"
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

// sendText sends a plain/markdown message and returns the message ID.
func (h *Handler) sendText(chatID int64, text string) int {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "Markdown"
	sent, err := h.bot.Send(msg)
	if err != nil {
		// Retry without markdown
		msg2 := tgbotapi.NewMessage(chatID, stripMarkdown(text))
		sent, err = h.bot.Send(msg2)
		if err != nil {
			log.Printf("sendText: %v", err)
			return 0
		}
	}
	return sent.MessageID
}
