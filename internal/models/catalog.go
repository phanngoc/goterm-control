package models

// ModelAPI identifies the wire protocol for a provider.
type ModelAPI string

const (
	APIClaudeCLI    ModelAPI = "claude-cli"    // Claude Code CLI subprocess
	APIAnthropic    ModelAPI = "anthropic"     // Anthropic Messages API (future)
	APIOpenAI       ModelAPI = "openai"        // OpenAI-compatible completions (future)
)

// InputType describes what a model can accept.
type InputType string

const (
	InputText     InputType = "text"
	InputImage    InputType = "image"
	InputDocument InputType = "document"
)

// Model describes an AI model's capabilities, cost, and routing info.
type Model struct {
	ID            string    `yaml:"id" json:"id"`                         // canonical ID (e.g. "claude-opus-4-6")
	Name          string    `yaml:"name" json:"name"`                     // display name
	Provider      string    `yaml:"provider" json:"provider"`             // provider key (e.g. "anthropic", "openai")
	API           ModelAPI  `yaml:"api" json:"api"`                       // wire protocol
	Aliases       []string  `yaml:"aliases,omitempty" json:"aliases"`     // shorthand names (e.g. "opus", "o4")
	ContextWindow int       `yaml:"context_window" json:"context_window"` // max input tokens
	MaxTokens     int       `yaml:"max_tokens" json:"max_tokens"`         // max output tokens
	Reasoning     bool      `yaml:"reasoning" json:"reasoning"`           // extended thinking support
	Input         []InputType `yaml:"input" json:"input"`                 // supported modalities
	Cost          ModelCost `yaml:"cost" json:"cost"`                     // pricing per 1M tokens
}

// ModelCost holds per-1M-token pricing.
type ModelCost struct {
	Input      float64 `yaml:"input" json:"input"`
	Output     float64 `yaml:"output" json:"output"`
	CacheRead  float64 `yaml:"cache_read" json:"cache_read"`
	CacheWrite float64 `yaml:"cache_write" json:"cache_write"`
}

// BuiltinModels returns the default Claude model catalog.
// These are hardcoded so the bot works with zero config beyond an API key.
func BuiltinModels() []Model {
	return []Model{
		{
			ID:            "claude-opus-4-6",
			Name:          "Claude Opus 4.6",
			Provider:      "anthropic",
			API:           APIClaudeCLI,
			Aliases:       []string{"opus", "o4"},
			ContextWindow: 200_000,
			MaxTokens:     32_000,
			Reasoning:     true,
			Input:         []InputType{InputText, InputImage, InputDocument},
			Cost:          ModelCost{Input: 15.0, Output: 75.0, CacheRead: 1.5, CacheWrite: 18.75},
		},
		{
			ID:            "claude-sonnet-4-6",
			Name:          "Claude Sonnet 4.6",
			Provider:      "anthropic",
			API:           APIClaudeCLI,
			Aliases:       []string{"sonnet", "s4"},
			ContextWindow: 200_000,
			MaxTokens:     16_000,
			Reasoning:     true,
			Input:         []InputType{InputText, InputImage, InputDocument},
			Cost:          ModelCost{Input: 3.0, Output: 15.0, CacheRead: 0.3, CacheWrite: 3.75},
		},
		{
			ID:            "claude-haiku-4-5",
			Name:          "Claude Haiku 4.5",
			Provider:      "anthropic",
			API:           APIClaudeCLI,
			Aliases:       []string{"haiku", "h4"},
			ContextWindow: 200_000,
			MaxTokens:     8_192,
			Reasoning:     false,
			Input:         []InputType{InputText, InputImage, InputDocument},
			Cost:          ModelCost{Input: 0.8, Output: 4.0, CacheRead: 0.08, CacheWrite: 1.0},
		},
	}
}
