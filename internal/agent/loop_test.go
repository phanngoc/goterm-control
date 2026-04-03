package agent

import (
	"context"
	"encoding/json"
	"testing"
)

// mockProvider returns a canned response.
type mockProvider struct {
	responses [][]StreamEvent // one response per call
	callIdx   int
}

func (m *mockProvider) Stream(_ context.Context, _ StreamParams) (<-chan StreamEvent, error) {
	ch := make(chan StreamEvent, 64)
	go func() {
		defer close(ch)
		if m.callIdx < len(m.responses) {
			for _, ev := range m.responses[m.callIdx] {
				ch <- ev
			}
			m.callIdx++
		}
	}()
	return ch, nil
}

// mockExecutor returns a fixed result for any tool.
type mockExecutor struct {
	result string
}

func (m *mockExecutor) Execute(_ context.Context, _ string, _ json.RawMessage) ToolResult {
	return ToolResult{Content: m.result}
}

func TestRunAgentSimpleResponse(t *testing.T) {
	provider := &mockProvider{
		responses: [][]StreamEvent{
			{
				{Type: "text", Text: "Hello! "},
				{Type: "text", Text: "How can I help?"},
				{Type: "end", StopReason: "end_turn", Usage: &Usage{InputTokens: 10, OutputTokens: 5}},
			},
		},
	}

	result, err := RunAgent(context.Background(), RunParams{
		Provider:     provider,
		ModelID:      "test-model",
		SystemPrompt: "You are helpful",
		UserMessage:  "Hi",
		MaxTokens:    1024,
	})

	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "Hello! How can I help?" {
		t.Errorf("expected 'Hello! How can I help?', got %q", result.Text)
	}
	if result.Iterations != 1 {
		t.Errorf("expected 1 iteration, got %d", result.Iterations)
	}
	if result.StopReason != "end_turn" {
		t.Errorf("expected end_turn, got %s", result.StopReason)
	}
}

func TestRunAgentWithToolUse(t *testing.T) {
	provider := &mockProvider{
		responses: [][]StreamEvent{
			// First call: model wants to use a tool
			{
				{Type: "text", Text: "Let me check..."},
				{Type: "tool_use", ToolID: "tc1", ToolName: "run_shell", ToolInput: json.RawMessage(`{"command":"date"}`)},
				{Type: "end", StopReason: "tool_use"},
			},
			// Second call: model responds after seeing tool result
			{
				{Type: "text", Text: "The date is today."},
				{Type: "end", StopReason: "end_turn"},
			},
		},
	}

	executor := &mockExecutor{result: "Thu Apr 3 2026"}

	result, err := RunAgent(context.Background(), RunParams{
		Provider:     provider,
		ToolExecutor: executor,
		ModelID:      "test-model",
		SystemPrompt: "You are helpful",
		UserMessage:  "What's the date?",
		MaxTokens:    1024,
		Tools:        []ToolDef{{Name: "run_shell", Description: "Run shell command"}},
	})

	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "The date is today." {
		t.Errorf("expected 'The date is today.', got %q", result.Text)
	}
	if result.Iterations != 2 {
		t.Errorf("expected 2 iterations, got %d", result.Iterations)
	}
	// Should have 4 messages: user, assistant+tool, tool_result, assistant
	if len(result.Messages) != 4 {
		t.Errorf("expected 4 messages, got %d", len(result.Messages))
	}
}

func TestRunAgentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	provider := &mockProvider{
		responses: [][]StreamEvent{
			{{Type: "text", Text: "Should not reach"}},
		},
	}

	result, err := RunAgent(ctx, RunParams{
		Provider:    provider,
		ModelID:     "test-model",
		UserMessage: "Hi",
		MaxTokens:   1024,
	})

	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if result.StopReason != "canceled" {
		t.Errorf("expected canceled stop reason, got %s", result.StopReason)
	}
}
