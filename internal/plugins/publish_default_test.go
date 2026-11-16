package plugins

import (
	"strings"
	"testing"

	"github.com/ngocp/goterm-control/internal/skills"
)

// The bundled publish plugin puts bomclaw's own MCP server in every agent's
// CLI: publishing is a tool the model sees, for each backend.
func TestTheBundledPublishPluginStartsTheBomclawMCPServer(t *testing.T) {
	ws := t.TempDir()
	if _, err := skills.EnsureDefaults(ws); err != nil {
		t.Fatal(err)
	}
	k := Load(ws, "")
	p := k.Find("publish")
	if p == nil {
		t.Fatal("publish plugin not loaded")
	}
	if len(p.Reasons) > 0 {
		t.Fatalf("publish plugin is not active: %v", p.Reasons)
	}
	if cfg := k.ClaudeMCPConfig(); !strings.Contains(cfg, `"bomclaw":{"command":"bomclaw","args":["mcp"]}`) {
		t.Errorf("claude --mcp-config = %s", cfg)
	}
	if args := strings.Join(k.CodexArgs(), " "); !strings.Contains(args, "mcp_servers.bomclaw") {
		t.Errorf("codex args = %s", args)
	}
	if !strings.Contains(k.OpencodeConfig(), "bomclaw") {
		t.Errorf("opencode config = %s", k.OpencodeConfig())
	}
}
