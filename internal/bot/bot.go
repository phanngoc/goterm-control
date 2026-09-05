package bot

import (
	"log"
	"path/filepath"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/ngocp/goterm-control/internal/agent"
	anthropicClient "github.com/ngocp/goterm-control/internal/anthropic"
	"github.com/ngocp/goterm-control/internal/chat"
	"github.com/ngocp/goterm-control/internal/claude"
	"github.com/ngocp/goterm-control/internal/codex"
	"github.com/ngocp/goterm-control/internal/config"
	"github.com/ngocp/goterm-control/internal/execution"
	"github.com/ngocp/goterm-control/internal/memory"
	"github.com/ngocp/goterm-control/internal/models"
	"github.com/ngocp/goterm-control/internal/msgqueue"
	"github.com/ngocp/goterm-control/internal/session"
	"github.com/ngocp/goterm-control/internal/storage"
	"github.com/ngocp/goterm-control/internal/titler"
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

// New creates and initialises the bot. db and sessions are shared with the
// gateway so label renames and turn counters are visible to the dashboard
// immediately (two managers on one SQLite file would clobber each other).
func New(cfg *config.Config, db *storage.DB, sessions *session.Manager) (*Bot, error) {
	api, err := tgbotapi.NewBotAPI(cfg.Telegram.Token)
	if err != nil {
		return nil, err
	}
	api.Debug = false
	log.Printf("bot: logged in as @%s", api.Self.UserName)

	// Register commands with Telegram menu
	commands := tgbotapi.NewSetMyCommands(
		tgbotapi.BotCommand{Command: "start", Description: "Show welcome message and help"},
		tgbotapi.BotCommand{Command: "sessions", Description: "List and switch sessions"},
		tgbotapi.BotCommand{Command: "new", Description: "Start a new conversation"},
		tgbotapi.BotCommand{Command: "reset", Description: "Clear current session"},
		tgbotapi.BotCommand{Command: "status", Description: "Show session info"},
		tgbotapi.BotCommand{Command: "models", Description: "List available models"},
		tgbotapi.BotCommand{Command: "model", Description: "Switch model (e.g. /model sonnet)"},
		tgbotapi.BotCommand{Command: "memory", Description: "Show persistent memory"},
		tgbotapi.BotCommand{Command: "remember", Description: "Save a note to memory"},
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

	// Transcript writer (JSONL audit trail — kept alongside SQLite)
	transcriptWriter := transcript.NewWriter(filepath.Join(cfg.Session.DataDir, "transcripts"))

	// Model resolver — builtin Claude models + custom models from config
	resolver := models.NewResolver(cfg.Models.Default, cfg.Models.Custom)
	defaultModel := resolver.Resolve(0)
	if defaultModel != nil {
		log.Printf("bot: default model=%s (%s)", defaultModel.ID, defaultModel.Name)
	}

	// Chat backend — one CLI subprocess family per agent, chosen by config.
	// Both keep their own conversation state (claude session / codex thread);
	// the session row records which one owns the stored id.
	var llm chat.Client
	if cfg.Provider == config.ProviderCodex {
		codexClient := codex.New(cfg.Claude.SystemPrompt)
		codexClient.SetWorkspace(cfg.Claude.Workspace)
		llm = codexClient
	} else {
		claudeClient := claude.New(cfg.Claude.SystemPrompt, executor)
		claudeClient.SetWorkspace(cfg.Claude.Workspace)
		llm = claudeClient
	}

	// Message store (SQLite — conversation history)
	messageStore := storage.NewMessageStore(db)

	// Execution engine
	engine := execution.NewEngine(execution.Hooks{}, 3)

	// Persistent memory (openclaw pattern) — markdown files in the workspace
	// that the Claude CLI reads/writes with its own tools.
	memManager := memory.NewManager(memory.Config{
		Enabled:             cfg.Memory.Enabled,
		Dir:                 cfg.Memory.Dir,
		MaxFileChars:        cfg.Memory.MaxFileChars,
		MaxTotalChars:       cfg.Memory.MaxTotalChars,
		FlushPrompt:         cfg.Memory.Flush.Prompt,
		SoftThresholdTokens: cfg.Memory.Flush.SoftThresholdTokens,
		FlushTimeout:        time.Duration(cfg.Memory.Flush.TimeoutSeconds) * time.Second,
	})
	if memManager.Enabled() {
		if err := memManager.Bootstrap(); err != nil {
			log.Printf("bot: warning: memory bootstrap failed: %v", err)
		} else {
			log.Printf("bot: memory enabled (dir=%s)", cfg.Memory.Dir)
		}
	}

	// Session titler — renames sessions to a content summary after each turn.
	// Uses the direct API when an API key is configured; otherwise falls back
	// to the claude CLI (which the bot already requires for OAuth tokens).
	var titleProvider agent.ModelProvider
	titleModel := resolver.Default()
	switch {
	case cfg.Provider == config.ProviderCodex:
		// Stay on the codex side: a claude model id would be rejected, and the
		// agent may not have Anthropic credentials at all.
		titleProvider = codex.NewCLIProvider(cfg.Claude.Workspace)
	case strings.HasPrefix(cfg.Claude.APIKey, "sk-ant-api"):
		titleProvider = anthropicClient.New(cfg.Claude.APIKey)
		if m := resolver.Lookup("haiku"); m != nil {
			titleModel = m.ID
		}
	default:
		titleProvider = claude.NewCLIProvider(cfg.Claude.Workspace)
		if m := resolver.Lookup("haiku"); m != nil {
			titleModel = m.ID
		}
	}
	sessionTitler := titler.New(titleProvider, titleModel)

	// Build handler first (queue needs handler.executeMessage as callback)
	handler := &Handler{
		bot:              api,
		sessions:         sessions,
		llm:              llm,
		cfg:              cfg,
		engine:           engine,
		transcript:       transcriptWriter,
		messages:         messageStore,
		resolver:         resolver,
		memory:           memManager,
		titler:           sessionTitler,
		approvalRequests: make(map[string]chan bool),
		indicator:        indicator,
		typing:           typing,
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

// Sessions exposes the bot's live session manager (read-only use: the
// gateway status RPC reports in-flight run info from it).
func (b *Bot) Sessions() *session.Manager {
	return b.sessions
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
