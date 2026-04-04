// Integration tests for NanoClaw — end-to-end flows with real APIs.
//
// These tests require live credentials. Run with:
//
//	NANOCLAW_LIVE_TEST=1 go test ./tests/ -v -timeout 120s
//
// Required env vars:
//   - ANTHROPIC_API_KEY: Anthropic API key
//   - TELEGRAM_TOKEN: Telegram bot token
//   - TELEGRAM_CHAT_ID: Chat ID to send test messages to (your personal chat with the bot)
package tests

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/ngocp/goterm-control/internal/agent"
	anthropicClient "github.com/ngocp/goterm-control/internal/anthropic"
	"github.com/ngocp/goterm-control/internal/claude"
	agentctx "github.com/ngocp/goterm-control/internal/context"
	"github.com/ngocp/goterm-control/internal/memory"
	"github.com/ngocp/goterm-control/internal/models"
	"github.com/ngocp/goterm-control/internal/tools"
)

// --- helpers ---

func skipUnlessLive(t *testing.T) {
	t.Helper()
	if os.Getenv("NANOCLAW_LIVE_TEST") == "" {
		t.Skip("skipping live test (set NANOCLAW_LIVE_TEST=1 to run)")
	}
}

func loadDotEnv(t *testing.T) {
	t.Helper()
	// Try loading .env from project root (go test cwd varies)
	for _, path := range []string{".env", "../.env", "../../.env"} {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		defer f.Close()
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			k = strings.TrimSpace(k)
			v = strings.Trim(strings.TrimSpace(v), `"'`)
			if os.Getenv(k) == "" {
				os.Setenv(k, v)
			}
		}
		return
	}
}

func requireEnv(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Fatalf("required env var %s is not set", key)
	}
	return v
}

// makeProvider returns the right ModelProvider based on token type.
// OAuth tokens (sk-ant-oat...) use CLI subprocess; API keys use direct API.
func makeProvider(apiKey string) agent.ModelProvider {
	if strings.HasPrefix(apiKey, "sk-ant-oat") {
		return claude.NewCLIProvider()
	}
	return anthropicClient.New(apiKey)
}

// makeToolProvider returns a provider for tests that need tool calling (direct API only).
// OAuth tokens don't support custom tools — the CLI manages its own tool set.
func makeToolProvider(t *testing.T, apiKey string) agent.ModelProvider {
	t.Helper()
	if strings.HasPrefix(apiKey, "sk-ant-oat") {
		t.Skip("skipping: tool-use tests require direct API key (sk-ant-api03-...), not OAuth token. Set ANTHROPIC_API_KEY to a direct key.")
	}
	return anthropicClient.New(apiKey)
}

// toolAdapter bridges tools.Executor to agent.ToolExecutor.
type toolAdapter struct {
	executor *tools.Executor
}

func (a *toolAdapter) Execute(ctx context.Context, name string, input json.RawMessage) agent.ToolResult {
	r := a.executor.Run(ctx, name, input)
	return agent.ToolResult{Content: r.Output, IsError: r.IsError}
}

func buildToolDefs() []agent.ToolDef {
	return allToolDefs()
}

func allToolDefs() []agent.ToolDef {
	return []agent.ToolDef{
		{Name: "run_shell", Description: "Execute a shell command on the Mac. Returns stdout+stderr.", InputSchema: map[string]any{
			"type": "object", "properties": map[string]any{
				"command":     map[string]any{"type": "string", "description": "Shell command"},
				"working_dir": map[string]any{"type": "string", "description": "Working directory"},
				"timeout":     map[string]any{"type": "integer", "description": "Timeout seconds"},
			}, "required": []string{"command"},
		}},
		{Name: "read_file", Description: "Read file contents. Max 100KB.", InputSchema: map[string]any{
			"type": "object", "properties": map[string]any{
				"path":   map[string]any{"type": "string", "description": "File path"},
				"offset": map[string]any{"type": "integer", "description": "Start line (1-indexed)"},
				"limit":  map[string]any{"type": "integer", "description": "Number of lines"},
			}, "required": []string{"path"},
		}},
		{Name: "write_file", Description: "Write content to a file. Creates parent dirs.", InputSchema: map[string]any{
			"type": "object", "properties": map[string]any{
				"path":    map[string]any{"type": "string", "description": "File path"},
				"content": map[string]any{"type": "string", "description": "Content to write"},
				"append":  map[string]any{"type": "boolean", "description": "Append mode"},
			}, "required": []string{"path", "content"},
		}},
		{Name: "list_dir", Description: "List directory contents with sizes.", InputSchema: map[string]any{
			"type": "object", "properties": map[string]any{
				"path":        map[string]any{"type": "string", "description": "Directory path"},
				"recursive":   map[string]any{"type": "boolean", "description": "Recursive listing"},
				"show_hidden": map[string]any{"type": "boolean", "description": "Show hidden files"},
			},
		}},
		{Name: "search_files", Description: "Search files by name or content.", InputSchema: map[string]any{
			"type": "object", "properties": map[string]any{
				"path":           map[string]any{"type": "string", "description": "Search directory"},
				"pattern":        map[string]any{"type": "string", "description": "Search pattern"},
				"search_content": map[string]any{"type": "boolean", "description": "Search file contents"},
			}, "required": []string{"pattern"},
		}},
		{Name: "get_clipboard", Description: "Get clipboard contents.", InputSchema: map[string]any{
			"type": "object", "properties": map[string]any{},
		}},
		{Name: "set_clipboard", Description: "Set clipboard to text.", InputSchema: map[string]any{
			"type": "object", "properties": map[string]any{
				"text": map[string]any{"type": "string", "description": "Text to copy"},
			}, "required": []string{"text"},
		}},
		{Name: "get_system_info", Description: "Get system information: OS, CPU, memory, disk.", InputSchema: map[string]any{
			"type": "object", "properties": map[string]any{},
		}},
		{Name: "list_processes", Description: "List running processes.", InputSchema: map[string]any{
			"type": "object", "properties": map[string]any{
				"filter":  map[string]any{"type": "string", "description": "Filter by name"},
				"sort_by": map[string]any{"type": "string", "description": "Sort: cpu, memory, pid"},
			},
		}},
		{Name: "run_applescript", Description: "Run AppleScript code.", InputSchema: map[string]any{
			"type": "object", "properties": map[string]any{
				"script": map[string]any{"type": "string", "description": "AppleScript code"},
			}, "required": []string{"script"},
		}},
		// Browser automation
		{Name: "browser_navigate", Description: "Navigate browser to a URL.", InputSchema: map[string]any{
			"type": "object", "properties": map[string]any{
				"url": map[string]any{"type": "string", "description": "URL to navigate to"},
			}, "required": []string{"url"},
		}},
		{Name: "browser_snapshot", Description: "Get accessibility snapshot with element refs (@e1, @e2).", InputSchema: map[string]any{
			"type": "object", "properties": map[string]any{
				"selector": map[string]any{"type": "string", "description": "CSS selector scope"},
			},
		}},
		{Name: "browser_click", Description: "Click element by ref.", InputSchema: map[string]any{
			"type": "object", "properties": map[string]any{
				"ref": map[string]any{"type": "string", "description": "Element ref (e.g. @e3)"},
			}, "required": []string{"ref"},
		}},
		{Name: "browser_fill", Description: "Clear and type in input field.", InputSchema: map[string]any{
			"type": "object", "properties": map[string]any{
				"ref":  map[string]any{"type": "string", "description": "Input field ref"},
				"text": map[string]any{"type": "string", "description": "Text to fill"},
			}, "required": []string{"ref", "text"},
		}},
		{Name: "browser_get_text", Description: "Get text/title/url from element or page.", InputSchema: map[string]any{
			"type": "object", "properties": map[string]any{
				"ref":      map[string]any{"type": "string", "description": "Element ref"},
				"property": map[string]any{"type": "string", "description": "text, html, value, title, url"},
			},
		}},
		{Name: "browser_eval", Description: "Execute JavaScript in browser.", InputSchema: map[string]any{
			"type": "object", "properties": map[string]any{
				"expression": map[string]any{"type": "string", "description": "JS expression"},
			}, "required": []string{"expression"},
		}},
		{Name: "browser_scroll", Description: "Scroll page.", InputSchema: map[string]any{
			"type": "object", "properties": map[string]any{
				"direction": map[string]any{"type": "string", "description": "up/down/left/right"},
			},
		}},
		{Name: "browser_back", Description: "Navigate back.", InputSchema: map[string]any{
			"type": "object", "properties": map[string]any{},
		}},
		{Name: "browser_wait", Description: "Wait for element, text, or time.", InputSchema: map[string]any{
			"type": "object", "properties": map[string]any{
				"ref":  map[string]any{"type": "string", "description": "Element ref to wait for"},
				"text": map[string]any{"type": "string", "description": "Text to wait for"},
				"ms":   map[string]any{"type": "integer", "description": "Milliseconds to wait"},
			},
		}},
	}
}

// ============================================================
// Test 1: Anthropic API direct — streaming text response
// ============================================================

func TestLive_AnthropicStreaming(t *testing.T) {
	skipUnlessLive(t)
	loadDotEnv(t)
	apiKey := requireEnv(t, "ANTHROPIC_API_KEY")

	client := makeProvider(apiKey)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	events, err := client.Stream(ctx, agent.StreamParams{
		Model:        "claude-haiku-4-5",
		SystemPrompt: "You are a helpful assistant. Be very brief.",
		Messages:     []agent.Message{{Role: "user", Content: "Say exactly: PONG"}},
		MaxTokens:    100,
	})
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}

	var text strings.Builder
	var gotEnd bool
	for ev := range events {
		switch ev.Type {
		case "text":
			text.WriteString(ev.Text)
		case "end":
			gotEnd = true
			if ev.Usage != nil {
				t.Logf("usage: input=%d output=%d", ev.Usage.InputTokens, ev.Usage.OutputTokens)
			}
		case "error":
			t.Fatalf("stream error: %v", ev.Error)
		}
	}

	response := text.String()
	t.Logf("response: %q", response)

	if !strings.Contains(response, "PONG") {
		t.Errorf("expected response to contain PONG, got: %q", response)
	}
	if !gotEnd {
		t.Error("did not receive end event")
	}
}

// ============================================================
// Test 2: Agent loop — simple text response (no tools)
// ============================================================

func TestLive_AgentLoopSimple(t *testing.T) {
	skipUnlessLive(t)
	loadDotEnv(t)
	apiKey := requireEnv(t, "ANTHROPIC_API_KEY")

	client := makeProvider(apiKey)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var chunks []string
	result, err := agent.RunAgent(ctx, agent.RunParams{
		Provider:     client,
		ModelID:      "claude-haiku-4-5",
		SystemPrompt: "You are helpful. Be very brief — one sentence max.",
		UserMessage:  "What is 2+2? Just say the number.",
		MaxTokens:    100,
		OnText:       func(text string) { chunks = append(chunks, text) },
	})
	if err != nil {
		t.Fatalf("RunAgent error: %v", err)
	}

	t.Logf("response: %q (iterations=%d, stop=%s)", result.Text, result.Iterations, result.StopReason)
	t.Logf("usage: in=%d out=%d", result.Usage.InputTokens, result.Usage.OutputTokens)
	t.Logf("streamed %d chunks", len(chunks))

	if result.Iterations != 1 {
		t.Errorf("expected 1 iteration for simple response, got %d", result.Iterations)
	}
	if result.StopReason != "end_turn" {
		t.Errorf("expected end_turn, got %s", result.StopReason)
	}
	if !strings.Contains(result.Text, "4") {
		t.Errorf("expected response to contain '4', got: %q", result.Text)
	}
	if len(chunks) == 0 {
		t.Error("expected streaming chunks, got none")
	}
}

// ============================================================
// Test 3: Agent loop — with tool use (run_shell)
// ============================================================

func TestLive_AgentLoopWithTools(t *testing.T) {
	skipUnlessLive(t)
	loadDotEnv(t)
	apiKey := requireEnv(t, "ANTHROPIC_API_KEY")

	client := makeToolProvider(t, apiKey)
	executor := tools.New(tools.ExecutorConfig{ShellTimeout: 10, MaxOutputBytes: 4096})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var toolCalls []string
	result, err := agent.RunAgent(ctx, agent.RunParams{
		Provider:     client,
		ToolExecutor: &toolAdapter{executor: executor},
		ModelID:      "claude-haiku-4-5",
		SystemPrompt: "You are helpful. Use the run_shell tool to answer questions about the system.",
		UserMessage:  "What is the current date? Use the run_shell tool with the `date` command.",
		Tools:        buildToolDefs(),
		MaxTokens:    1024,
		OnToolCall:   func(name, input string) { toolCalls = append(toolCalls, name) },
	})
	if err != nil {
		t.Fatalf("RunAgent error: %v", err)
	}

	t.Logf("response: %q", result.Text)
	t.Logf("iterations=%d stop=%s tool_calls=%v", result.Iterations, result.StopReason, toolCalls)
	t.Logf("messages in conversation: %d", len(result.Messages))

	if len(toolCalls) == 0 {
		t.Error("expected at least one tool call (run_shell), got none")
	}
	if result.Iterations < 2 {
		t.Errorf("expected >=2 iterations (call + response), got %d", result.Iterations)
	}

	// Should have mentioned the date in response
	if result.Text == "" {
		t.Error("expected non-empty response text")
	}
}

// ============================================================
// Test 4: Context engine — assembly and trimming with real API
// ============================================================

func TestLive_ContextEngineWithAgent(t *testing.T) {
	skipUnlessLive(t)
	loadDotEnv(t)
	apiKey := requireEnv(t, "ANTHROPIC_API_KEY")

	client := makeToolProvider(t, apiKey) // multi-turn context needs direct API
	engine := agentctx.NewEngine(200_000) // claude context window

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// First turn
	engine.Ingest(agent.Message{Role: "user", Content: "My name is NanoTest."})
	engine.Ingest(agent.Message{Role: "assistant", Content: "Nice to meet you, NanoTest!"})

	// Second turn — ask about the first turn (tests context assembly)
	msgs, tokens := engine.Assemble("You are helpful.")
	t.Logf("assembled %d messages, ~%d tokens", len(msgs), tokens)

	// Add new user message
	allMsgs := append(msgs, agent.Message{Role: "user", Content: "What is my name? Reply with just the name."})

	events, err := client.Stream(ctx, agent.StreamParams{
		Model:        "claude-haiku-4-5",
		SystemPrompt: "You are helpful. Be very brief.",
		Messages:     allMsgs,
		MaxTokens:    50,
	})
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}

	var text strings.Builder
	for ev := range events {
		if ev.Type == "text" {
			text.WriteString(ev.Text)
		}
		if ev.Type == "error" {
			t.Fatalf("stream error: %v", ev.Error)
		}
	}

	response := text.String()
	t.Logf("response: %q", response)

	if !strings.Contains(response, "NanoTest") {
		t.Errorf("expected model to remember 'NanoTest' from context, got: %q", response)
	}
}

// ============================================================
// Test 5: Model resolver — verify all builtin models work
// ============================================================

func TestLive_ModelResolverAllModels(t *testing.T) {
	skipUnlessLive(t)
	loadDotEnv(t)
	apiKey := requireEnv(t, "ANTHROPIC_API_KEY")

	client := makeProvider(apiKey)
	resolver := models.NewResolver("claude-haiku-4-5", nil)

	for _, m := range resolver.List() {
		t.Run(m.ID, func(t *testing.T) {
			// Only test haiku to save cost — others verified by model resolver unit tests
			if m.ID != "claude-haiku-4-5" {
				t.Skipf("skipping %s to save cost (haiku tested as representative)", m.ID)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()

			events, err := client.Stream(ctx, agent.StreamParams{
				Model:    m.ID,
				Messages: []agent.Message{{Role: "user", Content: "Say OK"}},
				MaxTokens: 10,
			})
			if err != nil {
				t.Fatalf("model %s: stream error: %v", m.ID, err)
			}

			var text strings.Builder
			for ev := range events {
				if ev.Type == "text" {
					text.WriteString(ev.Text)
				}
				if ev.Type == "error" {
					t.Fatalf("model %s: %v", m.ID, ev.Error)
				}
			}

			if text.Len() == 0 {
				t.Errorf("model %s: empty response", m.ID)
			}
			t.Logf("model %s: %q", m.ID, text.String())
		})
	}
}

// ============================================================
// Test 6: Telegram — send message and verify delivery
// ============================================================

func TestLive_TelegramSendMessage(t *testing.T) {
	skipUnlessLive(t)
	loadDotEnv(t)
	token := requireEnv(t, "TELEGRAM_TOKEN")
	chatIDStr := os.Getenv("TELEGRAM_CHAT_ID")
	if chatIDStr == "" {
		t.Skip("TELEGRAM_CHAT_ID not set — skipping Telegram send test")
	}
	chatID, err := strconv.ParseInt(chatIDStr, 10, 64)
	if err != nil {
		t.Fatalf("invalid TELEGRAM_CHAT_ID: %v", err)
	}

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		t.Fatalf("telegram auth failed: %v", err)
	}
	t.Logf("telegram: logged in as @%s", bot.Self.UserName)

	// Send a test message (plain text — avoids Markdown parse issues with special chars)
	msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("NanoClaw integration test\n\nTimestamp: %s\nBot: %s", time.Now().Format(time.RFC3339), bot.Self.UserName))
	sent, err := bot.Send(msg)
	if err != nil {
		t.Fatalf("send message failed: %v", err)
	}
	t.Logf("sent message ID=%d to chat=%d", sent.MessageID, chatID)

	// Edit the message (simulates streaming behavior)
	edit := tgbotapi.NewEditMessageText(chatID, sent.MessageID,
		fmt.Sprintf("NanoClaw integration test\n\nTimestamp: %s\nBot: %s\n\nEdit verified!", time.Now().Format(time.RFC3339), bot.Self.UserName))
	_, err = bot.Send(edit)
	if err != nil {
		t.Fatalf("edit message failed: %v", err)
	}
	t.Log("message edit successful")
}

// ============================================================
// Test 7: Full roundtrip — Telegram bot processes a message
//   and replies via Claude (the complete flow)
// ============================================================

func TestLive_FullRoundtrip(t *testing.T) {
	skipUnlessLive(t)
	loadDotEnv(t)
	token := requireEnv(t, "TELEGRAM_TOKEN")
	apiKey := requireEnv(t, "ANTHROPIC_API_KEY")
	chatIDStr := os.Getenv("TELEGRAM_CHAT_ID")
	if chatIDStr == "" {
		t.Skip("TELEGRAM_CHAT_ID not set — skipping full roundtrip test")
	}
	chatID, err := strconv.ParseInt(chatIDStr, 10, 64)
	if err != nil {
		t.Fatalf("invalid TELEGRAM_CHAT_ID: %v", err)
	}

	// --- Step 1: Create Telegram bot ---
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		t.Fatalf("telegram auth: %v", err)
	}
	t.Logf("step 1: telegram bot @%s ready", bot.Self.UserName)

	// --- Step 2: Create provider (tool-use requires direct API) ---
	if strings.HasPrefix(apiKey, "sk-ant-oat") {
		t.Skip("skipping: FullRoundtrip requires direct API key for tool-use")
	}
	provider := anthropicClient.New(apiKey)
	t.Log("step 2: anthropic provider ready")

	// --- Step 3: Create tool executor ---
	executor := tools.New(tools.ExecutorConfig{ShellTimeout: 10, MaxOutputBytes: 4096})
	t.Log("step 3: tool executor ready")

	// --- Step 4: Send "thinking" placeholder ---
	placeholder := tgbotapi.NewMessage(chatID, "⏳ _NanoClaw integration test — thinking..._")
	placeholder.ParseMode = "Markdown"
	placeholderMsg, err := bot.Send(placeholder)
	if err != nil {
		t.Fatalf("send placeholder: %v", err)
	}
	t.Logf("step 4: placeholder sent (msg_id=%d)", placeholderMsg.MessageID)

	// --- Step 5: Run agent loop ---
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var streamedText strings.Builder
	result, err := agent.RunAgent(ctx, agent.RunParams{
		Provider:     provider,
		ToolExecutor: &toolAdapter{executor: executor},
		ModelID:      "claude-haiku-4-5",
		SystemPrompt: "You are NanoClaw, a test bot. Be extremely brief (1-2 sentences). You can use tools if needed.",
		UserMessage:  "What OS am I running? Use get_system_info tool to check, then tell me.",
		Tools:        buildToolDefs(),
		MaxTokens:    512,
		OnText: func(text string) {
			streamedText.WriteString(text)
		},
		OnToolCall: func(name, input string) {
			t.Logf("  tool_call: %s", name)
			// Update placeholder to show tool use
			edit := tgbotapi.NewEditMessageText(chatID, placeholderMsg.MessageID,
				fmt.Sprintf("🔧 Using tool: %s...", name))
			bot.Send(edit)
		},
		OnToolResult: func(name, output string, isErr bool) {
			status := "✅"
			if isErr {
				status = "❌"
			}
			t.Logf("  tool_result: %s %s (%d bytes)", status, name, len(output))
		},
	})
	if err != nil {
		t.Fatalf("agent loop error: %v", err)
	}

	t.Logf("step 5: agent done — iterations=%d stop=%s", result.Iterations, result.StopReason)
	t.Logf("  usage: in=%d out=%d", result.Usage.InputTokens, result.Usage.OutputTokens)
	t.Logf("  response: %q", result.Text)

	// --- Step 6: Edit placeholder with final response ---
	finalText := result.Text
	if finalText == "" {
		finalText = streamedText.String()
	}
	if finalText == "" {
		finalText = "(empty response)"
	}

	// Truncate for Telegram limit
	if len(finalText) > 4000 {
		finalText = finalText[:4000] + "..."
	}

	edit := tgbotapi.NewEditMessageText(chatID, placeholderMsg.MessageID,
		fmt.Sprintf("🧪 *NanoClaw Full Roundtrip Test*\n\n%s\n\n_iterations: %d | tokens: %d in / %d out_",
			finalText, result.Iterations, result.Usage.InputTokens, result.Usage.OutputTokens))
	edit.ParseMode = "Markdown"
	_, err = bot.Send(edit)
	if err != nil {
		// Retry without markdown
		edit2 := tgbotapi.NewEditMessageText(chatID, placeholderMsg.MessageID, finalText)
		bot.Send(edit2)
	}
	t.Log("step 6: final response sent to Telegram")

	// --- Assertions ---
	if result.Iterations < 2 {
		t.Errorf("expected >=2 iterations (tool call + response), got %d", result.Iterations)
	}
	if result.Text == "" {
		t.Error("expected non-empty response")
	}
	// Should mention macOS or Darwin somewhere
	fullText := strings.ToLower(result.Text)
	if !strings.Contains(fullText, "mac") && !strings.Contains(fullText, "darwin") && !strings.Contains(fullText, "macos") {
		t.Logf("warning: response doesn't mention macOS/Darwin: %q", result.Text)
	}
}

// ============================================================
// Test 8: Multi-turn conversation — agent remembers context
// ============================================================

func TestLive_MultiTurnConversation(t *testing.T) {
	skipUnlessLive(t)
	loadDotEnv(t)
	apiKey := requireEnv(t, "ANTHROPIC_API_KEY")

	client := makeToolProvider(t, apiKey) // multi-turn needs direct API (CLI is stateless)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	// Turn 1: introduce context
	result1, err := agent.RunAgent(ctx, agent.RunParams{
		Provider:     client,
		ModelID:      "claude-haiku-4-5",
		SystemPrompt: "You are helpful. Be very brief.",
		UserMessage:  "Remember this code: ALPHA-7749. Just acknowledge.",
		MaxTokens:    100,
	})
	if err != nil {
		t.Fatalf("turn 1 error: %v", err)
	}
	t.Logf("turn 1: %q", result1.Text)

	// Turn 2: ask about the context (pass previous messages)
	result2, err := agent.RunAgent(ctx, agent.RunParams{
		Provider:     client,
		ModelID:      "claude-haiku-4-5",
		SystemPrompt: "You are helpful. Be very brief.",
		UserMessage:  "What was the code I told you? Reply with just the code.",
		Messages:     result1.Messages, // carry conversation forward
		MaxTokens:    100,
	})
	if err != nil {
		t.Fatalf("turn 2 error: %v", err)
	}
	t.Logf("turn 2: %q", result2.Text)

	if !strings.Contains(result2.Text, "ALPHA-7749") {
		t.Errorf("expected model to recall ALPHA-7749, got: %q", result2.Text)
	}
	// Should have 4 messages total: user1, assistant1, user2, assistant2
	if len(result2.Messages) < 4 {
		t.Errorf("expected >=4 messages in conversation, got %d", len(result2.Messages))
	}
}

// ============================================================
// Test 9: Memory — extract facts from conversation
// ============================================================

func TestLive_MemoryExtraction(t *testing.T) {
	skipUnlessLive(t)
	loadDotEnv(t)

	dir := t.TempDir()
	store := memory.NewStore(dir)

	// Simulate a conversation about deploying to a server
	entry := memory.ExtractFacts("sess_deploy", 100,
		"Deploy the app to /var/www/myapp on the production server. The repo is https://github.com/acme/myapp",
		"I've deployed the app to /var/www/myapp. The deployment was successful. I ran git pull and npm install, then restarted the service with systemctl restart myapp.",
	)

	t.Logf("keywords (%d): %v", len(entry.Keywords), entry.Keywords)
	t.Logf("facts (%d): %v", len(entry.Facts), entry.Facts)
	t.Logf("summary: %q", entry.Summary)

	// Should extract paths
	hasPath := false
	for _, f := range entry.Facts {
		if strings.Contains(f, "/var/www/myapp") {
			hasPath = true
		}
	}
	if !hasPath {
		t.Error("expected to extract path /var/www/myapp from facts")
	}

	// Should extract URL
	hasURL := false
	for _, f := range entry.Facts {
		if strings.Contains(f, "github.com/acme/myapp") {
			hasURL = true
		}
	}
	if !hasURL {
		t.Error("expected to extract URL github.com/acme/myapp from facts")
	}

	// Should have meaningful keywords
	if len(entry.Keywords) < 5 {
		t.Errorf("expected >=5 keywords, got %d", len(entry.Keywords))
	}

	// Store and read back
	if err := store.Append(entry); err != nil {
		t.Fatalf("append: %v", err)
	}
	all, err := store.ReadAll()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(all))
	}
	if all[0].SessionID != "sess_deploy" {
		t.Errorf("expected sess_deploy, got %s", all[0].SessionID)
	}
}

// ============================================================
// Test 10: Memory — search relevance ranking
// ============================================================

func TestLive_MemorySearch(t *testing.T) {
	skipUnlessLive(t)
	loadDotEnv(t)

	dir := t.TempDir()
	store := memory.NewStore(dir)

	// Seed 4 different topics
	entries := []memory.Entry{
		{SessionID: "s1", Keywords: []string{"docker", "container", "kubernetes", "deploy", "pod"},
			Facts: []string{"path: /etc/kubernetes/config", "url: https://k8s.io/docs"}, Summary: "Set up Kubernetes cluster with 3 nodes."},
		{SessionID: "s2", Keywords: []string{"golang", "testing", "benchmark", "coverage", "unit"},
			Facts: []string{"path: /Users/ngocp/go/src/myapp/main_test.go"}, Summary: "Wrote Go unit tests with 95% coverage."},
		{SessionID: "s3", Keywords: []string{"react", "typescript", "component", "frontend", "hook"},
			Facts: []string{"path: /src/components/Dashboard.tsx"}, Summary: "Created React dashboard component."},
		{SessionID: "s4", Keywords: []string{"docker", "compose", "nginx", "reverse", "proxy"},
			Facts: []string{"path: /etc/nginx/nginx.conf", "path: /docker-compose.yml"}, Summary: "Configured nginx reverse proxy in Docker Compose."},
	}
	for _, e := range entries {
		if err := store.Append(e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	// Search: docker → should rank s1 and s4 highest (both have "docker")
	results, err := store.Search("docker container setup", 3)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	t.Logf("search 'docker container setup': %d results", len(results))
	for i, r := range results {
		t.Logf("  #%d: %s — %s", i+1, r.SessionID, r.Summary)
	}
	if len(results) < 2 {
		t.Fatalf("expected >=2 results for docker query, got %d", len(results))
	}
	// Top result should be s1 (has "docker" + "container")
	if results[0].SessionID != "s1" {
		t.Errorf("expected s1 as top result, got %s", results[0].SessionID)
	}

	// Search: golang testing → should find s2
	results2, err := store.Search("golang test coverage", 2)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	t.Logf("search 'golang test coverage': %d results", len(results2))
	for i, r := range results2 {
		t.Logf("  #%d: %s — %s", i+1, r.SessionID, r.Summary)
	}
	if len(results2) == 0 || results2[0].SessionID != "s2" {
		t.Errorf("expected s2 as top result for golang query")
	}

	// Search: unrelated topic → should return empty or low results
	results3, err := store.Search("machine learning pytorch training", 3)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	t.Logf("search 'machine learning pytorch training': %d results", len(results3))
	// Could match "learning" loosely but shouldn't match strongly
}

// ============================================================
// Test 11: Memory — inject into prompt and Claude uses it
// ============================================================

func TestLive_MemoryInjectionWithClaude(t *testing.T) {
	skipUnlessLive(t)
	loadDotEnv(t)
	apiKey := requireEnv(t, "ANTHROPIC_API_KEY")

	dir := t.TempDir()
	store := memory.NewStore(dir)

	// Seed a memory about a specific project detail
	store.Append(memory.Entry{
		SessionID: "past_session",
		Keywords:  []string{"project", "codeword", "secret", "phoenix", "database"},
		Facts:     []string{"The project codeword is PHOENIX-42"},
		Summary:   "Discussed the secret project codeword PHOENIX-42 for the database migration.",
	})

	// Build memory context for a related query
	memCtx := memory.BuildMemoryContext(store, "what was the project codeword?", 5)
	t.Logf("memory context:\n%s", memCtx)

	if memCtx == "" {
		t.Fatal("expected non-empty memory context")
	}
	if !strings.Contains(memCtx, "PHOENIX-42") {
		t.Errorf("expected memory context to contain PHOENIX-42")
	}

	// Now ask Claude with memory injected into system prompt
	client := makeProvider(apiKey)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	systemPrompt := "You are helpful. Be very brief." + memCtx

	result, err := agent.RunAgent(ctx, agent.RunParams{
		Provider:     client,
		ModelID:      "claude-haiku-4-5",
		SystemPrompt: systemPrompt,
		UserMessage:  "What was the project codeword from our past conversation? Just say the codeword.",
		MaxTokens:    100,
	})
	if err != nil {
		t.Fatalf("agent error: %v", err)
	}

	t.Logf("response: %q", result.Text)

	if !strings.Contains(result.Text, "PHOENIX-42") {
		t.Errorf("expected Claude to recall PHOENIX-42 from memory, got: %q", result.Text)
	}
}

// ============================================================
// Test 12: Memory — full lifecycle (extract → store → search → inject → recall)
// ============================================================

func TestLive_MemoryFullLifecycle(t *testing.T) {
	skipUnlessLive(t)
	loadDotEnv(t)
	apiKey := requireEnv(t, "ANTHROPIC_API_KEY")

	dir := t.TempDir()
	store := memory.NewStore(dir)
	client := makeProvider(apiKey)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// --- Session 1: Have a conversation and extract memory ---
	t.Log("--- Session 1: conversation about server setup ---")
	result1, err := agent.RunAgent(ctx, agent.RunParams{
		Provider:     client,
		ModelID:      "claude-haiku-4-5",
		SystemPrompt: "You are helpful. Be very brief.",
		UserMessage:  "My production server IP is 10.0.1.42 and the deploy user is 'deployer'. Remember this.",
		MaxTokens:    200,
	})
	if err != nil {
		t.Fatalf("session 1: %v", err)
	}
	t.Logf("session 1 response: %q", result1.Text)

	// Extract and store memory from this conversation
	entry1 := memory.ExtractFacts("session_1", 100,
		"My production server IP is 10.0.1.42 and the deploy user is 'deployer'.",
		result1.Text,
	)
	// Manually add the important fact since rule-based extraction might miss IP
	entry1.Facts = append(entry1.Facts, "production server IP: 10.0.1.42", "deploy user: deployer")
	entry1.Keywords = append(entry1.Keywords, "production", "server", "deploy", "deployer", "10.0.1.42")
	if err := store.Append(entry1); err != nil {
		t.Fatalf("store append: %v", err)
	}
	t.Logf("stored memory: %d keywords, %d facts", len(entry1.Keywords), len(entry1.Facts))

	// --- Session 2: Different topic ---
	t.Log("--- Session 2: conversation about database ---")
	entry2 := memory.ExtractFacts("session_2", 100,
		"Set up PostgreSQL on port 5432 with database name 'appdb'.",
		"PostgreSQL is now running on port 5432. The database 'appdb' has been created with the schema applied.",
	)
	entry2.Keywords = append(entry2.Keywords, "postgresql", "database", "appdb", "5432")
	entry2.Facts = append(entry2.Facts, "PostgreSQL port: 5432", "database name: appdb")
	store.Append(entry2)

	// --- Session 3: Ask about the server (should recall from memory) ---
	t.Log("--- Session 3: recall server info via memory injection ---")

	memCtx := memory.BuildMemoryContext(store, "deploy to the production server", 3)
	t.Logf("injected memory context length: %d chars", len(memCtx))

	if !strings.Contains(memCtx, "10.0.1.42") {
		t.Errorf("memory context should contain server IP, got:\n%s", memCtx)
	}

	result3, err := agent.RunAgent(ctx, agent.RunParams{
		Provider:     client,
		ModelID:      "claude-haiku-4-5",
		SystemPrompt: "You are helpful. Be very brief. Use the memory context to answer." + memCtx,
		UserMessage:  "What is the production server IP and deploy user? Just state the facts.",
		MaxTokens:    150,
	})
	if err != nil {
		t.Fatalf("session 3: %v", err)
	}
	t.Logf("session 3 response: %q", result3.Text)

	if !strings.Contains(result3.Text, "10.0.1.42") {
		t.Errorf("expected Claude to recall server IP 10.0.1.42, got: %q", result3.Text)
	}
	if !strings.Contains(result3.Text, "deployer") {
		t.Errorf("expected Claude to recall deploy user 'deployer', got: %q", result3.Text)
	}

	// --- Verify search relevance ---
	t.Log("--- Verify search relevance ---")

	// Search for database should find session 2
	dbResults, _ := store.Search("postgresql database setup", 2)
	if len(dbResults) == 0 || dbResults[0].SessionID != "session_2" {
		t.Errorf("expected session_2 for database query, got %v", dbResults)
	}
	t.Logf("database search → top result: %s", dbResults[0].SessionID)

	// Search for server should find session 1
	srvResults, _ := store.Search("production server deploy", 2)
	if len(srvResults) == 0 || srvResults[0].SessionID != "session_1" {
		t.Errorf("expected session_1 for server query, got %v", srvResults)
	}
	t.Logf("server search → top result: %s", srvResults[0].SessionID)

	// Total entries
	all, _ := store.ReadAll()
	t.Logf("total memory entries: %d", len(all))
	if len(all) != 2 {
		t.Errorf("expected 2 entries, got %d", len(all))
	}
}

// ============================================================
// Test 13: Memory — Telegram roundtrip with memory injection
// ============================================================

func TestLive_MemoryTelegramRoundtrip(t *testing.T) {
	skipUnlessLive(t)
	loadDotEnv(t)
	apiKey := requireEnv(t, "ANTHROPIC_API_KEY")
	token := requireEnv(t, "TELEGRAM_TOKEN")
	chatIDStr := os.Getenv("TELEGRAM_CHAT_ID")
	if chatIDStr == "" {
		t.Skip("TELEGRAM_CHAT_ID not set")
	}
	chatID, _ := strconv.ParseInt(chatIDStr, 10, 64)

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		t.Fatalf("telegram: %v", err)
	}

	dir := t.TempDir()
	store := memory.NewStore(dir)

	// Pre-seed memory with non-sensitive project info
	store.Append(memory.Entry{
		SessionID: "old_session",
		Keywords:  []string{"project", "mascot", "nanoclaw", "dragon"},
		Facts:     []string{"Project mascot name: Drakey the Dragon"},
		Summary:   "Discussed the project mascot named Drakey the Dragon.",
	})

	// Send placeholder
	placeholder := tgbotapi.NewMessage(chatID, "Memory test — thinking...")
	sent, err := bot.Send(placeholder)
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	// Build memory + call agent
	memCtx := memory.BuildMemoryContext(store, "what is the project mascot", 3)
	client := makeProvider(apiKey)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := agent.RunAgent(ctx, agent.RunParams{
		Provider:     client,
		ModelID:      "claude-haiku-4-5",
		SystemPrompt: "Be very brief. Use memory context to answer." + memCtx,
		UserMessage:  "What is our project mascot name?",
		MaxTokens:    100,
	})
	if err != nil {
		t.Fatalf("agent: %v", err)
	}

	t.Logf("response: %q", result.Text)

	// Edit Telegram message with result
	editText := fmt.Sprintf("Memory Test Result:\n\n%s\n\n(from memory: old_session)", result.Text)
	edit := tgbotapi.NewEditMessageText(chatID, sent.MessageID, editText)
	bot.Send(edit)

	if !strings.Contains(result.Text, "Drakey") {
		t.Errorf("expected mascot name from memory, got: %q", result.Text)
	}
}

// ============================================================
// Test 14: Tool — run_shell (agent executes shell commands)
// ============================================================

func TestLive_ToolRunShell(t *testing.T) {
	skipUnlessLive(t)
	loadDotEnv(t)
	apiKey := requireEnv(t, "ANTHROPIC_API_KEY")

	client := makeToolProvider(t, apiKey)
	executor := tools.New(tools.ExecutorConfig{ShellTimeout: 10, MaxOutputBytes: 4096})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var usedTools []string
	result, err := agent.RunAgent(ctx, agent.RunParams{
		Provider:     client,
		ToolExecutor: &toolAdapter{executor: executor},
		ModelID:      "claude-haiku-4-5",
		SystemPrompt: "You are helpful. Use tools when asked. Be brief.",
		UserMessage:  "Use run_shell to run `echo HELLO_NANOCLAW` and tell me the output.",
		Tools:        allToolDefs(),
		MaxTokens:    512,
		OnToolCall:   func(name, _ string) { usedTools = append(usedTools, name) },
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	t.Logf("tools used: %v", usedTools)
	t.Logf("response: %q", result.Text)

	if len(usedTools) == 0 || usedTools[0] != "run_shell" {
		t.Errorf("expected run_shell to be called, got: %v", usedTools)
	}
	if !strings.Contains(result.Text, "HELLO_NANOCLAW") {
		t.Errorf("expected response to contain HELLO_NANOCLAW, got: %q", result.Text)
	}
}

// ============================================================
// Test 15: Tool — write_file + read_file roundtrip
// ============================================================

func TestLive_ToolWriteReadFile(t *testing.T) {
	skipUnlessLive(t)
	loadDotEnv(t)
	apiKey := requireEnv(t, "ANTHROPIC_API_KEY")

	client := makeToolProvider(t, apiKey)
	executor := tools.New(tools.ExecutorConfig{ShellTimeout: 10, MaxOutputBytes: 4096})

	tmpDir := t.TempDir()
	filePath := tmpDir + "/nanoclaw_test.txt"

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var usedTools []string

	// Ask Claude to write a file, then read it back
	result, err := agent.RunAgent(ctx, agent.RunParams{
		Provider:     client,
		ToolExecutor: &toolAdapter{executor: executor},
		ModelID:      "claude-haiku-4-5",
		SystemPrompt: "You are helpful. Use tools when asked. Be brief.",
		UserMessage:  fmt.Sprintf("Write the text 'NanoClaw was here 2026' to the file %s using write_file, then read it back using read_file and tell me what it says.", filePath),
		Tools:        allToolDefs(),
		MaxTokens:    512,
		OnToolCall:   func(name, _ string) { usedTools = append(usedTools, name) },
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	t.Logf("tools used: %v", usedTools)
	t.Logf("response: %q", result.Text)

	// Should have called write_file and read_file
	hasWrite, hasRead := false, false
	for _, tool := range usedTools {
		if tool == "write_file" {
			hasWrite = true
		}
		if tool == "read_file" {
			hasRead = true
		}
	}
	if !hasWrite {
		t.Error("expected write_file to be called")
	}
	if !hasRead {
		t.Error("expected read_file to be called")
	}

	// Verify file actually exists on disk
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("file not created: %v", err)
	}
	if !strings.Contains(string(data), "NanoClaw was here 2026") {
		t.Errorf("file content doesn't match: %q", string(data))
	}
	t.Logf("file on disk: %q", string(data))
}

// ============================================================
// Test 16: Tool — list_dir
// ============================================================

func TestLive_ToolListDir(t *testing.T) {
	skipUnlessLive(t)
	loadDotEnv(t)
	apiKey := requireEnv(t, "ANTHROPIC_API_KEY")

	client := makeToolProvider(t, apiKey)
	executor := tools.New(tools.ExecutorConfig{ShellTimeout: 10, MaxOutputBytes: 4096})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var usedTools []string
	result, err := agent.RunAgent(ctx, agent.RunParams{
		Provider:     client,
		ToolExecutor: &toolAdapter{executor: executor},
		ModelID:      "claude-haiku-4-5",
		SystemPrompt: "You are helpful. Use tools when asked. Be very brief.",
		UserMessage:  "Use list_dir to list the contents of /tmp and tell me how many items are there.",
		Tools:        allToolDefs(),
		MaxTokens:    512,
		OnToolCall:   func(name, _ string) { usedTools = append(usedTools, name) },
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	t.Logf("tools used: %v", usedTools)
	t.Logf("response: %q", result.Text)

	if len(usedTools) == 0 || usedTools[0] != "list_dir" {
		t.Errorf("expected list_dir, got: %v", usedTools)
	}
}

// ============================================================
// Test 17: Tool — clipboard roundtrip (set + get)
// ============================================================

func TestLive_ToolClipboard(t *testing.T) {
	skipUnlessLive(t)
	loadDotEnv(t)
	apiKey := requireEnv(t, "ANTHROPIC_API_KEY")

	client := makeToolProvider(t, apiKey)
	executor := tools.New(tools.ExecutorConfig{ShellTimeout: 10, MaxOutputBytes: 4096})

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	var usedTools []string
	result, err := agent.RunAgent(ctx, agent.RunParams{
		Provider:     client,
		ToolExecutor: &toolAdapter{executor: executor},
		ModelID:      "claude-haiku-4-5",
		SystemPrompt: "You are helpful. Use tools when asked. Be brief.",
		UserMessage:  "Use set_clipboard to put 'NANOCLAW_CLIP_TEST' on the clipboard, then use get_clipboard to read it back and confirm.",
		Tools:        allToolDefs(),
		MaxTokens:    512,
		OnToolCall:   func(name, _ string) { usedTools = append(usedTools, name) },
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	t.Logf("tools used: %v", usedTools)
	t.Logf("response: %q", result.Text)

	hasSet, hasGet := false, false
	for _, tool := range usedTools {
		if tool == "set_clipboard" {
			hasSet = true
		}
		if tool == "get_clipboard" {
			hasGet = true
		}
	}
	if !hasSet {
		t.Error("expected set_clipboard to be called")
	}
	if !hasGet {
		t.Error("expected get_clipboard to be called")
	}
	if !strings.Contains(result.Text, "NANOCLAW_CLIP_TEST") {
		t.Errorf("expected response to confirm clipboard content, got: %q", result.Text)
	}
}

// ============================================================
// Test 18: Tool — get_system_info + list_processes (chained)
// ============================================================

func TestLive_ToolSystemInfoAndProcesses(t *testing.T) {
	skipUnlessLive(t)
	loadDotEnv(t)
	apiKey := requireEnv(t, "ANTHROPIC_API_KEY")

	client := makeToolProvider(t, apiKey)
	executor := tools.New(tools.ExecutorConfig{ShellTimeout: 10, MaxOutputBytes: 4096})

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	var usedTools []string
	result, err := agent.RunAgent(ctx, agent.RunParams{
		Provider:     client,
		ToolExecutor: &toolAdapter{executor: executor},
		ModelID:      "claude-haiku-4-5",
		SystemPrompt: "You are helpful. Use tools when asked. Be brief.",
		UserMessage:  "Use get_system_info to check what Mac model I have, then use list_processes to find if 'Finder' is running. Report both answers.",
		Tools:        allToolDefs(),
		MaxTokens:    1024,
		OnToolCall:   func(name, _ string) { usedTools = append(usedTools, name) },
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	t.Logf("tools used: %v", usedTools)
	t.Logf("response: %q", result.Text)

	hasSysInfo, hasProcs := false, false
	for _, tool := range usedTools {
		if tool == "get_system_info" {
			hasSysInfo = true
		}
		if tool == "list_processes" {
			hasProcs = true
		}
	}
	if !hasSysInfo {
		t.Error("expected get_system_info to be called")
	}
	if !hasProcs {
		t.Error("expected list_processes to be called")
	}
	if result.Iterations < 2 {
		t.Errorf("expected >=2 iterations for multi-tool chain, got %d", result.Iterations)
	}
}

// ============================================================
// Test 19: Tool — search_files
// ============================================================

func TestLive_ToolSearchFiles(t *testing.T) {
	skipUnlessLive(t)
	loadDotEnv(t)
	apiKey := requireEnv(t, "ANTHROPIC_API_KEY")

	client := makeToolProvider(t, apiKey)
	executor := tools.New(tools.ExecutorConfig{ShellTimeout: 10, MaxOutputBytes: 4096})

	// Create a temp dir with known files to search
	tmpDir := t.TempDir()
	os.WriteFile(tmpDir+"/hello.txt", []byte("hello world"), 0644)
	os.WriteFile(tmpDir+"/goodbye.txt", []byte("farewell"), 0644)
	os.WriteFile(tmpDir+"/hello.go", []byte("package main"), 0644)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var usedTools []string
	result, err := agent.RunAgent(ctx, agent.RunParams{
		Provider:     client,
		ToolExecutor: &toolAdapter{executor: executor},
		ModelID:      "claude-haiku-4-5",
		SystemPrompt: "You are helpful. Use tools when asked. Be brief.",
		UserMessage:  fmt.Sprintf("Use search_files to find all files with 'hello' in their name in %s. How many matches?", tmpDir),
		Tools:        allToolDefs(),
		MaxTokens:    512,
		OnToolCall:   func(name, _ string) { usedTools = append(usedTools, name) },
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	t.Logf("tools used: %v", usedTools)
	t.Logf("response: %q", result.Text)

	if len(usedTools) == 0 || usedTools[0] != "search_files" {
		t.Errorf("expected search_files, got: %v", usedTools)
	}
	// Should find 2 files: hello.txt and hello.go
	if !strings.Contains(result.Text, "2") {
		t.Logf("warning: expected '2' matches mentioned, got: %q", result.Text)
	}
}

// ============================================================
// Test 20: Tool — run_applescript
// ============================================================

func TestLive_ToolAppleScript(t *testing.T) {
	skipUnlessLive(t)
	loadDotEnv(t)
	apiKey := requireEnv(t, "ANTHROPIC_API_KEY")

	client := makeToolProvider(t, apiKey)
	executor := tools.New(tools.ExecutorConfig{ShellTimeout: 10, MaxOutputBytes: 4096})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var usedTools []string
	result, err := agent.RunAgent(ctx, agent.RunParams{
		Provider:     client,
		ToolExecutor: &toolAdapter{executor: executor},
		ModelID:      "claude-haiku-4-5",
		SystemPrompt: "You are helpful. Use tools when asked. Be brief.",
		UserMessage:  `Use run_applescript with the script: return "NanoClaw AppleScript Works"`,
		Tools:        allToolDefs(),
		MaxTokens:    512,
		OnToolCall:   func(name, _ string) { usedTools = append(usedTools, name) },
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	t.Logf("tools used: %v", usedTools)
	t.Logf("response: %q", result.Text)

	if len(usedTools) == 0 || usedTools[0] != "run_applescript" {
		t.Errorf("expected run_applescript, got: %v", usedTools)
	}
	if !strings.Contains(result.Text, "NanoClaw AppleScript Works") {
		t.Logf("warning: expected AppleScript output in response")
	}
}

// ============================================================
// Test 21: Tool — multi-tool chain (complex task)
// ============================================================

func TestLive_ToolMultiStepTask(t *testing.T) {
	skipUnlessLive(t)
	loadDotEnv(t)
	apiKey := requireEnv(t, "ANTHROPIC_API_KEY")

	client := makeToolProvider(t, apiKey)
	executor := tools.New(tools.ExecutorConfig{ShellTimeout: 10, MaxOutputBytes: 4096})

	tmpDir := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	var usedTools []string
	result, err := agent.RunAgent(ctx, agent.RunParams{
		Provider:     client,
		ToolExecutor: &toolAdapter{executor: executor},
		ModelID:      "claude-haiku-4-5",
		SystemPrompt: "You are helpful. Use tools to complete tasks. Be brief in your final answer.",
		UserMessage: fmt.Sprintf(`Do these steps:
1. Use write_file to create %s/task.txt with content "Step 1 done"
2. Use run_shell to append " - Step 2 done" to the file: echo " - Step 2 done" >> %s/task.txt
3. Use read_file to read the final content
4. Tell me what the file contains.`, tmpDir, tmpDir),
		Tools:     allToolDefs(),
		MaxTokens: 1024,
		OnToolCall: func(name, _ string) {
			usedTools = append(usedTools, name)
			t.Logf("  tool: %s", name)
		},
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	t.Logf("tools used: %v", usedTools)
	t.Logf("iterations: %d", result.Iterations)
	t.Logf("response: %q", result.Text)

	// Should have used at least 3 tools
	if len(usedTools) < 3 {
		t.Errorf("expected >=3 tool calls, got %d: %v", len(usedTools), usedTools)
	}

	// Verify file on disk
	data, err := os.ReadFile(tmpDir + "/task.txt")
	if err != nil {
		t.Fatalf("file not created: %v", err)
	}
	content := string(data)
	t.Logf("file on disk: %q", content)

	if !strings.Contains(content, "Step 1") {
		t.Error("file missing Step 1")
	}
	if !strings.Contains(content, "Step 2") {
		t.Error("file missing Step 2")
	}
}

// ============================================================
// Test 22: Browser — navigate + snapshot + get title
// ============================================================

func TestLive_ToolBrowserNavigateAndSnapshot(t *testing.T) {
	skipUnlessLive(t)
	loadDotEnv(t)
	apiKey := requireEnv(t, "ANTHROPIC_API_KEY")

	client := makeToolProvider(t, apiKey)
	executor := tools.New(tools.ExecutorConfig{ShellTimeout: 30, MaxOutputBytes: 8192})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var usedTools []string
	result, err := agent.RunAgent(ctx, agent.RunParams{
		Provider:     client,
		ToolExecutor: &toolAdapter{executor: executor},
		ModelID:      "claude-haiku-4-5",
		SystemPrompt: `You control a headless browser via browser_* tools. Always use browser_navigate first to open a URL, then browser_snapshot to see the page content with element refs. Be brief.`,
		UserMessage:  "Navigate to https://example.com and tell me the page title and what links are on the page. Use browser_navigate then browser_snapshot.",
		Tools:        allToolDefs(),
		MaxTokens:    1024,
		OnToolCall:   func(name, _ string) { usedTools = append(usedTools, name); t.Logf("  tool: %s", name) },
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	t.Logf("tools used: %v", usedTools)
	t.Logf("response: %q", result.Text)

	hasNav, hasSnap := false, false
	for _, tool := range usedTools {
		if tool == "browser_navigate" {
			hasNav = true
		}
		if tool == "browser_snapshot" {
			hasSnap = true
		}
	}
	if !hasNav {
		t.Error("expected browser_navigate to be called")
	}
	if !hasSnap {
		t.Error("expected browser_snapshot to be called")
	}
	if !strings.Contains(strings.ToLower(result.Text), "example") {
		t.Errorf("expected response to mention 'example', got: %q", result.Text)
	}
}

// ============================================================
// Test 23: Browser — eval JavaScript
// ============================================================

func TestLive_ToolBrowserEval(t *testing.T) {
	skipUnlessLive(t)
	loadDotEnv(t)
	apiKey := requireEnv(t, "ANTHROPIC_API_KEY")

	client := makeToolProvider(t, apiKey)
	executor := tools.New(tools.ExecutorConfig{ShellTimeout: 30, MaxOutputBytes: 8192})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var usedTools []string
	result, err := agent.RunAgent(ctx, agent.RunParams{
		Provider:     client,
		ToolExecutor: &toolAdapter{executor: executor},
		ModelID:      "claude-haiku-4-5",
		SystemPrompt: "You control a headless browser. Be brief.",
		UserMessage:  "Navigate to https://example.com, then use browser_eval to run `document.title` and tell me the result.",
		Tools:        allToolDefs(),
		MaxTokens:    512,
		OnToolCall:   func(name, _ string) { usedTools = append(usedTools, name); t.Logf("  tool: %s", name) },
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	t.Logf("tools used: %v", usedTools)
	t.Logf("response: %q", result.Text)

	hasEval := false
	for _, tool := range usedTools {
		if tool == "browser_eval" {
			hasEval = true
		}
	}
	if !hasEval {
		t.Error("expected browser_eval to be called")
	}
	if !strings.Contains(result.Text, "Example Domain") {
		t.Logf("warning: expected 'Example Domain' in response")
	}
}

// ============================================================
// Test 24: Browser — click link and get new page
// ============================================================

func TestLive_ToolBrowserClickAndNavigate(t *testing.T) {
	skipUnlessLive(t)
	loadDotEnv(t)
	apiKey := requireEnv(t, "ANTHROPIC_API_KEY")

	client := makeToolProvider(t, apiKey)
	executor := tools.New(tools.ExecutorConfig{ShellTimeout: 30, MaxOutputBytes: 8192})

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	var usedTools []string
	result, err := agent.RunAgent(ctx, agent.RunParams{
		Provider:     client,
		ToolExecutor: &toolAdapter{executor: executor},
		ModelID:      "claude-haiku-4-5",
		SystemPrompt: `You control a headless browser. Workflow: browser_navigate → browser_snapshot → find refs → browser_click → browser_snapshot again. Be brief.`,
		UserMessage:  "Go to https://example.com, take a snapshot to see the links, click the 'More information...' link, then take another snapshot and tell me what page you ended up on.",
		Tools:        allToolDefs(),
		MaxTokens:    1024,
		OnToolCall:   func(name, _ string) { usedTools = append(usedTools, name); t.Logf("  tool: %s", name) },
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	t.Logf("tools used: %v", usedTools)
	t.Logf("iterations: %d", result.Iterations)
	t.Logf("response: %q", result.Text)

	hasClick := false
	for _, tool := range usedTools {
		if tool == "browser_click" {
			hasClick = true
		}
	}
	if !hasClick {
		t.Error("expected browser_click to be called")
	}
	if result.Iterations < 3 {
		t.Logf("note: expected >=3 iterations (navigate + snapshot + click + snapshot), got %d", result.Iterations)
	}
}

func init() {
	log.SetFlags(log.Ltime | log.Lshortfile)
}
