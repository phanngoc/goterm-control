package plugins_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ngocp/goterm-control/internal/chat"
	_ "github.com/ngocp/goterm-control/internal/claude"
	_ "github.com/ngocp/goterm-control/internal/codex"
	"github.com/ngocp/goterm-control/internal/models"
	_ "github.com/ngocp/goterm-control/internal/opencode"
	"github.com/ngocp/goterm-control/internal/plugins"
	"github.com/ngocp/goterm-control/internal/session"
)

// TestLivePluginReachesEachBackend proves a plugin's MCP server and command
// reach the model through each real CLI — the claim the whole package rests
// on, and one no unit test can make. It spends quota, so it runs only with
// PLUGINS_LIVE set to a comma list of backends: claude,codex,opencode.
//
//	PLUGINS_LIVE=claude,codex go test ./internal/plugins -run Live -v
func TestLivePluginReachesEachBackend(t *testing.T) {
	which := os.Getenv("PLUGINS_LIVE")
	if which == "" {
		t.Skip("set PLUGINS_LIVE=claude,codex,opencode to run against the real CLIs")
	}
	apis := map[string]models.ModelAPI{
		"claude": models.APIClaudeCLI, "codex": models.APICodexCLI, "opencode": models.APIOpenCodeCLI,
	}
	for _, name := range strings.Split(which, ",") {
		api, ok := apis[strings.TrimSpace(name)]
		if !ok {
			t.Fatalf("unknown backend %q", name)
		}
		t.Run(name, func(t *testing.T) { livePlugin(t, api) })
	}
}

func livePlugin(t *testing.T, api models.ModelAPI) {
	ws := t.TempDir()
	dir := filepath.Join(ws, plugins.Dir, "oracle")
	mustWrite(t, filepath.Join(dir, plugins.File), `
description: Knows the magic word and the secret number.
config:
  word: {default: pineapple}
prompt: Ask the get_magic_word tool for the magic word; run the secret-number command for the secret number.
mcp:
  oracle:
    command: python3
    args: ["-I", "${plugin_dir}/server.py"]
    env: {WORD: "${config.word}"}
`, 0o644)
	mustWrite(t, filepath.Join(dir, "server.py"), mcpServer, 0o644)
	mustWrite(t, filepath.Join(dir, "bin", "secret-number"), "#!/bin/sh\necho 90417\n", 0o755)
	st := plugins.State{}
	st.SetConfig("oracle", "word", "mangosteen")
	if err := st.Save(ws); err != nil {
		t.Fatal(err)
	}

	c, err := chat.Resolve(api, chat.Deps{
		SystemPrompt: "You are a test fixture. Use your tools; answer in one line.",
		SystemExtra:  func(turn string) string { return plugins.Load(ws, turn).Prompt() },
		Plugins:      func(turn string) *plugins.Kit { return plugins.Load(ws, turn) },
		Workspace:    ws,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var out strings.Builder
	var tools []string
	// claude needs a model named; the others fall back to their CLI default.
	model := os.Getenv("PLUGINS_LIVE_MODEL")
	if model == "" && api == models.APIClaudeCLI {
		model = "haiku"
	}
	err = c.SendMessage(ctx, session.New(4242), model,
		"What is the magic word, and what is the secret number? Use your tools for both.", "",
		chat.StreamCallbacks{
			OnText:     func(s string) { out.WriteString(s) },
			OnToolCall: func(name, input string) { tools = append(tools, name) },
		})
	if err != nil {
		t.Fatalf("turn: %v", err)
	}
	t.Logf("tools: %v\nanswer: %s", tools, out.String())
	got := strings.ToLower(out.String())
	if !strings.Contains(got, "mangosteen") {
		t.Errorf("the MCP server's answer (with its configured value) did not come back")
	}
	if !strings.Contains(got, "90417") {
		t.Errorf("the plugin command on PATH did not run")
	}
}

func mustWrite(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

// mcpServer is the smallest stdio MCP server that lists and answers one tool.
const mcpServer = `import json, os, sys
for line in sys.stdin:
    msg = json.loads(line)
    mid, method = msg.get("id"), msg.get("method")
    if mid is None:
        continue
    if method == "initialize":
        res = {"protocolVersion": msg.get("params", {}).get("protocolVersion", "2025-06-18"),
               "capabilities": {"tools": {}}, "serverInfo": {"name": "oracle", "version": "0"}}
    elif method == "tools/list":
        res = {"tools": [{"name": "get_magic_word", "description": "Returns the magic word",
                          "inputSchema": {"type": "object", "properties": {}}}]}
    elif method == "tools/call":
        res = {"content": [{"type": "text", "text": "the magic word is " + os.environ.get("WORD", "?")}]}
    elif method == "ping":
        res = {}
    else:
        print(json.dumps({"jsonrpc": "2.0", "id": mid, "error": {"code": -32601, "message": "unknown"}}), flush=True)
        continue
    print(json.dumps({"jsonrpc": "2.0", "id": mid, "result": res}), flush=True)
`
