package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/ngocp/goterm-control/internal/models"
	"gopkg.in/yaml.v3"
)

type Config struct {
	// Provider selects the CLI backend that answers messages:
	// "claude" (Claude Code CLI, default) or "codex" (OpenAI Codex CLI).
	// Each has its own auth; see docs/design/shared-agent-memory.md §13 Q1.
	Provider string `yaml:"provider"`

	// Agent identifies this gateway process in the shared coordination
	// database — traces, tasks and inter-agent messages are all keyed by it.
	Agent AgentConfig `yaml:"agent"`

	// Coord configures the database shared by every agent on this machine.
	Coord CoordConfig `yaml:"coord"`

	// Tasks controls whether this agent picks up work its peers queued.
	Tasks TasksConfig `yaml:"tasks"`

	Telegram TelegramConfig `yaml:"telegram"`
	Claude   ClaudeConfig   `yaml:"claude"`
	Models   ModelsConfig   `yaml:"models"`
	Security SecurityConfig `yaml:"security"`
	Tools    ToolsConfig    `yaml:"tools"`
	Session  SessionConfig  `yaml:"session"`
	Memory   MemoryConfig   `yaml:"memory"`
	Gateway  GatewayConfig  `yaml:"gateway"`
	Browser  BrowserConfig  `yaml:"browser"`
}

// AgentConfig names this agent. Two agents on one machine MUST NOT share an
// id: it is the primary key in the shared agents table and the addressee for
// task assignment and inter-agent messages.
type AgentConfig struct {
	ID     string `yaml:"id"`      // default "bomclaw"
	Name   string `yaml:"name"`    // display name for the admin page
	WSAddr string `yaml:"ws_addr"` // how a peer agent reaches this one
}

// CoordConfig configures the shared coordination database (traces, tasks,
// inter-agent messages). Separate from session.data_dir on purpose: session
// tables carry the write volume of a live conversation, coordination data is
// low volume and worthless unless every agent sees the same rows.
type CoordConfig struct {
	// Enabled is a pointer so an absent key means "on" while an explicit
	// `enabled: false` still turns it off — a plain bool cannot tell those apart.
	Enabled            *bool  `yaml:"enabled"`
	Path               string `yaml:"path"`                 // default ~/.goterm-shared/data/coord.db
	NotesFile          string `yaml:"notes_file"`           // default ~/goterm-shared/NOTES.md
	TraceRetentionDays int    `yaml:"trace_retention_days"` // default 7; 0 disables the purge
}

// IsEnabled reports whether the shared coordination database should be opened.
func (c CoordConfig) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

// TasksConfig controls the shared task queue from this agent's side.
//
// AutoClaim defaults to OFF and should stay off unless the agent is meant to
// act unattended: a claimed task is executed with the same machine access the
// chat backend has, and nobody is watching when it runs.
type TasksConfig struct {
	AutoClaim           bool `yaml:"auto_claim"`
	PollIntervalSeconds int  `yaml:"poll_interval_seconds"` // default 60
	TimeoutMinutes      int  `yaml:"timeout_minutes"`       // default 15
}

// GatewayConfig holds gateway HTTP server settings.
// BrowserConfig groups browser control. Extension is the Browser Bridge: a
// Chrome extension in the user's own browser that the agent drives through the
// gateway (`bomclaw browser …`).
type BrowserConfig struct {
	Extension ExtensionConfig `yaml:"extension"`
}

// ExtensionConfig configures the /ext endpoint the Browser Bridge connects to.
type ExtensionConfig struct {
	Enabled            *bool    `yaml:"enabled"`              // default true
	Token              string   `yaml:"token"`                // pairing secret; empty = generated into <data_dir>/browser-bridge.token
	AllowEval          *bool    `yaml:"allow_eval"`           // default true; false removes `bomclaw browser eval`
	CallTimeoutSeconds int      `yaml:"call_timeout_seconds"` // default 30
	BlockedHosts       []string `yaml:"blocked_hosts"`        // sites the agent may never open; "*.example.com" allowed
}

// IsEnabled treats an absent flag as on: the endpoint only ever talks to an
// extension holding the pairing token, so leaving it up costs nothing.
func (e ExtensionConfig) IsEnabled() bool { return e.Enabled == nil || *e.Enabled }

// EvalAllowed treats an absent flag as on, matching the managed Chrome tools.
func (e ExtensionConfig) EvalAllowed() bool { return e.AllowEval == nil || *e.AllowEval }

type GatewayConfig struct {
	Auth AuthConfig `yaml:"auth"`
}

// AuthConfig controls dashboard username/password authentication.
// MUST be enabled before exposing the gateway via a public tunnel — the
// dashboard RPC drives a bypassPermissions agent with full machine control.
type AuthConfig struct {
	Enabled         bool   `yaml:"enabled"`
	PublicHost      string `yaml:"public_host"`       // e.g. "bot.bomclaw.org" (WS Origin allowlist)
	SessionTTLHours int    `yaml:"session_ttl_hours"` // default 168 (7 days)
}

// ClaudeConfig is kept for backward compatibility — the claude CLI subprocess config.
type ClaudeConfig struct {
	APIKey           string `yaml:"api_key"`
	Model            string `yaml:"model"`             // default model ID
	MaxTokens        int    `yaml:"max_tokens"`
	SystemPrompt     string `yaml:"system_prompt"`
	Workspace        string `yaml:"workspace"`         // working directory for claude CLI subprocess
	ExecutionTimeout int    `yaml:"execution_timeout"` // minutes; max time for a single request (default: 20)
}

// ModelsConfig defines available models and custom providers.
type ModelsConfig struct {
	Default  string                `yaml:"default"`  // default model ID (overrides claude.model)
	Custom   []models.Model        `yaml:"custom"`   // additional model definitions
}

type TelegramConfig struct {
	Token     string          `yaml:"token"`
	Timeout   int             `yaml:"timeout"`
	Indicator IndicatorConfig `yaml:"indicator"`
}

type IndicatorConfig struct {
	Enabled            bool     `yaml:"enabled"`
	BotName            string   `yaml:"bot_name"`
	Frames             []string `yaml:"frames"`
	Interval           int      `yaml:"interval"`
	UseChatAction      bool     `yaml:"use_chat_action"`
	ChatActionInterval int      `yaml:"chat_action_interval"` // seconds between sendChatAction calls
	ChatActionTTL      int      `yaml:"chat_action_ttl"`      // max seconds before auto-stop
}

type SecurityConfig struct {
	AllowedUserIDs []int64 `yaml:"allowed_user_ids"`
}

type ToolsConfig struct {
	ShellTimeout   int      `yaml:"shell_timeout"`
	MaxOutputBytes int      `yaml:"max_output_bytes"`
	AllowedPaths   []string `yaml:"allowed_paths"`
	// NOTE: The Claude CLI runs with --permission-mode bypassPermissions.
	// All tool calls (shell, file write, process kill) execute without approval.
	// This is intentional for a personal single-user bot. For shared deployments,
	// restrict access via security.allowed_user_ids instead.
}

type SessionConfig struct {
	DataDir string      `yaml:"data_dir"`
	Reset   ResetConfig `yaml:"reset"`
}

// ResetConfig controls automatic session rotation (openclaw pattern).
// Memory is flushed to disk before any automatic reset.
type ResetConfig struct {
	DailyAt     string `yaml:"daily_at"`     // "HH:MM" local time; "off" disables (default "04:00")
	IdleMinutes int    `yaml:"idle_minutes"` // rotate after this much inactivity; 0 disables
}

// MemoryConfig controls the markdown-based persistent memory system.
type MemoryConfig struct {
	Enabled       bool        `yaml:"enabled"`
	Dir           string      `yaml:"dir"`             // memory root; default: claude.workspace
	MaxFileChars  int         `yaml:"max_file_chars"`  // per injected file cap (default 20000)
	MaxTotalChars int         `yaml:"max_total_chars"` // whole memory block cap (default 60000)
	Flush         FlushConfig `yaml:"flush"`
}

// FlushConfig controls the silent memory-flush turn that runs before a
// session is reset or when its context grows past the soft threshold.
type FlushConfig struct {
	SoftThresholdTokens int    `yaml:"soft_threshold_tokens"` // default 100000
	TimeoutSeconds      int    `yaml:"timeout_seconds"`       // default 120
	Prompt              string `yaml:"prompt"`                // override default flush prompt; {today} expands to today's note path
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	// Override with env vars
	if tok := os.Getenv("TELEGRAM_TOKEN"); tok != "" {
		cfg.Telegram.Token = tok
	}
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		cfg.Claude.APIKey = key
	}
	if tok := os.Getenv("BOMCLAW_BROWSER_TOKEN"); tok != "" {
		cfg.Browser.Extension.Token = tok
	}

	// Defaults
	if cfg.Provider == "" {
		cfg.Provider = ProviderClaude
	}
	if cfg.Agent.ID == "" {
		cfg.Agent.ID = "bomclaw"
	}
	if cfg.Agent.Name == "" {
		cfg.Agent.Name = cfg.Agent.ID
	}
	if strings.HasPrefix(cfg.Coord.Path, "~/") {
		home, _ := os.UserHomeDir()
		cfg.Coord.Path = home + cfg.Coord.Path[1:]
	}
	if strings.HasPrefix(cfg.Coord.NotesFile, "~/") {
		home, _ := os.UserHomeDir()
		cfg.Coord.NotesFile = home + cfg.Coord.NotesFile[1:]
	}
	if cfg.Coord.TraceRetentionDays == 0 {
		cfg.Coord.TraceRetentionDays = 7
	}
	if cfg.Tasks.PollIntervalSeconds == 0 {
		cfg.Tasks.PollIntervalSeconds = 60
	}
	if cfg.Tasks.TimeoutMinutes == 0 {
		cfg.Tasks.TimeoutMinutes = 15
	}
	if cfg.Telegram.Timeout == 0 {
		cfg.Telegram.Timeout = 60
	}
	if cfg.Claude.Model == "" {
		cfg.Claude.Model = "claude-opus-5"
	}
	if cfg.Claude.MaxTokens == 0 {
		cfg.Claude.MaxTokens = 8192
	}
	if cfg.Claude.Workspace == "" {
		home, _ := os.UserHomeDir()
		cfg.Claude.Workspace = home + "/goterm-workspace"
	} else if strings.HasPrefix(cfg.Claude.Workspace, "~/") {
		home, _ := os.UserHomeDir()
		cfg.Claude.Workspace = home + cfg.Claude.Workspace[1:]
	}
	if cfg.Claude.ExecutionTimeout == 0 {
		cfg.Claude.ExecutionTimeout = 20
	}
	if cfg.Tools.ShellTimeout == 0 {
		cfg.Tools.ShellTimeout = 60
	}
	if cfg.Tools.MaxOutputBytes == 0 {
		cfg.Tools.MaxOutputBytes = 8192
	}
	if cfg.Session.DataDir == "" {
		home, _ := os.UserHomeDir()
		cfg.Session.DataDir = home + "/.goterm/data"
	} else if strings.HasPrefix(cfg.Session.DataDir, "~/") {
		home, _ := os.UserHomeDir()
		cfg.Session.DataDir = home + cfg.Session.DataDir[1:]
	}
	if cfg.Session.Reset.DailyAt == "" {
		cfg.Session.Reset.DailyAt = "04:00"
	}
	if cfg.Memory.Dir == "" {
		cfg.Memory.Dir = cfg.Claude.Workspace
	} else if strings.HasPrefix(cfg.Memory.Dir, "~/") {
		home, _ := os.UserHomeDir()
		cfg.Memory.Dir = home + cfg.Memory.Dir[1:]
	}
	if cfg.Memory.MaxFileChars == 0 {
		cfg.Memory.MaxFileChars = 20000
	}
	if cfg.Memory.MaxTotalChars == 0 {
		cfg.Memory.MaxTotalChars = 60000
	}
	if cfg.Memory.Flush.SoftThresholdTokens == 0 {
		cfg.Memory.Flush.SoftThresholdTokens = 100000
	}
	if cfg.Memory.Flush.TimeoutSeconds == 0 {
		cfg.Memory.Flush.TimeoutSeconds = 120
	}
	if cfg.Gateway.Auth.SessionTTLHours == 0 {
		cfg.Gateway.Auth.SessionTTLHours = 168
	}
	if cfg.Browser.Extension.CallTimeoutSeconds == 0 {
		cfg.Browser.Extension.CallTimeoutSeconds = 30
	}
	if cfg.Telegram.Indicator.Enabled {
		if len(cfg.Telegram.Indicator.Frames) == 0 {
			cfg.Telegram.Indicator.Frames = []string{"⏳", "⌛"}
		}
		if cfg.Telegram.Indicator.Interval == 0 {
			cfg.Telegram.Indicator.Interval = 3
		}
	}
	if cfg.Telegram.Indicator.UseChatAction {
		if cfg.Telegram.Indicator.ChatActionInterval == 0 {
			cfg.Telegram.Indicator.ChatActionInterval = 4
		}
		if cfg.Telegram.Indicator.ChatActionTTL == 0 {
			cfg.Telegram.Indicator.ChatActionTTL = 120
		}
	}

	// Resolve default model: models.default takes priority over claude.model
	if cfg.Models.Default == "" {
		cfg.Models.Default = cfg.Claude.Model
	}

	return &cfg, nil
}

// Supported provider keys.
const (
	ProviderClaude = "claude"
	ProviderCodex  = "codex"
)

func (c *Config) Validate() error {
	if c.Telegram.Token == "" {
		return fmt.Errorf("telegram.token is required (set TELEGRAM_TOKEN env var or config)")
	}
	switch c.Provider {
	case ProviderClaude:
		if c.Claude.APIKey == "" {
			return fmt.Errorf("claude.api_key is required (set ANTHROPIC_API_KEY env var or config)")
		}
	case ProviderCodex:
		// Codex authenticates itself via `codex login`; no key lives here.
		// Guard the model though: NewResolver silently falls back to the first
		// builtin (a claude model) when the configured id is unknown, and the
		// codex CLI would then reject every turn with a 400.
		want := c.Models.Default
		if want == "" {
			want = c.Claude.Model
		}
		m := models.NewResolver(want, c.Models.Custom).Lookup(want)
		if m == nil || m.API != models.APICodexCLI {
			return fmt.Errorf("provider %q needs a codex model, but models.default/claude.model is %q; "+
				"use a model with api: codex-cli (e.g. gpt-6-astra)", ProviderCodex, want)
		}
	default:
		return fmt.Errorf("provider must be %q or %q, got %q", ProviderClaude, ProviderCodex, c.Provider)
	}
	return nil
}

func (c *SecurityConfig) IsAllowed(userID int64) bool {
	if len(c.AllowedUserIDs) == 0 {
		return true
	}
	for _, id := range c.AllowedUserIDs {
		if id == userID {
			return true
		}
	}
	return false
}
