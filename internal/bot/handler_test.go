package bot

import (
	"strings"
	"testing"

	"github.com/ngocp/goterm-control/internal/agent"
)

func TestShortenBashCommand(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxRunes int
		want     string
	}{
		{
			name:     "cd with long path",
			input:    "cd /Users/ngocp/Documents/projects/meClaw/goterm-control",
			maxRunes: 25,
			want:     "cd ../goterm-control",
		},
		{
			name:     "ls with long path and subdir",
			input:    "ls /Users/ngocp/Documents/projects/meClaw/goterm-control/internal/bot",
			maxRunes: 25,
			want:     "ls ../internal/bot",
		},
		{
			name:     "cd short path unchanged",
			input:    "cd /tmp",
			maxRunes: 25,
			want:     "cd /tmp",
		},
		{
			name:     "ls with flags no path",
			input:    "ls -la",
			maxRunes: 25,
			want:     "ls -la",
		},
		{
			name:     "multi-command takes first segment",
			input:    "cd /Users/ngocp/Documents/projects/meClaw/goterm-control && ls",
			maxRunes: 25,
			want:     "cd ../goterm-control",
		},
		{
			name:     "no path fallback to head truncate",
			input:    "echo hello world this is a very long command string",
			maxRunes: 25,
			want:     "echo hello world this is ",
		},
		{
			name:     "flags before path",
			input:    "ls -la /Users/ngocp/Documents/projects/meClaw/goterm-control/internal",
			maxRunes: 25,
			want:     "ls ../internal",
		},
		{
			name:     "grep with path",
			input:    "grep -r pattern /Users/ngocp/Documents/projects/meClaw/goterm-control",
			maxRunes: 25,
			want:     "grep ../goterm-control",
		},
		{
			name:     "pipe separator",
			input:    "cat /Users/ngocp/Documents/projects/long/path/file.go | head -20",
			maxRunes: 25,
			want:     "cat ../long/path/file.go",
		},
		{
			name:     "relative path with ./",
			input:    "cat ./very/long/nested/deep/directory/structure/file.txt",
			maxRunes: 25,
			want:     "cat ../structure/file.txt",
		},
		{
			name:     "tilde path",
			input:    "ls ~/Documents/projects/meClaw/goterm-control/internal/bot/handler.go",
			maxRunes: 25,
			want:     "ls ../bot/handler.go",
		},
		{
			name:     "already short enough",
			input:    "cd /tmp && ls -la",
			maxRunes: 25,
			want:     "cd /tmp && ls -la",
		},
		{
			name:     "semicolon separator",
			input:    "cd /Users/ngocp/Documents/projects/meClaw/goterm-control; ls",
			maxRunes: 25,
			want:     "cd ../goterm-control",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shortenBashCommand(tt.input, tt.maxRunes)
			if got != tt.want {
				t.Errorf("shortenBashCommand(%q, %d)\n  got  %q\n  want %q",
					tt.input, tt.maxRunes, got, tt.want)
			}
		})
	}
}

func TestToolLabel_BashCommand(t *testing.T) {
	tests := []struct {
		name      string
		toolName  string
		inputJSON string
		want      string
	}{
		{
			name:      "bash cd long path",
			toolName:  "Bash",
			inputJSON: `{"command":"cd /Users/ngocp/Documents/projects/meClaw/goterm-control"}`,
			want:      "Bash(cd ../goterm-control)",
		},
		{
			name:      "bash ls short",
			toolName:  "Bash",
			inputJSON: `{"command":"ls -la"}`,
			want:      "Bash(ls -la)",
		},
		{
			name:      "read file_path shortened",
			toolName:  "Read",
			inputJSON: `{"file_path":"/Users/ngocp/Documents/projects/meClaw/goterm-control/internal/bot/handler.go"}`,
			want:      "Read(../bot/handler.go)",
		},
		{
			name:      "edit path key shortened",
			toolName:  "Edit",
			inputJSON: `{"path":"/Users/ngocp/Documents/projects/meClaw/goterm-control/config.yaml"}`,
			want:      "Edit(../config.yaml)",
		},
		{
			name:      "empty json returns name only",
			toolName:  "Bash",
			inputJSON: `{}`,
			want:      "Bash",
		},
		{
			name:      "invalid json returns name only",
			toolName:  "Bash",
			inputJSON: `not json`,
			want:      "Bash",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toolLabel(tt.toolName, tt.inputJSON)
			if got != tt.want {
				t.Errorf("toolLabel(%q, %q)\n  got  %q\n  want %q",
					tt.toolName, tt.inputJSON, got, tt.want)
			}
		})
	}
}

func TestShortenPath(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxRunes int
		want     string
	}{
		{
			name:     "long absolute path",
			input:    "/Users/ngocp/Documents/projects/meClaw/goterm-control/internal/bot/handler.go",
			maxRunes: 25,
			want:     "../bot/handler.go",
		},
		{
			name:     "short path unchanged",
			input:    "/tmp/file.txt",
			maxRunes: 25,
			want:     "/tmp/file.txt",
		},
		{
			name:     "exact budget",
			input:    "exactly-twenty-five-rune",
			maxRunes: 25,
			want:     "exactly-twenty-five-rune",
		},
		{
			name:     "single deep file",
			input:    "/a/b/c/d/e/f/g/handler.go",
			maxRunes: 20,
			want:     "../e/f/g/handler.go",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shortenPath(tt.input, tt.maxRunes)
			if got != tt.want {
				t.Errorf("shortenPath(%q, %d)\n  got  %q\n  want %q",
					tt.input, tt.maxRunes, got, tt.want)
			}
		})
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
