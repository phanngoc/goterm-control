package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/ngocp/goterm-control/internal/models"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Telegram TelegramConfig `yaml:"telegram"`
	Claude   ClaudeConfig   `yaml:"claude"`
	Models   ModelsConfig   `yaml:"models"`
	Security SecurityConfig `yaml:"security"`
	Tools    ToolsConfig    `yaml:"tools"`
	Session  SessionConfig  `yaml:"session"`
	Memory   MemoryConfig   `yaml:"memory"`
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
	DataDir     string `yaml:"data_dir"`
	IdleTimeout int    `yaml:"idle_timeout"` // minutes
}

type MemoryConfig struct {
	Enabled    bool `yaml:"enabled"`
	MaxEntries int  `yaml:"max_entries"`
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

	// Defaults
	if cfg.Telegram.Timeout == 0 {
		cfg.Telegram.Timeout = 60
	}
	if cfg.Claude.Model == "" {
		cfg.Claude.Model = "claude-opus-4-6"
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
	if strings.TrimSpace(cfg.Claude.SystemPrompt) == "" {
		cfg.Claude.SystemPrompt = DefaultSystemPrompt()
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
	}
	if cfg.Session.IdleTimeout == 0 {
		cfg.Session.IdleTimeout = 30
	}
	if cfg.Memory.MaxEntries == 0 {
		cfg.Memory.MaxEntries = 5
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

func (c *Config) Validate() error {
	if c.Telegram.Token == "" {
		return fmt.Errorf("telegram.token is required (set TELEGRAM_TOKEN env var or config)")
	}
	if c.Claude.APIKey == "" {
		return fmt.Errorf("claude.api_key is required (set ANTHROPIC_API_KEY env var or config)")
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
