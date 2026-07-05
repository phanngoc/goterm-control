package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ngocp/goterm-control/internal/agent"
)

// CLIProvider implements agent.ModelProvider using the claude CLI subprocess.
// This works with OAuth subscription tokens (sk-ant-oat01-...) that can't be
// used directly with the Anthropic REST API.
//
// IMPORTANT: The CLI manages its own tool loop internally — it calls tools like
// Bash, Read, Edit etc. on its own. Our browser_* tools are NOT available to the
// CLI directly. For browser automation with OAuth tokens, the agent loop should
// use this provider for text generation, and execute browser tools locally.
type CLIProvider struct {
	// workspace is the CLI subprocess working directory. Critical on macOS:
	// project-level permission deny rules (.claude/settings.json) load from
	// the cwd, and file searches root there — a launchd-inherited cwd of "/"
	// would skip the deny rules and scan TCC-protected folders.
	workspace string
}

// NewCLIProvider creates a provider whose CLI subprocesses run in workspace.
// An empty workspace falls back to ~/goterm-workspace.
func NewCLIProvider(workspace string) *CLIProvider {
	if workspace == "" {
		home, _ := os.UserHomeDir()
		workspace = filepath.Join(home, "goterm-workspace")
	}
	return &CLIProvider{workspace: workspace}
}

// Stream spawns `claude -p --output-format stream-json` and emits events.
// The CLI runs in prompt mode: single user message in, streamed response out.
// Tool calls from the CLI (Bash, Read, etc.) are handled internally by the CLI.
// Our custom tools (browser_*, etc.) are NOT available — the caller (agent loop)
// must handle those by re-prompting with tool results.
func (p *CLIProvider) Stream(ctx context.Context, params agent.StreamParams) (<-chan agent.StreamEvent, error) {
	// Build the user message from conversation history
	prompt := lastUserMessage(params.Messages)
	if prompt == "" {
		return nil, fmt.Errorf("no user message found")
	}

	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--verbose",
		"--model", params.Model,
		"--mcp-config", emptyMCPConfig,
		"--strict-mcp-config",
	}

	// Always carry the file-access guard so the CLI's own tools stay out of
	// TCC-protected folders (same rule as the bot path in client.go).
	args = append(args, "--append-system-prompt", params.SystemPrompt+fsGuardPrompt)

	cmd := exec.CommandContext(ctx, claudeBin, args...)
	// Run in the workspace so its .claude/settings.json deny rules apply and
	// searches root there instead of the launchd cwd ("/").
	_ = os.MkdirAll(p.workspace, 0755)
	cmd.Dir = p.workspace
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Env = filteredEnv(envVarsToRemove)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start claude: %w", err)
	}

	go func() {
		s := bufio.NewScanner(stderr)
		for s.Scan() {
			log.Printf("claude-cli stderr: %s", s.Text())
		}
	}()

	ch := make(chan agent.StreamEvent, 64)
	go func() {
		defer close(ch)
		defer cmd.Wait()

		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)

		for scanner.Scan() {
			if ctx.Err() != nil {
				cmd.Process.Kill()
				return
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
			case "assistant":
				if ev.Message == nil {
					continue
				}
				for _, b := range ev.Message.Content {
					switch b.Type {
					case "text":
						if b.Text != "" {
							ch <- agent.StreamEvent{Type: "text", Text: b.Text}
						}
					case "tool_use":
						ch <- agent.StreamEvent{
							Type:      "tool_use",
							ToolID:    b.ID,
							ToolName:  b.Name,
							ToolInput: b.Input,
						}
					}
				}

			case "result":
				if ev.IsError {
					ch <- agent.StreamEvent{Type: "error", Error: fmt.Errorf("claude: %s", ev.Result)}
					return
				}
				ch <- agent.StreamEvent{Type: "end", StopReason: "end_turn"}
			}
		}

		if err := scanner.Err(); err != nil {
			ch <- agent.StreamEvent{Type: "error", Error: fmt.Errorf("scan: %w", err)}
		}
	}()

	return ch, nil
}

func lastUserMessage(messages []agent.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" && messages[i].Content != "" {
			return messages[i].Content
		}
	}
	return ""
}
