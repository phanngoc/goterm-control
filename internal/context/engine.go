package context

import (
	"github.com/ngocp/goterm-control/internal/agent"
)

// Engine manages conversation history and assembles context under a token budget.
// Equivalent to openclaw's ContextEngine but without pluggable backends.
type Engine struct {
	maxTokens int           // model's context window
	budget    float64       // fraction of context window to use (e.g. 0.8)
	messages  []agent.Message
}

// NewEngine creates a context engine for a given model's context window.
func NewEngine(maxTokens int) *Engine {
	return &Engine{
		maxTokens: maxTokens,
		budget:    0.8,
		messages:  make([]agent.Message, 0),
	}
}

// Ingest adds a message to the conversation history.
func (e *Engine) Ingest(msg agent.Message) {
	e.messages = append(e.messages, msg)
}

// Messages returns the full conversation history.
func (e *Engine) Messages() []agent.Message {
	return e.messages
}

// SetMessages replaces the conversation history (used after compaction or load).
func (e *Engine) SetMessages(msgs []agent.Message) {
	e.messages = msgs
}

// Assemble returns messages that fit within the token budget.
// If the full history exceeds the budget, older messages are trimmed.
func (e *Engine) Assemble(systemPrompt string) ([]agent.Message, int) {
	budgetTokens := int(float64(e.maxTokens) * e.budget)

	// Estimate system prompt tokens
	systemTokens := EstimateTokens(systemPrompt)
	available := budgetTokens - systemTokens

	if available <= 0 {
		// System prompt alone exceeds budget — return last message only
		if len(e.messages) > 0 {
			return e.messages[len(e.messages)-1:], systemTokens + estimateMessagesTokens(e.messages[len(e.messages)-1:])
		}
		return nil, systemTokens
	}

	// Walk backwards from the end, accumulating messages until budget is exhausted
	total := 0
	start := len(e.messages)
	for i := len(e.messages) - 1; i >= 0; i-- {
		msgTokens := estimateMessageTokens(e.messages[i])
		if total+msgTokens > available {
			break
		}
		total += msgTokens
		start = i
	}

	assembled := e.messages[start:]
	return assembled, systemTokens + total
}

// TokenCount returns the estimated total tokens in the conversation.
func (e *Engine) TokenCount() int {
	return estimateMessagesTokens(e.messages)
}

// Clear resets the conversation history.
func (e *Engine) Clear() {
	e.messages = e.messages[:0]
}

// estimateMessageTokens estimates tokens for a single message.
func estimateMessageTokens(m agent.Message) int {
	tokens := 4 // role overhead
	tokens += EstimateTokens(m.Content)
	for _, tc := range m.ToolCalls {
		tokens += EstimateTokens(tc.Name) + EstimateTokens(string(tc.Input)) + 10
	}
	for _, tr := range m.ToolResults {
		tokens += EstimateTokens(tr.Content) + 10
	}
	return tokens
}

func estimateMessagesTokens(msgs []agent.Message) int {
	total := 0
	for _, m := range msgs {
		total += estimateMessageTokens(m)
	}
	return total
}
