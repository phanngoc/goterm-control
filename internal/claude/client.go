package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"

	"github.com/ngocp/goterm-control/internal/session"
	"github.com/ngocp/goterm-control/internal/tools"
)

// claudeBin is the name of the Claude Code CLI binary.
const claudeBin = "claude"

// envVarsToRemove are cleared before spawning so the CLI uses its own
// OAuth subscription instead of the API key (matches openclaw CLAUDE_CLI_CLEAR_ENV).
var envVarsToRemove = []string{"ANTHROPIC_API_KEY", "ANTHROPIC_API_KEY_OLD"}

// emptyMCPConfig prevents the CLI from loading user MCP servers (Serena, etc.)
// which would cause browser popups and unnecessary overhead.
const emptyMCPConfig = `{"mcpServers":{}}`

// StreamCallbacks lets the bot layer react to Claude events.
type StreamCallbacks struct {
	OnText       func(chunk string)
	OnToolCall   func(name string, inputJSON string)
	OnToolResult func(name string, result tools.ToolResult)
}

// Client wraps the claude CLI subprocess.
type Client struct {
	model        string
	systemPrompt string
}

// New creates a Claude client backed by the claude CLI subprocess.
// apiKey, maxTokens, and executor are unused — the CLI manages auth and tools.
func New(apiKey, model string, maxTokens int, systemPrompt string, executor *tools.Executor) *Client {
	log.Printf("claude: subprocess client (model=%s)", model)
	return &Client{
		model:        model,
		systemPrompt: systemPrompt,
	}
}

// --- stream-json event types from claude CLI ---

type streamEvent struct {
	Type      string      `json:"type"`
	Message   *cliMessage `json:"message,omitempty"`
	SessionID string      `json:"session_id,omitempty"`
	Result    string      `json:"result,omitempty"`
	IsError   bool        `json:"is_error,omitempty"`
}

type cliMessage struct {
	Role    string     `json:"role"`
	Content []cliBlock `json:"content"`
}

type cliBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Name      string          `json:"name,omitempty"`
	ID        string          `json:"id,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type pendingCall struct {
	name    string
	command string // bash command text (for screenshot detection)
}

// SendMessage sends userText to the claude CLI and streams events via callbacks.
func (c *Client) SendMessage(ctx context.Context, sess *session.Session, userText string, cb StreamCallbacks) error {
	sessionID := sess.GetSessionID()
	isNewSession := sessionID == ""

	args := buildArgs(c.model, sessionID, isNewSession, c.systemPrompt)

	cmd := exec.CommandContext(ctx, claudeBin, args...)

	// Pass user message via stdin (safe for arbitrary text).
	cmd.Stdin = strings.NewReader(userText)

	// Remove ANTHROPIC_API_KEY so CLI uses its own OAuth subscription.
	cmd.Env = filteredEnv(envVarsToRemove)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start claude: %w", err)
	}

	// Drain stderr to logs.
	go func() {
		s := bufio.NewScanner(stderr)
		for s.Scan() {
			log.Printf("claude stderr: %s", s.Text())
		}
	}()

	pending := map[string]pendingCall{}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)

	for scanner.Scan() {
		if ctx.Err() != nil {
			_ = cmd.Process.Kill()
			break
		}
		line := scanner.Text()
		if line == "" {
			continue
		}

		var ev streamEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}

		switch ev.Type {
		case "system":
			if ev.SessionID != "" && isNewSession {
				sess.SetSessionID(ev.SessionID)
				log.Printf("claude: session_id=%s", ev.SessionID)
			}

		case "assistant":
			if ev.Message == nil {
				continue
			}
			for _, b := range ev.Message.Content {
				switch b.Type {
				case "text":
					if b.Text != "" && cb.OnText != nil {
						cb.OnText(b.Text)
					}
				case "tool_use":
					p := pendingCall{name: b.Name}
					if b.Name == "Bash" {
						var inp struct {
							Command string `json:"command"`
						}
						if json.Unmarshal(b.Input, &inp) == nil {
							p.command = inp.Command
						}
					}
					pending[b.ID] = p
					if cb.OnToolCall != nil {
						cb.OnToolCall(b.Name, formatInput(b.Input))
					}
				}
			}

		case "user":
			if ev.Message == nil {
				continue
			}
			for _, b := range ev.Message.Content {
				if b.Type != "tool_result" {
					continue
				}
				p, _ := pending[b.ToolUseID]
				delete(pending, b.ToolUseID)

				result := tools.ToolResult{
					Output:  b.Content,
					IsError: b.IsError,
				}
				// Screenshot detection: bash ran screencapture → send as photo.
				if strings.Contains(p.command, "screencapture") {
					if path := extractScreenshotPath(p.command); path != "" {
						result.IsImage = true
						result.ImagePath = path
						result.Output = "screenshot at " + path
					}
				}
				if cb.OnToolResult != nil {
					cb.OnToolResult(p.name, result)
				}
			}

		case "result":
			sess.IncrementMessages()
			if ev.IsError {
				return fmt.Errorf("claude error: %s", ev.Result)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan: %w", err)
	}

	return cmd.Wait()
}

// buildArgs constructs the claude CLI argument list.
func buildArgs(model, sessionID string, isNew bool, systemPrompt string) []string {
	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
		"--permission-mode", "bypassPermissions",
		"--model", model,
		// Disable user MCP servers to prevent Serena/etc. startup browser popups.
		"--mcp-config", emptyMCPConfig,
		"--strict-mcp-config",
	}

	if !isNew {
		args = append(args, "--resume", sessionID)
	} else if systemPrompt != "" {
		// --append-system-prompt on first message only (openclaw pattern).
		args = append(args, "--append-system-prompt", systemPrompt)
	}

	return args
}

// filteredEnv returns the current process env minus the given keys.
func filteredEnv(remove []string) []string {
	skip := make(map[string]bool, len(remove))
	for _, k := range remove {
		skip[k] = true
	}
	var env []string
	for _, kv := range os.Environ() {
		key := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			key = kv[:i]
		}
		if !skip[key] {
			env = append(env, kv)
		}
	}
	return env
}

// extractScreenshotPath finds the .png/.jpg path in a screencapture command.
func extractScreenshotPath(cmd string) string {
	for _, p := range strings.Fields(cmd) {
		if strings.HasPrefix(p, "-") {
			continue
		}
		if p == "screencapture" {
			continue
		}
		if strings.Contains(p, ".png") || strings.Contains(p, ".jpg") {
			return p
		}
	}
	return ""
}

func formatInput(raw json.RawMessage) string {
	var pretty map[string]any
	if err := json.Unmarshal(raw, &pretty); err != nil {
		return string(raw)
	}
	b, _ := json.MarshalIndent(pretty, "", "  ")
	return string(b)
}
