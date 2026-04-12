package bot

import (
	"fmt"
	"log"
	"path/filepath"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	anthropicAPI "github.com/ngocp/goterm-control/internal/anthropic"
	"github.com/ngocp/goterm-control/internal/claude"
	"github.com/ngocp/goterm-control/internal/config"
	"github.com/ngocp/goterm-control/internal/execution"
	"github.com/ngocp/goterm-control/internal/memory"
	"github.com/ngocp/goterm-control/internal/models"
	"github.com/ngocp/goterm-control/internal/msgqueue"
	"github.com/ngocp/goterm-control/internal/session"
	"github.com/ngocp/goterm-control/internal/storage"
	"github.com/ngocp/goterm-control/internal/tools"
	"github.com/ngocp/goterm-control/internal/transcript"
)

// Bot is the top-level Telegram bot.
type Bot struct {
	api       *tgbotapi.BotAPI
	handler   *Handler
	cfg       *config.Config
	sessions  *session.Manager
	engine    *execution.Engine
	queue     *msgqueue.Queue
	indicator *NameIndicator
	typing    *TypingIndicator
}

// New creates and initialises the bot.
func New(cfg *config.Config) (*Bot, error) {
	api, err := tgbotapi.NewBotAPI(cfg.Telegram.Token)
	if err != nil {
		return nil, err
	}
	api.Debug = false
	log.Printf("bot: logged in as @%s", api.Self.UserName)

	// Register commands with Telegram menu
	commands := tgbotapi.NewSetMyCommands(
		tgbotapi.BotCommand{Command: "start", Description: "Show welcome message and help"},
		tgbotapi.BotCommand{Command: "reset", Description: "Clear conversation history"},
		tgbotapi.BotCommand{Command: "status", Description: "Show session info"},
		tgbotapi.BotCommand{Command: "models", Description: "List available models"},
		tgbotapi.BotCommand{Command: "model", Description: "Switch model (e.g. /model sonnet)"},
		tgbotapi.BotCommand{Command: "cancel", Description: "Cancel current request"},
	)
	if _, err := api.Request(commands); err != nil {
		log.Printf("bot: warning: failed to set commands menu: %v", err)
	}

	// Name indicator (loading animation via setMyName)
	indicator := NewNameIndicator(api, cfg.Telegram.Indicator)

	// Typing indicator (sendChatAction keepalive loop per chat)
	execTTL := time.Duration(cfg.Claude.ExecutionTimeout) * time.Minute
	typing := NewTypingIndicator(api, cfg.Telegram.Indicator, execTTL)

	executor := tools.New(tools.ExecutorConfig{
		ShellTimeout:   cfg.Tools.ShellTimeout,
		MaxOutputBytes: cfg.Tools.MaxOutputBytes,
		AllowedPaths:   cfg.Tools.AllowedPaths,
	})

	// Storage — SQLite database
	db, err := storage.Open(filepath.Join(cfg.Session.DataDir, "goterm.db"))
	if err != nil {
		return nil, fmt.Errorf("storage: %w", err)
	}

	// Session persistence (SQLite-backed)
	idleTimeout := time.Duration(cfg.Session.IdleTimeout) * time.Minute
	sessions := session.NewManager(storage.NewSessionStore(db), idleTimeout)

	// Transcript writer (JSONL audit trail — kept alongside SQLite)
	transcriptWriter := transcript.NewWriter(filepath.Join(cfg.Session.DataDir, "transcripts"))

	// Memory store (SQLite with FTS5)
	var memoryStore memory.MemoryBackend
	if cfg.Memory.Enabled {
		memoryStore = storage.NewMemoryStore(db)
	}

	// Model resolver — builtin Claude models + custom models from config
	resolver := models.NewResolver(cfg.Models.Default, cfg.Models.Custom)
	defaultModel := resolver.Resolve(0)
	if defaultModel != nil {
		log.Printf("bot: default model=%s (%s)", defaultModel.ID, defaultModel.Name)
	}

	claudeClient := claude.New(cfg.Claude.SystemPrompt, executor)
	claudeClient.SetWorkspace(cfg.Claude.Workspace)

	// Message store (SQLite — conversation history)
	messageStore := storage.NewMessageStore(db)

	// Execution engine
	engine := execution.NewEngine(execution.Hooks{}, 3)

	// Create Anthropic API client for LLM-based memory extraction (uses Haiku).
	// This is separate from the main claude CLI client used for conversation.
	var completer memory.Completer
	if cfg.Memory.Enabled && cfg.Memory.ExtractionMode != "rule" && cfg.Claude.APIKey != "" {
		completer = anthropicAPI.New(cfg.Claude.APIKey)
	}

	// Build handler first (queue needs handler.executeMessage as callback)
	handler := &Handler{
		bot:              api,
		sessions:         sessions,
		claude:           claudeClient,
		cfg:              cfg,
		engine:           engine,
		transcript:       transcriptWriter,
		memory:           memoryStore,
		messages:         messageStore,
		resolver:         resolver,
		approvalRequests: make(map[string]chan bool),
		indicator:        indicator,
		typing:           typing,
		completer:        completer,
	}

	// Message queue: debounce 800ms + collect while busy
	queue := msgqueue.New(800*time.Millisecond, handler.executeMessage)
	handler.queue = queue

	return &Bot{
		api:       api,
		handler:   handler,
		cfg:       cfg,
		sessions:  sessions,
		engine:    engine,
		queue:     queue,
		indicator: indicator,
		typing:    typing,
	}, nil
}

// Run starts the long-polling loop. Blocks until ctx is done.
func (b *Bot) Run() {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = b.cfg.Telegram.Timeout

	updates := b.api.GetUpdatesChan(u)

	log.Printf("bot: listening for updates (timeout=%ds)...", b.cfg.Telegram.Timeout)

	for update := range updates {
		go b.handler.Handle(update)
	}
}

// Shutdown performs graceful cleanup.
func (b *Bot) Shutdown() {
	b.typing.Close()
	b.indicator.Close()
	b.queue.Close()
	b.engine.Close()
	b.sessions.SaveNow()
	log.Println("bot: shutdown complete")
}
