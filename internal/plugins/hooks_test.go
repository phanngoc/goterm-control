package plugins

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func needSh(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("hook fixtures are sh scripts")
	}
}

// script writes an executable sh script and returns its path.
func script(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	writeFile(t, path, "#!/bin/sh\n"+body+"\n", 0o755)
	return path
}

// dispatch runs RunHooks with the given hooks in the environment.
func dispatch(t *testing.T, hooks []Hook, event, backend string, payload map[string]any) (int, string, string) {
	t.Helper()
	b, _ := json.Marshal(hooks)
	t.Setenv(HooksEnv, string(b))
	in, _ := json.Marshal(payload)
	var out, errb bytes.Buffer
	code := RunHooks(event, backend, bytes.NewReader(in), &out, &errb)
	return code, out.String(), errb.String()
}

func TestHookManifestIsValidated(t *testing.T) {
	ws := t.TempDir()
	root := filepath.Join(ws, Dir)
	plugin(t, root, "no-run", "hooks: {pre_tool: [{match: bash}]}")
	plugin(t, root, "bad-re", "hooks: {pre_tool: [{match: '(', run: x}]}")
	plugin(t, root, "slow", "hooks: {post_tool: [{run: x, timeout: 999}]}")
	plugin(t, root, "ok", "hooks: {pre_tool: [{match: bash, run: ./guard}]}")
	k := Load(ws, "")
	for _, n := range []string{"no-run", "bad-re", "slow"} {
		if p := mustFind(t, k, n); p.Status != Broken {
			t.Errorf("%s: %s", n, p.Status)
		}
	}
	if len(k.Hooks) != 1 || k.Hooks[0].Timeout != DefaultHookTimeout || k.Hooks[0].Event != PreTool {
		t.Fatalf("hooks = %+v", k.Hooks)
	}
}

// Hooks are code that runs before every tool call: a repo's plugin does not
// get to install one until the owner trusts it.
func TestProjectHooksNeedTrust(t *testing.T) {
	agent, repo := t.TempDir(), t.TempDir()
	plugin(t, filepath.Join(repo, ProjectDir), "repo-guard", "hooks: {pre_tool: [{run: ./guard}]}")
	k := Load(agent, repo)
	if p := mustFind(t, k, "repo-guard"); !p.Untrusted || len(k.Hooks) != 0 {
		t.Fatalf("untrusted hooks registered: untrusted=%v hooks=%v", p.Untrusted, k.Hooks)
	}
	if k.ClaudeSettings() != "" || k.CodexHookArgs() != nil || k.OpencodeConfig() != "" {
		t.Fatal("an untrusted hook reached a backend")
	}
	st := State{}
	st.SetTrusted("repo-guard", true)
	_ = st.Save(agent)
	if k := Load(agent, repo); len(k.Hooks) != 1 {
		t.Fatalf("trusted hook missing: %v", k.Hooks)
	}
}

func TestHookRenderingPerBackend(t *testing.T) {
	ws := t.TempDir()
	plugin(t, filepath.Join(ws, Dir), "guard", `
hooks:
  pre_tool: [{match: bash, run: "${plugin_dir}/guard", timeout: 7}]
  post_tool: [{run: "${plugin_dir}/audit"}]
`)
	old := hookBin
	hookBin = func() string { return "/opt/bom claw/bomclaw" }
	defer func() { hookBin = old }()
	k := Load(ws, "")

	var settings struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type, Command string
				Timeout       int
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(k.ClaudeSettings()), &settings); err != nil {
		t.Fatal(err)
	}
	pre := settings.Hooks["PreToolUse"]
	if len(pre) != 1 || pre[0].Matcher != "*" ||
		pre[0].Hooks[0].Command != shellQuote("/opt/bom claw/bomclaw")+" hook pre_tool --backend claude" ||
		pre[0].Hooks[0].Timeout != 12 {
		t.Fatalf("claude PreToolUse = %+v", pre)
	}
	if len(settings.Hooks["PostToolUse"]) != 1 {
		t.Fatalf("claude PostToolUse missing: %+v", settings.Hooks)
	}

	codex := strings.Join(k.CodexHookArgs(), " ")
	for _, want := range []string{"--dangerously-bypass-hook-trust", "features.hooks=true",
		`hooks.PreToolUse=[{matcher="*", hooks=[{type="command", command=` +
			tomlString(shellQuote("/opt/bom claw/bomclaw")+" hook pre_tool --backend codex") + `}]}]`} {
		if !strings.Contains(codex, want) {
			t.Errorf("codex args lack %q:\n%s", want, codex)
		}
	}

	oc := k.OpencodeConfig()
	if !strings.Contains(oc, `"plugin":["file://`) {
		t.Fatalf("opencode config = %s", oc)
	}
	if b, err := os.ReadFile(filepath.Join(ws, ".bomclaw", "opencode-hooks.mjs")); err != nil || string(b) != opencodePlugin {
		t.Fatalf("bridge not written: %v", err)
	}

	env := strings.Join(k.ApplyEnv(nil), "\n")
	if !strings.Contains(env, HooksEnv+"=") || !strings.Contains(env, HookBinEnv+"=/opt/bom claw/bomclaw") {
		t.Fatalf("env lacks the dispatcher's inputs:\n%s", env)
	}
	if !strings.Contains(k.Prompt(), "hooks: 2 around your tool calls") {
		t.Errorf("prompt does not mention the hooks:\n%s", k.Prompt())
	}
}

func TestDispatcherRefusesAndAllows(t *testing.T) {
	needSh(t)
	dir := t.TempDir()
	t.Setenv(WorkspaceEnv, dir)
	seen := filepath.Join(dir, "seen.json")
	denyExit := script(t, dir, "deny-exit", `cat > `+seen+`; echo "no force pushes" >&2; exit 2`)
	denyJSON := script(t, dir, "deny-json", `echo '{"decision":"deny","reason":"read-only today"}'`)
	allow := script(t, dir, "allow", `exit 0`)

	bash := map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "git push --force"}, "cwd": dir}
	read := map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": "/x"}}

	// exit 2 → refused; claude and codex get exit 2 and the reason on stderr.
	code, _, stderr := dispatch(t, []Hook{{Plugin: "guard", Event: PreTool, Match: "bash", Run: denyExit, Dir: dir, Timeout: 5}}, PreTool, "claude", bash)
	if code != 2 || !strings.Contains(stderr, "guard: no force pushes") {
		t.Fatalf("exit-2 deny: code=%d stderr=%q", code, stderr)
	}
	var got HookPayload
	b, _ := os.ReadFile(seen)
	_ = json.Unmarshal(b, &got)
	if got.Tool != "bash" || got.RawTool != "Bash" || got.Input["command"] != "git push --force" ||
		got.Backend != "claude" || got.Plugin != "guard" || got.Event != PreTool {
		t.Fatalf("payload = %+v", got)
	}

	// The match is on the tool: a bash guard does not see Read.
	if code, _, _ := dispatch(t, []Hook{{Plugin: "guard", Event: PreTool, Match: "bash", Run: denyExit, Dir: dir, Timeout: 5}}, PreTool, "claude", read); code != 0 {
		t.Fatal("a bash hook refused a Read")
	}

	// A JSON decision works too; opencode gets it back as JSON.
	code, stdout, _ := dispatch(t, []Hook{{Plugin: "ro", Event: PreTool, Run: denyJSON, Dir: dir, Timeout: 5}}, PreTool, "opencode", read)
	var d HookDecision
	_ = json.Unmarshal([]byte(stdout), &d)
	if code != 0 || d.Decision != "deny" || d.Reason != "ro: read-only today" {
		t.Fatalf("opencode deny: code=%d stdout=%q", code, stdout)
	}

	// First refusal wins; an allowing hook before it does not stop the chain.
	code, _, _ = dispatch(t, []Hook{
		{Plugin: "a", Event: PreTool, Run: allow, Dir: dir, Timeout: 5},
		{Plugin: "b", Event: PreTool, Run: denyJSON, Dir: dir, Timeout: 5},
	}, PreTool, "codex", bash)
	if code != 2 {
		t.Fatalf("chain: code=%d", code)
	}

	// post_tool hooks observe; their "deny" changes nothing.
	if code, _, _ := dispatch(t, []Hook{{Plugin: "audit", Event: PostTool, Run: denyJSON, Dir: dir, Timeout: 5}}, PostTool, "claude", bash); code != 0 {
		t.Fatal("a post_tool hook refused a call that already ran")
	}

	log, _ := os.ReadFile(filepath.Join(dir, ".bomclaw", "hooks.log"))
	if !strings.Contains(string(log), "tool=bash plugin=guard deny guard: no force pushes") {
		t.Fatalf("hooks.log:\n%s", log)
	}
}

func TestDispatcherFailsOpenUnlessToldOtherwise(t *testing.T) {
	needSh(t)
	dir := t.TempDir()
	t.Setenv(WorkspaceEnv, dir)
	crash := script(t, dir, "crash", `echo boom >&2; exit 1`)
	hang := script(t, dir, "hang", `sleep 5`)
	garbage := script(t, dir, "garbage", `echo not json`)
	bash := map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}}

	for name, run := range map[string]string{"crash": crash, "hang": hang, "garbage": garbage} {
		if code, _, _ := dispatch(t, []Hook{{Plugin: name, Event: PreTool, Run: run, Dir: dir, Timeout: 1}}, PreTool, "claude", bash); code != 0 {
			t.Errorf("%s: a broken hook stopped the agent (code %d)", name, code)
		}
		code, _, stderr := dispatch(t, []Hook{{Plugin: name, Event: PreTool, Run: run, Dir: dir, Timeout: 1, FailClosed: true}}, PreTool, "claude", bash)
		if code != 2 || !strings.Contains(stderr, "fail-closed") {
			t.Errorf("%s fail_closed: code=%d stderr=%q", name, code, stderr)
		}
	}

	// The dispatcher's own trouble never blocks a call.
	var out, errb bytes.Buffer
	if code := RunHooks(PreTool, "claude", strings.NewReader("{not json"), &out, &errb); code != 0 {
		t.Fatal("an unreadable payload blocked the call")
	}
}

func TestCodexArgvCommandIsALine(t *testing.T) {
	needSh(t)
	dir := t.TempDir()
	seen := filepath.Join(dir, "seen.json")
	rec := script(t, dir, "rec", `cat > `+seen)
	dispatch(t, []Hook{{Plugin: "p", Event: PreTool, Run: rec, Dir: dir, Timeout: 5}}, PreTool, "codex",
		map[string]any{"tool_name": "shell", "tool_input": map[string]any{"command": []any{"bash", "-lc", "rm -rf /tmp/x"}}})
	var got HookPayload
	b, _ := os.ReadFile(seen)
	_ = json.Unmarshal(b, &got)
	if got.Tool != "bash" || got.Input["command"] != "bash -lc rm -rf /tmp/x" {
		t.Fatalf("payload = %+v", got)
	}
}

// The opencode bridge, run by node the way opencode's Bun runtime would load
// it: a deny from the dispatcher must become a thrown error.
func TestOpencodeBridgeThrowsOnDeny(t *testing.T) {
	needSh(t)
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	dir := t.TempDir()
	bridge := filepath.Join(dir, "bridge.mjs")
	writeFile(t, bridge, opencodePlugin, 0o644)
	args := filepath.Join(dir, "args")
	fake := script(t, dir, "fake-bomclaw", `echo "$@" > `+args+`; cat > /dev/null; echo '{"decision":"deny","reason":"guard: nope"}'`)
	probe := `
const m = await import(process.argv[1]);
const hooks = await m.BomclawHooks({});
try {
  await hooks["tool.execute.before"]({tool: "bash", sessionID: "s", callID: "c"}, {args: {command: "rm -rf /"}});
  console.log("ALLOWED");
} catch (e) { console.log("THREW:" + e.message); }
`
	cmd := exec.Command(node, "--input-type=module", "-e", probe, bridge)
	cmd.Env = append(os.Environ(), HookBinEnv+"="+fake)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "THREW:guard: nope") {
		t.Fatalf("bridge output: %s", out)
	}
	if b, _ := os.ReadFile(args); strings.TrimSpace(string(b)) != "hook pre_tool --backend opencode" {
		t.Fatalf("dispatcher called with %q", b)
	}
}
