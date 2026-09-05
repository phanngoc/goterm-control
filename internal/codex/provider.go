package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"

	"github.com/ngocp/goterm-control/internal/agent"
)

// CLIProvider implements agent.ModelProvider using the codex CLI subprocess.
//
// This is the one-shot text path used by the session titler and the `chat`
// command — not the conversational bot path (that is Client, which keeps a
// codex thread alive across turns). Because callers here only want text back,
// the subprocess runs read-only: it must not execute commands to answer.
type CLIProvider struct {
	// workspace is the CLI subprocess working directory, so codex reads the
	// agent's own project context rather than the launchd cwd ("/").
	workspace string
}

// NewCLIProvider creates a provider whose CLI subprocesses run in workspace.
// An empty workspace falls back to ~/goterm-workspace.
func NewCLIProvider(workspace string) *CLIProvider {
	if workspace == "" {
		workspace = defaultWorkspace()
	}
	return &CLIProvider{workspace: workspace}
}

// Stream spawns `codex exec --json` and emits events. Every call starts a fresh
// ephemeral thread: there is no session to resume, and persisting throwaway
// title-generation threads would clutter the user's codex history.
func (p *CLIProvider) Stream(ctx context.Context, params agent.StreamParams) (<-chan agent.StreamEvent, error) {
	prompt := lastUserMessage(params.Messages)
	if prompt == "" {
		return nil, fmt.Errorf("no user message found")
	}
	if params.SystemPrompt != "" {
		prompt = params.SystemPrompt + fsGuardPrompt + "\n\n---\n\n" + prompt
	}

	args := []string{
		"exec",
		"--json",
		"--skip-git-repo-check",
		// Text-only callers: no shell, no writes, no approval prompts to hang on.
		"--sandbox", "read-only",
		// Do not leave a saved session behind for a one-shot call.
		"--ephemeral",
	}
	if params.Model != "" {
		args = append(args, "-m", params.Model)
	}
	args = append(args, "-")

	cmd := exec.CommandContext(ctx, codexBin, args...)
	_ = os.MkdirAll(p.workspace, 0755)
	cmd.Dir = p.workspace
	cmd.Stdin = strings.NewReader(prompt)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start codex: %w", err)
	}

	go func() {
		s := bufio.NewScanner(stderr)
		for s.Scan() {
			log.Printf("codex-cli stderr: %s", s.Text())
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
				_ = cmd.Process.Kill()
				return
			}
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}

			var ev event
			if err := json.Unmarshal([]byte(line), &ev); err != nil {
				continue
			}

			switch ev.Type {
			case "item.completed":
				if ev.Item == nil {
					continue
				}
				switch ev.Item.Type {
				case "agent_message", "assistant_message":
					if ev.Item.Text != "" {
						ch <- agent.StreamEvent{Type: "text", Text: ev.Item.Text}
					}
				case "error":
					log.Printf("codex item error: %s", ev.Item.Message)
				}

			case "turn.completed":
				end := agent.StreamEvent{Type: "end", StopReason: "end_turn"}
				if ev.Usage != nil {
					end.Usage = &agent.Usage{
						InputTokens:  ev.Usage.InputTokens,
						OutputTokens: ev.Usage.OutputTokens,
						CacheRead:    ev.Usage.CachedInputTokens,
					}
				}
				ch <- end
				return

			case "turn.failed":
				msg := "turn failed"
				if ev.Error != nil && ev.Error.Message != "" {
					msg = ev.Error.Message
				}
				ch <- agent.StreamEvent{Type: "error", Error: fmt.Errorf("codex: %s", msg)}
				return

			case "error":
				ch <- agent.StreamEvent{Type: "error", Error: fmt.Errorf("codex: %s", ev.Message)}
				return
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
