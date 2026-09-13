package bot

import (
	"strings"
	"testing"

	"github.com/ngocp/goterm-control/internal/agent"
)

func TestBuildHistoryContext_WithMessages(t *testing.T) {
	store := &mockMessageStore{
		messages: []agent.Message{
			{Role: "user", Content: "push repo crypto-radar lên github"},
			{Role: "assistant", Content: "Đã push thành công repo crypto-radar lên GitHub."},
		},
	}
	h := &Handler{messages: store}

	ctx := h.buildHistoryContext("chat_123", 8)
	if ctx == "" {
		t.Fatal("expected non-empty history context")
	}
	if !strings.Contains(ctx, "crypto-radar") {
		t.Errorf("expected history to contain 'crypto-radar', got:\n%s", ctx)
	}
	if !strings.Contains(ctx, "Recent Conversation History") {
		t.Error("expected history header")
	}
	if !strings.Contains(ctx, "**User**:") {
		t.Error("expected User role label")
	}
	if !strings.Contains(ctx, "**Assistant**:") {
		t.Error("expected Assistant role label")
	}
}

func TestBuildHistoryContext_Empty(t *testing.T) {
	store := &mockMessageStore{}
	h := &Handler{messages: store}

	ctx := h.buildHistoryContext("chat_123", 8)
	if ctx != "" {
		t.Errorf("expected empty context for no messages, got:\n%s", ctx)
	}
}

func TestBuildHistoryContext_NilStore(t *testing.T) {
	h := &Handler{messages: nil}

	ctx := h.buildHistoryContext("chat_123", 8)
	if ctx != "" {
		t.Errorf("expected empty context for nil store, got:\n%s", ctx)
	}
}

func TestBuildHistoryContext_TruncatesLongMessages(t *testing.T) {
	longContent := strings.Repeat("x", 300)
	store := &mockMessageStore{
		messages: []agent.Message{
			{Role: "user", Content: longContent},
		},
	}
	h := &Handler{messages: store}

	ctx := h.buildHistoryContext("chat_123", 8)
	if strings.Contains(ctx, longContent) {
		t.Error("expected long message to be truncated")
	}
	if !strings.Contains(ctx, "...") {
		t.Error("expected truncation indicator '...'")
	}
}

// mockMessageStore implements MessageStore for testing.
type mockMessageStore struct {
	messages []agent.Message
}

func (m *mockMessageStore) Append(_ string, msg agent.Message) error {
	m.messages = append(m.messages, msg)
	return nil
}

func (m *mockMessageStore) LoadHistory(_ string, limit int) ([]agent.Message, error) {
	if limit > 0 && len(m.messages) > limit {
		return m.messages[len(m.messages)-limit:], nil
	}
	return m.messages, nil
}
