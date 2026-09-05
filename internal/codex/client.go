// Package codex drives the OpenAI Codex CLI (`codex exec`) as a chat backend,
// mirroring internal/claude so the bot can run either one.
//
// Event contract: `codex exec --json` writes one JSON object per line. Top-level
// events are thread.started / turn.started / turn.completed / turn.failed /
// item.{started,updated,completed} / error; the item payload carries its own
// `type` (agent_message, reasoning, command_execution, file_change,
// mcp_tool_call, web_search, todo_list, error). Verified against codex-cli
// 0.153.4.
package codex

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

	"github.com/ngocp/goterm-control/internal/chat"
	"github.com/ngocp/goterm-control/internal/session"
	"github.com/ngocp/goterm-control/internal/tools"
)

// codexBin is the name of the Codex CLI binary.
const codexBin = "codex"

// ProviderName is the session-storage key for the Codex CLI provider.
const ProviderName = "codex"

// Client wraps the codex CLI subprocess.
type Client struct {
	systemPrompt string
	workspace    string // working directory for the CLI subprocess
}

// New creates a Codex client backed by the codex CLI subprocess.
// The CLI manages its own auth (`codex login`) and its own tool loop.
func New(systemPrompt string) *Client {
	log.Printf("codex: subprocess client initialized")
	return &Client{systemPrompt: systemPrompt}
}

// SetWorkspace sets the working directory for spawned CLI processes.
func (c *Client) SetWorkspace(dir string) { c.workspace = dir }

// Name identifies this provider in session storage.
func (c *Client) Name() string { return ProviderName }

// SendMessage sends userText to the codex CLI and streams events via callbacks.
//
// Codex has no equivalent of claude's --append-system-prompt, so on a new thread
// the system prompt and memory are folded into the first user message; the
// thread carries them in its history from then on. (A future option is writing
// them to <workspace>/AGENTS.md, which codex loads automatically — that writes
// into the user's workspace, so it is deliberately not done here.)
func (c *Client) SendMessage(ctx context.Context, sess *session.Session, modelID string,
	userText string, memoryContext string, cb chat.StreamCallbacks) error {

	threadID := sess.GetSessionID()
	// A thread started by another CLI cannot be resumed by codex — a provider
	// switch must begin a fresh thread rather than fail every turn.
	isNewThread := threadID == "" || sess.GetProvider() != ProviderName

	prompt := userText
	if isNewThread {
		prompt = c.firstTurnPrompt(userText, memoryContext)
	}

	args := buildArgs(modelID, threadID, isNewThread)

	cmd := exec.CommandContext(ctx, codexBin, args...)
	if c.workspace != "" {
		_ = os.MkdirAll(c.workspace, 0755)
		cmd.Dir = c.workspace
	}
	cmd.Stdin = strings.NewReader(prompt)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start codex: %w", err)
	}

	// Drain stderr to logs. Codex logs auth/model diagnostics here; the last
	// lines are the only clue when a turn dies before emitting any event.
	var lastErrLine string
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		s := bufio.NewScanner(stderr)
		for s.Scan() {
			line := s.Text()
			if line == "" {
				continue
			}
			lastErrLine = line
			log.Printf("codex stderr: %s", line)
		}
	}()

	// command_execution items arrive as started → completed; keep the command
	// text so the completion can be reported (and screenshots detected).
	pending := map[string]string{}
	sawTurn := false

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)

	for scanner.Scan() {
		if ctx.Err() != nil {
			_ = cmd.Process.Kill()
			break
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
		case "thread.started":
			if ev.ThreadID != "" && isNewThread {
				sess.SetSessionID(ev.ThreadID)
				sess.SetProvider(ProviderName)
				log.Printf("codex: thread_id=%s", ev.ThreadID)
			}

		case "item.started":
			if ev.Item == nil {
				continue
			}
			switch ev.Item.Type {
			case "command_execution":
				pending[ev.Item.ID] = ev.Item.Command
				if cb.OnToolCall != nil {
					cb.OnToolCall("Bash", ev.Item.Command)
				}
			case "mcp_tool_call":
				if cb.OnToolCall != nil {
					cb.OnToolCall(mcpToolName(ev.Item), formatInput(ev.Item.Arguments))
				}
			}

		case "item.completed":
			if ev.Item == nil {
				continue
			}
			c.handleCompletedItem(ev.Item, pending, cb)

		case "turn.completed":
			sawTurn = true
			sess.IncrementMessages()
			if ev.Usage != nil {
				sess.AddTokens(ev.Usage.InputTokens, ev.Usage.OutputTokens)
				// Context size proxy for the threshold memory flush: what this
				// call actually carried, cached prompt tokens included.
				sess.SetLastContextTokens(ev.Usage.InputTokens + ev.Usage.CachedInputTokens)
			}

		case "turn.failed":
			msg := "turn failed"
			if ev.Error != nil && ev.Error.Message != "" {
				msg = ev.Error.Message
			}
			return fmt.Errorf("codex error: %s", msg)

		case "error":
			if ev.Message != "" {
				return fmt.Errorf("codex error: %s", ev.Message)
			}
			return fmt.Errorf("codex error: unspecified stream error")
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan: %w", err)
	}

	waitErr := cmd.Wait()
	<-stderrDone

	// A non-zero exit with no turn.completed means codex died before doing any
	// work (bad auth, unknown model). Surface the stderr tail — without it the
	// user only sees "exit status 1".
	if waitErr != nil && !sawTurn {
		if lastErrLine != "" {
			return fmt.Errorf("codex error: %s", lastErrLine)
		}
		return fmt.Errorf("codex: %w", waitErr)
	}
	return waitErr
}

// handleCompletedItem maps one finished codex item onto the bot callbacks.
func (c *Client) handleCompletedItem(it *threadItem, pending map[string]string, cb chat.StreamCallbacks) {
	switch it.Type {
	case "agent_message", "assistant_message":
		// Codex emits the reply as one completed item rather than deltas, so
		// this fires once per turn with the full text.
		if it.Text != "" && cb.OnText != nil {
			cb.OnText(it.Text)
		}

	case "command_execution":
		command := pending[it.ID]
		if command == "" {
			command = it.Command
		}
		delete(pending, it.ID)

		result := tools.ToolResult{
			Output:  it.AggregatedOutput,
			IsError: it.Status == "failed" || (it.ExitCode != nil && *it.ExitCode != 0),
		}
		// Screenshot detection: a shell screencapture → send the file as a photo.
		if strings.Contains(command, "screencapture") {
			if path := chat.ExtractScreenshotPath(command); path != "" {
				result.IsImage = true
				result.ImagePath = path
				result.Output = "screenshot at " + path
			}
		}
		if cb.OnToolResult != nil {
			cb.OnToolResult("Bash", result)
		}

	case "file_change":
		if cb.OnToolResult == nil {
			return
		}
		var b strings.Builder
		for _, ch := range it.Changes {
			fmt.Fprintf(&b, "%s %s\n", ch.Kind, ch.Path)
		}
		cb.OnToolResult("Edit", tools.ToolResult{
			Output:  strings.TrimRight(b.String(), "\n"),
			IsError: it.Status == "failed",
		})

	case "mcp_tool_call":
		if cb.OnToolResult == nil {
			return
		}
		out := string(it.Result)
		if it.Error != "" {
			out = it.Error
		}
		cb.OnToolResult(mcpToolName(it), tools.ToolResult{
			Output:  out,
			IsError: it.Status == "failed" || it.Error != "",
		})

	case "web_search":
		if cb.OnToolCall != nil {
			cb.OnToolCall("WebSearch", it.Query)
		}

	case "error":
		// Item-level errors are warnings (e.g. model metadata missing); the
		// turn continues, so log rather than abort.
		log.Printf("codex item error: %s", it.Message)
	}
}

// firstTurnPrompt folds the operating instructions and cross-session memory
// into the opening message of a new thread.
func (c *Client) firstTurnPrompt(userText, memoryContext string) string {
	var b strings.Builder
	if c.systemPrompt != "" {
		b.WriteString("# Operating instructions\n\n")
		b.WriteString(c.systemPrompt)
		b.WriteString(fsGuardPrompt)
		if memoryContext != "" {
			b.WriteString("\n")
			b.WriteString(memoryContext)
		}
		b.WriteString("\n\n---\n\n# Message\n\n")
	}
	b.WriteString(userText)
	return b.String()
}

// buildArgs constructs the codex CLI argument list.
// The trailing "-" makes codex read the prompt from stdin, which is safe for
// arbitrary text (a prompt passed as argv would hit ARG_MAX and quoting bugs).
func buildArgs(model, threadID string, isNew bool) []string {
	args := []string{"exec"}
	if !isNew {
		args = append(args, "resume", threadID)
	}
	args = append(args,
		"--json",
		// The workspace is not necessarily a git repo; without this codex
		// refuses to run outside one.
		"--skip-git-repo-check",
		// Parity with the claude path's --permission-mode bypassPermissions:
		// this is a headless agent that is expected to drive the machine.
		"--dangerously-bypass-approvals-and-sandbox",
	)
	if model != "" {
		args = append(args, "-m", model)
	}
	return append(args, "-")
}

// fsGuardPrompt keeps the CLI's own file tools away from macOS TCC-protected
// folders. The gateway runs headless under launchd, so a stray recursive scan
// of $HOME pops permission dialogs the user cannot see the reason for.
const fsGuardPrompt = "\n\n## File access\n" +
	"- Never scan, list, or search ~/Music, ~/Pictures, ~/Movies, or ~/Downloads " +
	"unless the user explicitly asks for a file there.\n" +
	"- Avoid recursive searches rooted at the home directory (~); root them at the " +
	"workspace or a specific project directory instead.\n"

func mcpToolName(it *threadItem) string {
	if it.Server == "" {
		return it.Tool
	}
	return it.Server + "." + it.Tool
}

func formatInput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var pretty map[string]any
	if err := json.Unmarshal(raw, &pretty); err != nil {
		return string(raw)
	}
	b, _ := json.MarshalIndent(pretty, "", "  ")
	return string(b)
}

// defaultWorkspace is used when no workspace is configured.
func defaultWorkspace() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "goterm-workspace")
}
