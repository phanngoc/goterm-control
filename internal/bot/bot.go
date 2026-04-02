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

	claudeClient := claude.New(
		cfg.Claude.APIKey,
		cfg.Claude.Model,
		cfg.Claude.MaxTokens,
		cfg.Claude.SystemPrompt,
		executor,
	)

	// Execution engine with hooks (wired in handler)
	engine := execution.NewEngine(execution.Hooks{})

	handler := NewHandler(api, sessions, claudeClient, cfg, engine, transcriptWriter, memoryStore)

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
		// Process each update in its own goroutine so slow responses don't block polling
		go b.handler.Handle(update)
	}
}

// Shutdown performs graceful cleanup.
func (b *Bot) Shutdown() {
	b.engine.Close()
	b.sessions.SaveNow()
	log.Println("bot: shutdown complete")
}
