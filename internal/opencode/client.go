// Package opencode runs a turn through the opencode CLI
// (https://github.com/anomalyco/opencode) as a subprocess, the way the claude
// and codex packages do.
//
// opencode also has a `serve` mode with an HTTP API, and it is deliberately not
// used. A subprocess is the shape the other two already have, so it inherits
// the process-group cleanup that keeps a CLI from outliving the gateway; a
// server is a second lifetime to supervise, and nothing here needs one yet.
package opencode

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/ngocp/goterm-control/internal/chat"
	"github.com/ngocp/goterm-control/internal/credentials"
	"github.com/ngocp/goterm-control/internal/execution"
	"github.com/ngocp/goterm-control/internal/models"
	"github.com/ngocp/goterm-control/internal/session"
	"github.com/ngocp/goterm-control/internal/tools"
)

// opencodeBin is the name of the opencode CLI binary.
const opencodeBin = "opencode"

// ProviderName is the session-storage key for this provider.
const ProviderName = "opencode"

// Client wraps the opencode CLI subprocess.
type Client struct {
	systemPrompt string
	pool         *credentials.Pool
	workspace    string
}

// New creates an opencode client. The CLI owns its own auth and its own tool
// loop, exactly as codex does.
func New(systemPrompt string) *Client {
	log.Printf("opencode: subprocess client initialized")
	return &Client{systemPrompt: systemPrompt}
}

// SetPool attaches the credential pool this client rotates sessions across.
func (c *Client) SetPool(p *credentials.Pool) { c.pool = p }

// SetWorkspace sets the working directory for spawned CLI processes.
func (c *Client) SetWorkspace(dir string) { c.workspace = dir }

// Name identifies this provider in session storage.
func (c *Client) Name() string { return ProviderName }

func (c *Client) account(sess *session.Session) (credentials.Account, error) {
	if c.pool == nil || c.pool.Empty() {
		return credentials.Account{}, nil
	}
	acct, err := c.pool.Pick(sess.GetAccount())
	if err != nil {
		return credentials.Account{}, err
	}
	sess.SetAccount(acct.Name)
	c.pool.MarkStarted(acct.Name)
	return acct, nil
}

func (c *Client) recordOutcome(acct credentials.Account, err error) {
	if c.pool == nil || acct.Name == "" {
		return
	}
	switch {
	case err == nil:
		c.pool.MarkSuccess(acct.Name)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
	default:
		c.pool.MarkFailure(acct.Name, err)
	}
}

// SendMessage runs one turn and streams its events through cb.
//
// opencode has no flag for an extra system prompt, so like codex the prompt and
// memory are folded into the first message of a session; the session carries
// them from then on.
func (c *Client) SendMessage(ctx context.Context, sess *session.Session, modelID string,
	userText string, memoryContext string, cb chat.StreamCallbacks) (err error) {

	acct, err := c.account(sess)
	if err != nil {
		return err
	}
	defer func() { c.recordOutcome(acct, err) }()

	sessionID := sess.GetSessionID()
	// A session belonging to another CLI cannot be resumed here. Starting a
	// fresh one is the only option that does not fail every turn after a
	// provider switch.
	isNew := sessionID == "" || sess.GetProvider() != ProviderName

	prompt := userText
	if isNew {
		prompt = c.firstTurnPrompt(userText, memoryContext)
	}

	cmd := exec.CommandContext(ctx, opencodeBin, buildArgs(modelID, sessionID, isNew)...)
	execution.Detach(cmd)
	cmd.Env = credentials.ApplyEnv(os.Environ(), acct)
	if c.workspace != "" {
		_ = os.MkdirAll(c.workspace, 0755)
		cmd.Dir = c.workspace
	}
	// The message goes on stdin rather than argv: a prompt with a newline, a
	// quote or a leading dash is ordinary here and would be a quoting bug there.
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
		return fmt.Errorf("start opencode: %w", err)
	}
	defer execution.Track(cmd)()

	var lastErrLine string
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		s := bufio.NewScanner(stderr)
		for s.Scan() {
			if line := s.Text(); line != "" {
				lastErrLine = line
				log.Printf("opencode stderr: %s", line)
			}
		}
	}()

	// Every exit path reaps the child: returning on a stream error without
	// waiting leaves a zombie and a goroutine blocked on a pipe that never
	// closes, one per failed turn.
	var (
		waitOnce sync.Once
		waitErr  error
	)
	reap := func() error {
		waitOnce.Do(func() {
			waitErr = cmd.Wait()
			<-stderrDone
		})
		return waitErr
	}
	abort := func(err error) error {
		if cmd.Process != nil {
			_ = execution.KillGroup(cmd.Process)
		}
		_ = reap()
		return err
	}
	defer reap()

	sawText := false
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)

	for scanner.Scan() {
		if ctx.Err() != nil {
			_ = execution.KillGroup(cmd.Process)
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var ev event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue // the CLI also prints non-JSON diagnostics on stdout
		}

		// Every event carries the session id, so the first one is enough — and
		// taking it from any event rather than a dedicated "started" type means
		// a turn whose opening event we do not handle still records where to
		// resume from.
		if isNew && ev.SessionID != "" && sess.GetSessionID() == "" {
			sess.SetSessionID(ev.SessionID)
			sess.SetProvider(ProviderName)
			log.Printf("opencode: session_id=%s", ev.SessionID)
		}

		switch ev.Type {
		case "text":
			if ev.Part == nil || ev.Part.Text == "" {
				continue
			}
			sawText = true
			if cb.OnText != nil {
				cb.OnText(ev.Part.Text)
			}

		case "tool_use":
			if ev.Part == nil || ev.Part.State == nil {
				continue
			}
			name := ev.Part.Tool
			if name == "" {
				name = "tool"
			}
			// opencode reports a tool once, already finished — there is no
			// started event to pair with. So the call and its result are
			// announced together rather than the sink being told about a call
			// that, as far as it knows, never came back.
			if cb.OnToolCall != nil {
				cb.OnToolCall(name, ev.Part.State.inputJSON())
			}
			if cb.OnToolResult != nil {
				cb.OnToolResult(name, toolResult(ev.Part.State))
			}

		case "error":
			return abort(fmt.Errorf("opencode error: %s", errorText(ev.Error, lastErrLine)))
		}
	}
	if err := scanner.Err(); err != nil {
		return abort(fmt.Errorf("read opencode output: %w", err))
	}

	if err := reap(); err != nil {
		// A named session the CLI does not know is the one failure the bot
		// layer handles rather than surfaces: it starts a fresh session instead
		// of leaving the conversation permanently broken.
		if !isNew && sessionNotFound(lastErrLine) {
			return chat.ErrSessionNotFound
		}
		if lastErrLine != "" {
			return fmt.Errorf("opencode: %w (%s)", err, lastErrLine)
		}
		return fmt.Errorf("opencode: %w", err)
	}
	if !sawText {
		log.Printf("opencode: turn produced no text (stderr: %s)", lastErrLine)
	}
	sess.IncrementMessages()
	return nil
}

// buildArgs assembles the CLI invocation. The message itself is not here: it
// arrives on stdin.
func buildArgs(modelID, sessionID string, isNew bool) []string {
	args := []string{"run", "--format", "json"}
	if modelID != "" {
		args = append(args, "--model", modelID)
	}
	if !isNew && sessionID != "" {
		args = append(args, "--session", sessionID)
	}
	return args
}

// firstTurnPrompt folds the system prompt and memory into the opening message,
// because opencode has no flag that carries them separately. Later turns resume
// a session that already holds them.
func (c *Client) firstTurnPrompt(userText, memoryContext string) string {
	var b strings.Builder
	if p := strings.TrimSpace(c.systemPrompt); p != "" {
		b.WriteString(p)
		b.WriteString("\n\n")
	}
	if m := strings.TrimSpace(memoryContext); m != "" {
		b.WriteString(m)
		b.WriteString("\n\n")
	}
	b.WriteString(userText)
	return b.String()
}

func toolResult(s *toolState) tools.ToolResult {
	if s.Status == "error" || s.Error != "" {
		msg := s.Error
		if msg == "" {
			msg = "tool failed"
		}
		return tools.ToolResult{Output: msg, IsError: true}
	}
	return tools.ToolResult{Output: s.Output}
}

func errorText(raw json.RawMessage, fallback string) string {
	if len(raw) > 0 {
		var msg struct {
			Message string `json:"message"`
			Name    string `json:"name"`
		}
		if err := json.Unmarshal(raw, &msg); err == nil {
			if msg.Message != "" {
				return msg.Message
			}
			if msg.Name != "" {
				return msg.Name
			}
		}
		return string(raw)
	}
	if fallback != "" {
		return fallback
	}
	return "unknown error"
}

// sessionNotFound recognises the CLI's own words for a session id it cannot
// resume. Matched loosely on purpose: the alternative is treating a stale id as
// a hard failure, which breaks a conversation permanently rather than for one
// turn.
func sessionNotFound(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "session not found") || strings.Contains(s, "no such session")
}

// Registering here is what lets internal/bot stay ignorant of this package;
// config imports it for the side effect.
func init() {
	chat.Register(models.APIOpenCodeCLI, func(d chat.Deps) chat.Client {
		c := New(d.SystemPrompt)
		c.SetWorkspace(d.Workspace)
		c.SetPool(d.Pool)
		return c
	})
}
