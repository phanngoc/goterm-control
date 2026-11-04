package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// Hooks around tool calls.
//
// The tool calls happen inside the CLI, so the gateway never sees one in time
// to stop it. But all three CLIs run hooks of their own, and each accepts them
// per invocation — claude through --settings, codex through -c, opencode
// through a plugin named in OPENCODE_CONFIG_CONTENT. So a plugin declares a
// hook once, and every backend is pointed at ONE dispatcher, `bomclaw hook`,
// which:
//
//  1. reads the backend's payload and normalises it — tool names differ
//     (Bash / bash / shell), and so do the shapes;
//  2. runs the matching hooks of this turn's trusted, active plugins;
//  3. answers in whatever form that backend understands.
//
// Every backend difference lives in this file, and a plugin author writes a
// hook against one payload, not three.

// HooksEnv carries the turn's hooks to the dispatcher, as JSON. Computed by
// the gateway for the turn and inherited by the CLI and everything it runs,
// so the dispatcher acts on exactly the toolkit the turn started with.
const HooksEnv = "BOMCLAW_HOOKS"

// HookBinEnv is the dispatcher's path, for the opencode plugin to call.
const HookBinEnv = "BOMCLAW_HOOK_BIN"

// Hook events.
const (
	PreTool  = "pre_tool"
	PostTool = "post_tool"
)

// Hook is one hook as a turn runs it: its plugin's, expanded and resolved.
type Hook struct {
	Plugin     string `json:"plugin"`
	Event      string `json:"event"`
	Match      string `json:"match,omitempty"`
	Run        string `json:"run"`
	Dir        string `json:"dir"`
	Timeout    int    `json:"timeout"`
	FailClosed bool   `json:"fail_closed,omitempty"`
}

// hooksOf expands one plugin's declared hooks.
func hooksOf(p *Plugin, expand func(string) string) []Hook {
	var out []Hook
	for event, list := range map[string][]HookSpec{PreTool: p.Hooks.PreTool, PostTool: p.Hooks.PostTool} {
		for _, h := range list {
			t := h.Timeout
			if t == 0 {
				t = DefaultHookTimeout
			}
			out = append(out, Hook{Plugin: p.Name, Event: event, Match: h.Match,
				Run: expand(h.Run), Dir: p.Dir, Timeout: t, FailClosed: h.FailClosed})
		}
	}
	return out
}

// hookBin is the dispatcher the CLIs are told to run: this binary, resolved
// through symlinks so a CLI given it runs the same build the gateway is —
// unless HookBinEnv names another, which is how a test (whose own binary is
// a test runner) points the CLIs at a real bomclaw.
var hookBin = func() string {
	if b := os.Getenv(HookBinEnv); b != "" {
		return b
	}
	exe, err := os.Executable()
	if err != nil {
		return "bomclaw"
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		return real
	}
	return exe
}

func (k *Kit) eventTimeout(event string) int {
	total := 0
	for _, h := range k.Hooks {
		if h.Event == event {
			total += h.Timeout
		}
	}
	return total + 5
}

func (k *Kit) has(event string) bool {
	for _, h := range k.Hooks {
		if h.Event == event {
			return true
		}
	}
	return false
}

func dispatcher(event, backend string) string {
	return shellQuote(hookBin()) + " hook " + event + " --backend " + backend
}

// ClaudeSettings is the --settings value registering the dispatcher, or ""
// when no plugin has hooks — then claude runs exactly as it did before.
func (k *Kit) ClaudeSettings() string {
	if k == nil || len(k.Hooks) == 0 {
		return ""
	}
	hooks := map[string]any{}
	for event, name := range map[string]string{PreTool: "PreToolUse", PostTool: "PostToolUse"} {
		if !k.has(event) {
			continue
		}
		hooks[name] = []any{map[string]any{
			"matcher": "*",
			"hooks": []any{map[string]any{
				"type": "command", "command": dispatcher(event, "claude"), "timeout": k.eventTimeout(event),
			}},
		}}
	}
	b, _ := json.Marshal(map[string]any{"hooks": hooks})
	return string(b)
}

// CodexHookArgs turns codex's hooks on for this invocation and registers the
// dispatcher. The trust bypass is deliberate (decided 2026-11-04): codex
// otherwise wants each hook trusted interactively, which a headless agent
// cannot do — and the hooks here already passed bomclaw's own trust gate,
// which is the review that flag stands in for.
func (k *Kit) CodexHookArgs() []string {
	if k == nil || len(k.Hooks) == 0 {
		return nil
	}
	args := []string{"--dangerously-bypass-hook-trust", "-c", "features.hooks=true"}
	for event, name := range map[string]string{PreTool: "PreToolUse", PostTool: "PostToolUse"} {
		if !k.has(event) {
			continue
		}
		args = append(args, "-c", fmt.Sprintf(`hooks.%s=[{matcher="*", hooks=[{type="command", command=%s}]}]`,
			name, tomlString(dispatcher(event, "codex"))))
	}
	return args
}

// opencodePlugin is the bridge opencode loads: its tool hooks call the
// dispatcher, and a refusal becomes the thrown error opencode reports to the
// model. Static — the hooks themselves travel in HooksEnv.
const opencodePlugin = `// Written by bomclaw. Bridges opencode's tool hooks to bomclaw plugin hooks.
import { spawnSync } from "node:child_process";

function dispatch(event, payload) {
  const bin = process.env.BOMCLAW_HOOK_BIN;
  if (!bin) return null;
  const r = spawnSync(bin, ["hook", event, "--backend", "opencode"], {
    input: JSON.stringify(payload), encoding: "utf8", timeout: 600000,
  });
  if (r.error) return null;
  try { return JSON.parse(r.stdout || "{}"); } catch { return null; }
}

export const BomclawHooks = async () => ({
  "tool.execute.before": async (input, output) => {
    const d = dispatch("pre_tool", {
      tool_name: input.tool, tool_input: output && output.args, session_id: input.sessionID,
    });
    if (d && d.decision === "deny") throw new Error(d.reason || "blocked by a bomclaw plugin hook");
  },
  "tool.execute.after": async (input, output) => {
    dispatch("post_tool", {
      tool_name: input.tool, tool_input: input.args, tool_response: output && output.output,
      session_id: input.sessionID,
    });
  },
});
`

// opencodePluginPath writes the bridge into the agent's workspace (only when
// it changed) and returns its file:// URL, or "" if it could not.
func (k *Kit) opencodePluginPath() string {
	if k.AgentWorkspace == "" {
		return ""
	}
	path := filepath.Join(k.AgentWorkspace, ".bomclaw", "opencode-hooks.mjs")
	if cur, err := os.ReadFile(path); err != nil || string(cur) != opencodePlugin {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return ""
		}
		if err := os.WriteFile(path, []byte(opencodePlugin), 0o644); err != nil {
			return ""
		}
	}
	return "file://" + filepath.ToSlash(path)
}

// hookEnv is what the dispatcher needs in the CLI's environment.
func (k *Kit) hookEnv() []string {
	if len(k.Hooks) == 0 {
		return nil
	}
	b, _ := json.Marshal(k.Hooks)
	return []string{HooksEnv + "=" + string(b), HookBinEnv + "=" + hookBin()}
}

// --- the dispatcher ----------------------------------------------------------

// HookPayload is what a plugin's hook receives on stdin, the same whichever
// backend made the call.
type HookPayload struct {
	Event   string         `json:"event"`
	Tool    string         `json:"tool"`     // normalised: bash, edit, write, read, …
	RawTool string         `json:"raw_tool"` // the backend's own name
	Input   map[string]any `json:"input"`
	Output  any            `json:"output,omitempty"` // post_tool only
	Cwd     string         `json:"cwd"`
	Backend string         `json:"backend"`
	Session string         `json:"session,omitempty"`
	Plugin  string         `json:"plugin"`
}

// HookDecision is what a pre_tool hook may print on stdout (exit 0). Exiting
// 2 with a reason on stderr is the same as {"decision":"deny"}.
type HookDecision struct {
	Decision string `json:"decision"` // "allow" (default) or "deny"
	Reason   string `json:"reason,omitempty"`
}

// toolAliases maps backend tool names onto one vocabulary, so `match: bash`
// means the shell on every backend.
var toolAliases = map[string]string{
	"shell": "bash", "exec_command": "bash", "local_shell": "bash", "container.exec": "bash",
	"multiedit": "edit", "apply_patch": "edit", "str_replace_editor": "edit",
}

// NormalizeTool is the vocabulary hooks match against.
func NormalizeTool(raw string) string {
	t := strings.ToLower(strings.TrimSpace(raw))
	if a, ok := toolAliases[t]; ok {
		return a
	}
	return t
}

// RunHooks is `bomclaw hook <event> --backend <name>`. It returns the exit
// code for the backend. It never fails the backend for its own reasons: an
// unreadable payload or a malformed hook list lets the call through, logged.
func RunHooks(event, backend string, stdin io.Reader, stdout, stderr io.Writer) int {
	var raw map[string]any
	data, _ := io.ReadAll(io.LimitReader(stdin, 8<<20))
	if err := json.Unmarshal(data, &raw); err != nil {
		logHook(event, backend, "", "", "error", "unreadable payload: "+err.Error())
		return 0
	}
	var hooks []Hook
	if err := json.Unmarshal([]byte(os.Getenv(HooksEnv)), &hooks); err != nil && os.Getenv(HooksEnv) != "" {
		logHook(event, backend, "", "", "error", "unreadable "+HooksEnv+": "+err.Error())
		return 0
	}

	p := HookPayload{Event: event, Backend: backend, RawTool: str(raw["tool_name"]),
		Output: raw["tool_response"], Cwd: str(raw["cwd"]), Session: str(raw["session_id"])}
	p.Tool = NormalizeTool(p.RawTool)
	p.Input, _ = raw["tool_input"].(map[string]any)
	if p.Input == nil {
		p.Input = map[string]any{}
	}
	// A shell command as an argv list (codex has sent one) reads as a line.
	if argv, ok := p.Input["command"].([]any); ok {
		parts := make([]string, len(argv))
		for i, a := range argv {
			parts[i] = fmt.Sprint(a)
		}
		p.Input["command"] = strings.Join(parts, " ")
	}
	if p.Cwd == "" {
		p.Cwd, _ = os.Getwd()
	}

	for _, h := range hooks {
		if h.Event != event || !matches(h.Match, p.Tool, p.RawTool) {
			continue
		}
		p.Plugin = h.Plugin
		d, err := runOne(h, p)
		switch {
		case err != nil && h.FailClosed && event == PreTool:
			d = HookDecision{Decision: "deny", Reason: fmt.Sprintf("%s's hook failed and is fail-closed: %v", h.Plugin, err)}
		case err != nil:
			logHook(event, backend, p.Tool, h.Plugin, "error", err.Error()+" (let through)")
			continue
		}
		if event == PreTool && d.Decision == "deny" {
			reason := d.Reason
			if reason == "" {
				reason = "refused by the " + h.Plugin + " plugin"
			} else {
				reason = h.Plugin + ": " + reason
			}
			logHook(event, backend, p.Tool, h.Plugin, "deny", reason)
			return refuse(backend, reason, stdout, stderr)
		}
		logHook(event, backend, p.Tool, h.Plugin, "allow", "")
	}
	return 0
}

// refuse answers a denial in the backend's own terms. claude and codex both
// treat exit 2 as "blocked" and hand stderr to the model as the reason;
// opencode's bridge reads a decision from stdout and throws it.
func refuse(backend, reason string, stdout, stderr io.Writer) int {
	if backend == "opencode" {
		json.NewEncoder(stdout).Encode(HookDecision{Decision: "deny", Reason: reason})
		return 0
	}
	fmt.Fprintln(stderr, reason)
	return 2
}

func matches(pattern, tool, raw string) bool {
	if pattern == "" {
		return true
	}
	re, err := regexp.Compile("^(?:" + pattern + ")$")
	if err != nil {
		return false
	}
	return re.MatchString(tool) || re.MatchString(raw)
}

// runOne runs one hook command with the payload on stdin.
func runOne(h Hook, p HookPayload) (HookDecision, error) {
	var d HookDecision
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(h.Timeout)*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", h.Run)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", h.Run)
	}
	cmd.Dir = h.Dir
	body, _ := json.Marshal(p)
	cmd.Stdin = bytes.NewReader(body)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return d, fmt.Errorf("timed out after %ds", h.Timeout)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 2 {
		return HookDecision{Decision: "deny", Reason: strings.TrimSpace(errb.String())}, nil
	}
	if err != nil {
		return d, fmt.Errorf("%v: %s", err, cut(strings.TrimSpace(errb.String()), 300))
	}
	if s := bytes.TrimSpace(out.Bytes()); len(s) > 0 {
		if err := json.Unmarshal(s, &d); err != nil {
			return d, fmt.Errorf("printed something that is not a decision: %s", cut(string(s), 200))
		}
	}
	return d, nil
}

// logHook appends one line to the agent's hook log — the audit trail of what
// was refused and why, and the only place a fail-open hook's error shows up.
func logHook(event, backend, tool, plugin, outcome, detail string) {
	ws := os.Getenv(WorkspaceEnv)
	if ws == "" {
		return
	}
	path := filepath.Join(ws, ".bomclaw", "hooks.log")
	if info, err := os.Stat(path); err == nil && info.Size() > 5<<20 {
		_ = os.Rename(path, path+".1")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s %s tool=%s plugin=%s %s %s\n",
		time.Now().Format(time.RFC3339), backend, event, tool, plugin, outcome, strings.ReplaceAll(detail, "\n", " "))
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func shellQuote(s string) string {
	if runtime.GOOS == "windows" {
		return `"` + s + `"`
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
