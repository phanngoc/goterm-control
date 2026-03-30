package bot

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/ngocp/goterm-control/internal/claude"
	"github.com/ngocp/goterm-control/internal/config"
	"github.com/ngocp/goterm-control/internal/session"
	"github.com/ngocp/goterm-control/internal/tools"
)

// Handler processes Telegram updates.
type Handler struct {
	bot      *tgbotapi.BotAPI
	sessions *session.Manager
	claude   *claude.Client
	cfg      *config.Config

	// approvalRequests maps callbackData → channel to signal approval/cancel
	approvalMu       sync.Mutex
	approvalRequests map[string]chan bool
}

func NewHandler(bot *tgbotapi.BotAPI, sessions *session.Manager, claudeClient *claude.Client, cfg *config.Config) *Handler {
	return &Handler{
		bot:              bot,
		sessions:         sessions,
		claude:           claudeClient,
		cfg:              cfg,
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

	// Regular message → Claude
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
			"📊 *Session Status*\n\nChat ID: `%d`\nTurns: %d\nSession: `%s`\nModel: `%s`",
			msg.Chat.ID, sess.MessageCount(), sessionID, h.cfg.Claude.Model,
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

	// Cancel any in-flight request
	sess.Cancel()

	// Send placeholder "thinking" message
	placeholder := h.sendText(chatID, "⏳ _Thinking..._")
	if placeholder == 0 {
		return
	}

	// Set up context with cancel
	ctx, cancel := context.WithCancel(context.Background())
	sess.SetCancel(cancel)
	defer func() {
		cancel()
		sess.SetCancel(nil)
	}()

	streamer := NewStreamer(h.bot, chatID, placeholder)

	// Track text so far for tool announcements
	var textMu sync.Mutex
	currentText := ""

	cb := claude.StreamCallbacks{
		OnText: func(chunk string) {
			textMu.Lock()
			currentText += chunk
			textMu.Unlock()
			streamer.Write(chunk)
		},
		OnToolCall: func(name string, inputJSON string) {
			notice := FormatToolCall(name, inputJSON)
			streamer.Append(notice)
		},
		OnToolResult: func(name string, result tools.ToolResult) {
			if result.IsImage {
				// Send screenshot as photo
				textMu.Lock()
				capText := currentText
				textMu.Unlock()
				_ = capText
				streamer.Flush()
				streamer.SendPhoto(result.ImagePath, fmt.Sprintf("📸 Screenshot from %s", name))
				// Tell Claude what happened
				streamer.Append("\n📸 _Screenshot sent above._\n")
			} else {
				notice := FormatToolResult(name, result.Output, result.IsError)
				streamer.Append(notice)
			}
		},
	}

	if err := h.claude.SendMessage(ctx, sess, msg.Text, cb); err != nil {
		if ctx.Err() != nil {
			streamer.Finalize()
			return // cancelled by user
		}
		log.Printf("claude error: %v", err)
		streamer.Append(fmt.Sprintf("\n\n❌ Error: %v", err))
	}

	streamer.Finalize()
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
