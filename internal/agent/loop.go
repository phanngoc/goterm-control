package agent

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

const defaultMaxIterations = 50

// RunAgent executes the core agent loop:
// message → model call → tool execution → model call → ... until end_turn or limit.
//
// This is the heart of BomClaw — equivalent to openclaw's runEmbeddedPiAgent.
func RunAgent(ctx context.Context, params RunParams) (*RunResult, error) {
	if params.MaxIterations <= 0 {
		params.MaxIterations = defaultMaxIterations
	}

	// Start with existing messages + new user message
	messages := make([]Message, len(params.Messages))
	copy(messages, params.Messages)
	messages = append(messages, Message{Role: "user", Content: params.UserMessage})

	var totalUsage Usage
	var lastText string

	for iteration := 0; iteration < params.MaxIterations; iteration++ {
		if ctx.Err() != nil {
			return &RunResult{
				Messages:   messages,
				Usage:      totalUsage,
				Iterations: iteration,
				StopReason: "canceled",
				Error:      ctx.Err(),
			}, ctx.Err()
		}

		// Stream model response
		events, err := params.Provider.Stream(ctx, StreamParams{
			Model:        params.ModelID,
			SystemPrompt: params.SystemPrompt,
			Messages:     messages,
			Tools:        params.Tools,
			MaxTokens:    params.MaxTokens,
		})
		if err != nil {
			if isContextOverflow(err) {
				log.Printf("agent: context overflow at iteration %d, trimming history", iteration)
				messages = trimOldMessages(messages, len(messages)/2)
				continue
			}
			if isRateLimit(err) {
				wait := backoffDuration(iteration)
				log.Printf("agent: rate limited, waiting %s", wait)
				select {
				case <-time.After(wait):
					continue
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return nil, fmt.Errorf("model stream: %w", err)
		}

		// Process stream events
		var textBuf strings.Builder
		var toolCalls []ToolCall
		var usage *Usage
		var stopReason string

		for ev := range events {
			switch ev.Type {
			case "text":
				textBuf.WriteString(ev.Text)
				if params.OnText != nil {
					params.OnText(ev.Text)
				}
			case "tool_use":
				toolCalls = append(toolCalls, ToolCall{
					ID:    ev.ToolID,
					Name:  ev.ToolName,
					Input: ev.ToolInput,
				})
				if params.OnToolCall != nil {
					params.OnToolCall(ev.ToolName, string(ev.ToolInput))
				}
			case "end":
				usage = ev.Usage
				stopReason = ev.StopReason
			case "error":
				return nil, ev.Error
			}
		}

		responseText := textBuf.String()
		lastText = responseText

		// Accumulate usage
		if usage != nil {
			totalUsage.InputTokens += usage.InputTokens
			totalUsage.OutputTokens += usage.OutputTokens
			totalUsage.CacheRead += usage.CacheRead
			totalUsage.CacheWrite += usage.CacheWrite
		}

		// Add assistant message to history
		assistantMsg := Message{Role: "assistant", Content: responseText, ToolCalls: toolCalls}
		messages = append(messages, assistantMsg)

		// If no tool calls, the model is done
		if len(toolCalls) == 0 || stopReason == "end_turn" {
			return &RunResult{
				Text:       lastText,
				Messages:   messages,
				Usage:      totalUsage,
				Iterations: iteration + 1,
				StopReason: "end_turn",
			}, nil
		}

		// Execute tools and build tool result message
		var results []ToolResult
		for _, tc := range toolCalls {
			result := params.ToolExecutor.Execute(ctx, tc.Name, tc.Input)
			result.ID = tc.ID
			results = append(results, result)

			if params.OnToolResult != nil {
				params.OnToolResult(tc.Name, result.Content, result.IsError)
			}
		}

		// Add tool results as user message (Anthropic API pattern)
		messages = append(messages, Message{Role: "user", ToolResults: results})
	}

	return &RunResult{
		Text:       lastText,
		Messages:   messages,
		Usage:      totalUsage,
		Iterations: params.MaxIterations,
		StopReason: "max_iterations",
		Error:      fmt.Errorf("agent loop exceeded %d iterations", params.MaxIterations),
	}, nil
}

// trimOldMessages keeps the first message (system context) and the last keepCount messages.
func trimOldMessages(messages []Message, keepCount int) []Message {
	if len(messages) <= keepCount {
		return messages
	}
	// Keep first message + last keepCount
	trimmed := make([]Message, 0, keepCount+1)
	trimmed = append(trimmed, messages[0])
	trimmed = append(trimmed, messages[len(messages)-keepCount:]...)
	return trimmed
}

func isContextOverflow(err error) bool {
	s := err.Error()
	return strings.Contains(s, "context_length") ||
		strings.Contains(s, "too long") ||
		strings.Contains(s, "maximum context") ||
		strings.Contains(s, "overloaded")
}

func isRateLimit(err error) bool {
	s := err.Error()
	return strings.Contains(s, "rate_limit") ||
		strings.Contains(s, "429") ||
		strings.Contains(s, "too many requests")
}

func backoffDuration(attempt int) time.Duration {
	base := time.Second
	for i := 0; i < attempt && i < 5; i++ {
		base *= 2
	}
	if base > 30*time.Second {
		base = 30 * time.Second
	}
	return base
}
