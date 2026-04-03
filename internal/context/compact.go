package context

import (
	"context"
	"fmt"
	"strings"

	"github.com/ngocp/goterm-control/internal/agent"
)

const compactionPrompt = `Summarize the conversation so far in a concise paragraph. Focus on:
1. Key facts and decisions
2. Tools used and their results
3. Current state of the task
Keep it under 500 words. This summary will replace the old messages to free up context space.`

// Compact summarizes older messages to reduce context size.
// It calls the model to generate a summary, then replaces old messages with it.
// The last keepRecent messages are always preserved.
func (e *Engine) Compact(ctx context.Context, provider agent.ModelProvider, modelID string, keepRecent int) error {
	if keepRecent <= 0 {
		keepRecent = 4
	}
	if len(e.messages) <= keepRecent {
		return nil // nothing to compact
	}

	// Split: old messages to summarize, recent messages to keep
	oldMessages := e.messages[:len(e.messages)-keepRecent]
	recentMessages := e.messages[len(e.messages)-keepRecent:]

	// Build a summary request
	var summaryText strings.Builder
	for _, m := range oldMessages {
		summaryText.WriteString(fmt.Sprintf("[%s] %s\n", m.Role, m.Content))
		for _, tc := range m.ToolCalls {
			summaryText.WriteString(fmt.Sprintf("  tool_call: %s(%s)\n", tc.Name, string(tc.Input)))
		}
		for _, tr := range m.ToolResults {
			prefix := "  tool_result"
			if tr.IsError {
				prefix = "  tool_error"
			}
			// Truncate long tool results
			content := tr.Content
			if len(content) > 500 {
				content = content[:500] + "..."
			}
			summaryText.WriteString(fmt.Sprintf("%s: %s\n", prefix, content))
		}
	}

	// Ask the model to summarize
	events, err := provider.Stream(ctx, agent.StreamParams{
		Model:        modelID,
		SystemPrompt: compactionPrompt,
		Messages: []agent.Message{
			{Role: "user", Content: summaryText.String()},
		},
		MaxTokens: 1024,
	})
	if err != nil {
		return fmt.Errorf("compact: %w", err)
	}

	var summary strings.Builder
	for ev := range events {
		if ev.Type == "text" {
			summary.WriteString(ev.Text)
		}
		if ev.Type == "error" {
			return ev.Error
		}
	}

	// Replace old messages with a summary message
	compactedMessages := make([]agent.Message, 0, 1+len(recentMessages))
	compactedMessages = append(compactedMessages, agent.Message{
		Role:    "user",
		Content: "[Context summary from earlier conversation]\n\n" + summary.String(),
	})
	compactedMessages = append(compactedMessages, recentMessages...)

	e.messages = compactedMessages
	return nil
}
