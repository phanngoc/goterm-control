package bot

import (
	"log"
	"path/filepath"
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

// Bot is the top-level Telegram bot.
type Bot struct {
	api      *tgbotapi.BotAPI
	handler  *Handler
	cfg      *config.Config
	sessions *session.Manager
	engine   *execution.Engine
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

	executor := tools.New(tools.ExecutorConfig{
		ShellTimeout:   cfg.Tools.ShellTimeout,
		MaxOutputBytes: cfg.Tools.MaxOutputBytes,
		AllowedPaths:   cfg.Tools.AllowedPaths,
	})

	// Session persistence
	store := session.NewStore(filepath.Join(cfg.Session.DataDir, "sessions.json"))
	idleTimeout := time.Duration(cfg.Session.IdleTimeout) * time.Minute
	sessions := session.NewManager(store, idleTimeout)

	// Transcript writer
	transcriptWriter := transcript.NewWriter(filepath.Join(cfg.Session.DataDir, "transcripts"))

	// Memory store
	var memoryStore *memory.Store
	if cfg.Memory.Enabled {
		memoryStore = memory.NewStore(filepath.Join(cfg.Session.DataDir, "memory"))
	}

	// Model resolver — builtin Claude models + custom models from config
	resolver := models.NewResolver(cfg.Models.Default, cfg.Models.Custom)
	defaultModel := resolver.Resolve(0)
	if defaultModel != nil {
		log.Printf("bot: default model=%s (%s)", defaultModel.ID, defaultModel.Name)
	}

	claudeClient := claude.New(cfg.Claude.SystemPrompt, executor)

	// Execution engine
	engine := execution.NewEngine(execution.Hooks{})

	handler := NewHandler(api, sessions, claudeClient, cfg, engine, transcriptWriter, memoryStore, resolver)

	return &Bot{
		api:      api,
		handler:  handler,
		cfg:      cfg,
		sessions: sessions,
		engine:   engine,
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
	b.engine.Close()
	b.sessions.SaveNow()
	log.Println("bot: shutdown complete")
}
