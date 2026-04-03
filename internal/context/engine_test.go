package context

import (
	"testing"

	"github.com/ngocp/goterm-control/internal/agent"
)

func TestEngineAssemble(t *testing.T) {
	e := NewEngine(1000) // 1000 token window

	// Add messages
	e.Ingest(agent.Message{Role: "user", Content: "Hello"})
	e.Ingest(agent.Message{Role: "assistant", Content: "Hi there! How can I help?"})
	e.Ingest(agent.Message{Role: "user", Content: "Tell me about Go"})

	msgs, tokens := e.Assemble("You are helpful.")

	if len(msgs) != 3 {
		t.Errorf("expected 3 messages, got %d", len(msgs))
	}
	if tokens <= 0 {
		t.Errorf("expected positive token count, got %d", tokens)
	}
}

func TestEngineAssembleTrimming(t *testing.T) {
	e := NewEngine(100) // tiny window — will force trimming

	// Add many messages
	for i := 0; i < 50; i++ {
		e.Ingest(agent.Message{Role: "user", Content: "This is a somewhat long message that uses up tokens."})
		e.Ingest(agent.Message{Role: "assistant", Content: "Here is my response to your question, which also uses tokens."})
	}

	msgs, _ := e.Assemble("System prompt here.")

	// Should have trimmed — not all 100 messages
	if len(msgs) >= 100 {
		t.Errorf("expected trimming, got %d messages", len(msgs))
	}
	if len(msgs) == 0 {
		t.Error("expected at least some messages after trimming")
	}
}

func TestEngineTokenCount(t *testing.T) {
	e := NewEngine(200000)
	e.Ingest(agent.Message{Role: "user", Content: "Hello world"})
	e.Ingest(agent.Message{Role: "assistant", Content: "Hi!"})

	tokens := e.TokenCount()
	if tokens <= 0 {
		t.Errorf("expected positive tokens, got %d", tokens)
	}
}

func TestEngineClear(t *testing.T) {
	e := NewEngine(200000)
	e.Ingest(agent.Message{Role: "user", Content: "Hello"})
	e.Clear()

	if len(e.Messages()) != 0 {
		t.Errorf("expected empty after clear, got %d", len(e.Messages()))
	}
}

func TestEstimateTokens(t *testing.T) {
	// ~4 chars per token
	tokens := EstimateTokens("Hello world") // 11 chars → ~3 tokens
	if tokens < 2 || tokens > 5 {
		t.Errorf("expected ~3 tokens for 'Hello world', got %d", tokens)
	}

	// Empty string
	if EstimateTokens("") != 0 {
		t.Error("expected 0 tokens for empty string")
	}
}
