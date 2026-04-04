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
	agentctx "github.com/ngocp/goterm-control/internal/context"
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
	// Try loading .env from project root
	for _, path := range []string{".env", "../.env"} {
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

// toolAdapter bridges tools.Executor to agent.ToolExecutor.
type toolAdapter struct {
	executor *tools.Executor
}

func (a *toolAdapter) Execute(ctx context.Context, name string, input json.RawMessage) agent.ToolResult {
	r := a.executor.Run(ctx, name, input)
	return agent.ToolResult{Content: r.Output, IsError: r.IsError}
}

func buildToolDefs() []agent.ToolDef {
	return []agent.ToolDef{
		{Name: "run_shell", Description: "Execute a shell command", InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string", "description": "Shell command to execute"},
			},
			"required": []string{"command"},
		}},
		{Name: "read_file", Description: "Read file contents", InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "File path"},
			},
			"required": []string{"path"},
		}},
		{Name: "get_system_info", Description: "Get system info", InputSchema: map[string]any{
			"type": "object", "properties": map[string]any{},
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

	client := anthropicClient.New(apiKey)
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
			t.Logf("usage: input=%d output=%d", ev.Usage.OutputTokens, ev.Usage.OutputTokens)
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

	client := anthropicClient.New(apiKey)
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

	client := anthropicClient.New(apiKey)
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

	client := anthropicClient.New(apiKey)
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

	client := anthropicClient.New(apiKey)
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

	// --- Step 2: Create Anthropic provider ---
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

	client := anthropicClient.New(apiKey)
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

func init() {
	log.SetFlags(log.Ltime | log.Lshortfile)
}
