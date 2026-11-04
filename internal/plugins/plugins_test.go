package plugins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// cmdFile is how a test names a command in bin/: as is on Unix, where the
// executable bit decides; with an extension in PATHEXT on Windows.
func cmdFile(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".cmd"
	}
	return name
}

func writeFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

// plugin writes <root>/<name>/plugin.yaml.
func plugin(t *testing.T, root, name, manifest string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	writeFile(t, filepath.Join(dir, File), manifest, 0o644)
	return dir
}

func skill(t *testing.T, dir, name, desc string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, name, "SKILL.md"),
		"---\nname: "+name+"\ndescription: "+desc+"\n---\n\nbody\n", 0o644)
}

func mustFind(t *testing.T, k *Kit, name string) *Plugin {
	t.Helper()
	p := k.Find(name)
	if p == nil {
		t.Fatalf("no plugin %q in %+v", name, k.Plugins)
	}
	return p
}

// A bare skill folder is a plugin too: one list, one switch.
func TestBareSkillIsAPlugin(t *testing.T) {
	ws := t.TempDir()
	skill(t, filepath.Join(ws, "skills"), "deploy", "Use when shipping.")

	k := Load(ws, "")
	p := mustFind(t, k, "deploy")
	if p.Kind != KindSkill || p.Status != Active {
		t.Fatalf("got %+v", p)
	}
	if !strings.Contains(k.Prompt(), "**deploy** — Use when shipping.") {
		t.Fatalf("skill missing from prompt:\n%s", k.Prompt())
	}
	if strings.Contains(k.Prompt(), "Your plugins") {
		t.Fatalf("a skill-only toolkit grew a plugins section:\n%s", k.Prompt())
	}
}

func TestPluginContributesEverything(t *testing.T) {
	ws := t.TempDir()
	dir := plugin(t, filepath.Join(ws, Dir), "forge", `
description: Work with the code forge. Use for PRs and issues.
provides: [vcs]
config:
  org: {default: acme}
  token: {secret: true, required: true}
prompt: Default org is ${config.org}; token ${config.token}.
env:
  FORGE_HOME: ${plugin_dir}/data
mcp:
  forge:
    command: ${plugin_dir}/bin/forge-mcp
    args: [--org, "${config.org}"]
`)
	writeFile(t, filepath.Join(dir, "bin", cmdFile("forge-pr")), "#!/bin/sh\n", 0o755)
	writeFile(t, filepath.Join(dir, "bin", "notes.txt"), "not a command", 0o644)
	skill(t, filepath.Join(dir, "skills"), "review-pr", "Use when reviewing a PR.")

	st := State{}
	st.SetConfig("forge", "token", "s3cret")
	if err := st.Save(ws); err != nil {
		t.Fatal(err)
	}

	k := Load(ws, "")
	p := mustFind(t, k, "forge")
	if p.Status != Active {
		t.Fatalf("status %s: %v", p.Status, p.Reasons)
	}
	prompt := k.Prompt()
	for _, want := range []string{"review-pr", "**forge**", "commands: " + cmdFile("forge-pr"), "MCP servers: forge", "Default org is acme"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lacks %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "s3cret") {
		t.Fatalf("a secret reached the prompt:\n%s", prompt)
	}
	if strings.Contains(prompt, "notes.txt") {
		t.Errorf("a non-executable file was offered as a command")
	}

	env := strings.Join(k.ApplyEnv([]string{"PATH=/usr/bin"}), "\n")
	for _, want := range []string{
		"FORGE_HOME=" + dir + "/data",
		"BOMCLAW_FORGE_TOKEN=s3cret",
		"PATH=/usr/bin" + string(filepath.ListSeparator) + filepath.Join(dir, "bin"),
	} {
		if !strings.Contains(env, want) {
			t.Errorf("env lacks %q:\n%s", want, env)
		}
	}

	var cfg struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(k.ClaudeMCPConfig()), &cfg); err != nil {
		t.Fatal(err)
	}
	if s := cfg.MCPServers["forge"]; s.Command != dir+"/bin/forge-mcp" || strings.Join(s.Args, " ") != "--org acme" {
		t.Fatalf("claude mcp = %+v", cfg)
	}
	codex := strings.Join(k.CodexArgs(), " ")
	if !strings.Contains(codex, `-c mcp_servers.forge.command=`+tomlString(dir+"/bin/forge-mcp")) ||
		!strings.Contains(codex, `-c mcp_servers.forge.args=["--org", "acme"]`) {
		t.Fatalf("codex args = %s", codex)
	}
	if oc := k.OpencodeConfig(); !strings.Contains(oc, `"type":"local"`) || !strings.Contains(oc, `"forge-mcp"`) &&
		!strings.Contains(oc, "forge-mcp") {
		t.Fatalf("opencode config = %s", oc)
	}
}

// With no plugin servers, claude still gets a complete, empty config: with
// --strict-mcp-config that is what keeps the operator's own servers out.
func TestEmptyKitStillIsolatesClaude(t *testing.T) {
	var nilKit *Kit
	for _, k := range []*Kit{nilKit, Load(t.TempDir(), "")} {
		if got := k.ClaudeMCPConfig(); got != `{"mcpServers":{}}` {
			t.Fatalf("got %s", got)
		}
		if k.CodexArgs() != nil || k.OpencodeConfig() != "" {
			t.Fatalf("an empty kit produced backend config")
		}
	}
}

func TestPendingSaysWhatIsMissing(t *testing.T) {
	ws := t.TempDir()
	root := filepath.Join(ws, Dir)
	plugin(t, root, "needs-bin", "requires: {bins: [no-such-command-xyz]}\nprompt: hi")
	plugin(t, root, "needs-config", "config: {token: {required: true}}\nprompt: hi")
	plugin(t, root, "needs-dep", "inject: [needs-config]\nprompt: hi")
	plugin(t, root, "bad-enum", "config: {mode: {enum: [a, b], default: a}}\nprompt: hi")
	st := State{}
	st.SetConfig("bad-enum", "mode", "c")
	if err := st.Save(ws); err != nil {
		t.Fatal(err)
	}

	k := Load(ws, "")
	for name, want := range map[string]string{
		"needs-bin":    "needs `no-such-command-xyz` on PATH",
		"needs-config": "needs config token (bomclaw plugins config needs-config token=…)",
		"needs-dep":    "waits for needs-config (needs-config is pending)",
		"bad-enum":     `config mode="c" is not one of [a b]`,
	} {
		p := mustFind(t, k, name)
		if p.Status != Pending || !strings.Contains(strings.Join(p.Reasons, "; "), want) {
			t.Errorf("%s: %s %v, want reason %q", name, p.Status, p.Reasons, want)
		}
	}
	if !strings.Contains(k.Prompt(), "Not loaded") || !strings.Contains(k.Prompt(), "needs-dep") {
		t.Fatalf("pending plugins not reported:\n%s", k.Prompt())
	}

	// Configure the root of the chain: both wake on the next load.
	st.SetConfig("needs-config", "token", "x")
	if err := st.Save(ws); err != nil {
		t.Fatal(err)
	}
	k = Load(ws, "")
	for _, name := range []string{"needs-config", "needs-dep"} {
		if p := mustFind(t, k, name); p.Status != Active {
			t.Errorf("%s still %s: %v", name, p.Status, p.Reasons)
		}
	}
}

func TestInjectBySomeoneElsesCapability(t *testing.T) {
	ws := t.TempDir()
	root := filepath.Join(ws, Dir)
	plugin(t, root, "gitea", "provides: [forge]")
	plugin(t, root, "release", "inject: {required: [forge], optional: [chat]}")
	if p := mustFind(t, Load(ws, ""), "release"); p.Status != Active {
		t.Fatalf("release: %s %v", p.Status, p.Reasons)
	}

	st := State{}
	st.SetEnabled("gitea", false)
	if err := st.Save(ws); err != nil {
		t.Fatal(err)
	}
	k := Load(ws, "")
	if p := mustFind(t, k, "gitea"); p.Status != Disabled {
		t.Fatalf("gitea: %s", p.Status)
	}
	if p := mustFind(t, k, "release"); p.Status != Pending {
		t.Fatalf("release did not wait for its provider: %s", p.Status)
	}
}

// Disabling a bare skill takes it out of the index.
func TestDisableASkill(t *testing.T) {
	ws := t.TempDir()
	skill(t, filepath.Join(ws, "skills"), "noisy", "Use always.")
	st := State{}
	st.SetEnabled("noisy", false)
	if err := st.Save(ws); err != nil {
		t.Fatal(err)
	}
	if got := Load(ws, "").Prompt(); strings.Contains(got, "noisy") {
		t.Fatalf("disabled skill still offered:\n%s", got)
	}
}

// A repo's plugins teach without asking but run code only once trusted.
func TestProjectPluginsNeedTrustToRunCode(t *testing.T) {
	agent, repo := t.TempDir(), t.TempDir()
	dir := plugin(t, filepath.Join(repo, ProjectDir), "repo-tools", `
description: This repo's tooling.
prompt: Run migrations with make migrate.
env: {EVIL: "1"}
mcp: {repo: {command: ./server}}
`)
	writeFile(t, filepath.Join(dir, "bin", cmdFile("git")), "#!/bin/sh\n", 0o755)
	skill(t, filepath.Join(dir, "skills"), "migrate", "Use before touching the schema.")

	k := Load(agent, repo)
	p := mustFind(t, k, "repo-tools")
	if p.Status != Active || !p.Untrusted {
		t.Fatalf("got %s untrusted=%v", p.Status, p.Untrusted)
	}
	if len(k.Path) != 0 || len(k.Env) != 0 || len(k.Servers) != 0 {
		t.Fatalf("untrusted project plugin contributed code: path=%v env=%v mcp=%v", k.Path, k.Env, k.Servers)
	}
	prompt := k.Prompt()
	if !strings.Contains(prompt, "migrate") || !strings.Contains(prompt, "make migrate") {
		t.Fatalf("untrusted plugin lost its skills or prompt:\n%s", prompt)
	}
	if strings.Contains(prompt, "commands: git") {
		t.Fatalf("untrusted commands offered:\n%s", prompt)
	}

	st := State{}
	st.SetTrusted("repo-tools", true)
	if err := st.Save(agent); err != nil {
		t.Fatal(err)
	}
	k = Load(agent, repo)
	if len(k.Path) != 1 || k.Env["EVIL"] != "1" || len(k.Servers) != 1 {
		t.Fatalf("trusted plugin still held back: path=%v env=%v mcp=%v", k.Path, k.Env, k.Servers)
	}
	// Even trusted, a bin folder is appended: its `git` does not shadow the real one.
	env := k.ApplyEnv([]string{"PATH=/usr/bin"})
	if last := env[len(env)-1]; !strings.HasPrefix(last, "PATH=/usr/bin"+string(filepath.ListSeparator)) {
		t.Fatalf("plugin bin was not appended: %s", last)
	}
}

func TestProjectWinsANameCollision(t *testing.T) {
	agent, repo := t.TempDir(), t.TempDir()
	plugin(t, filepath.Join(agent, Dir), "deploy", "description: general")
	plugin(t, filepath.Join(repo, ProjectDir), "deploy", "description: this repo's")
	if p := mustFind(t, Load(agent, repo), "deploy"); p.Description != "this repo's" {
		t.Fatalf("got %q", p.Description)
	}
}

func TestBrokenManifestIsListedNotFatal(t *testing.T) {
	ws := t.TempDir()
	root := filepath.Join(ws, Dir)
	plugin(t, root, "broken", "name: [not, a, string")
	plugin(t, root, "two-ways", "mcp: {x: {command: a, url: http://b}}")
	plugin(t, root, "escapes", "bin: ../../elsewhere")
	plugin(t, root, "fine", "prompt: ok")
	k := Load(ws, "")
	for _, name := range []string{"broken", "two-ways", "escapes"} {
		if p := mustFind(t, k, name); p.Status != Broken {
			t.Errorf("%s: %s", name, p.Status)
		}
	}
	if p := mustFind(t, k, "fine"); p.Status != Active {
		t.Fatalf("one broken plugin took a healthy one down: %s", p.Status)
	}
}

func TestRemoteServerPerBackend(t *testing.T) {
	ws := t.TempDir()
	plugin(t, filepath.Join(ws, Dir), "search", `
config: {key: {env: SEARCH_TEST_KEY, secret: true}}
mcp:
  search:
    url: https://mcp.example.com/
    headers: {Authorization: "Bearer ${config.key}"}
`)
	t.Setenv("SEARCH_TEST_KEY", "k1")
	k := Load(ws, "")
	if !strings.Contains(k.ClaudeMCPConfig(), `"type":"http"`) ||
		!strings.Contains(k.ClaudeMCPConfig(), "Bearer k1") {
		t.Fatalf("claude: %s", k.ClaudeMCPConfig())
	}
	codex := strings.Join(k.CodexArgs(), " ")
	if !strings.Contains(codex, `mcp_servers.search.url="https://mcp.example.com/"`) ||
		!strings.Contains(codex, `mcp_servers.search.http_headers={"Authorization" = "Bearer k1"}`) {
		t.Fatalf("codex: %s", codex)
	}
	if oc := k.OpencodeConfig(); !strings.Contains(oc, `"type":"remote"`) {
		t.Fatalf("opencode: %s", oc)
	}
}

func TestStateRoundTrip(t *testing.T) {
	ws := t.TempDir()
	st := State{}
	st.SetEnabled("a", false)
	st.SetTrusted("b", true)
	st.SetConfig("c", "k", "v")
	if err := st.Save(ws); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(ws, StateFile))
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no Unix permission bits to check.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("state file mode: %v", info.Mode())
	}
	got, err := LoadState(ws)
	if err != nil {
		t.Fatal(err)
	}
	got.SetEnabled("a", true)
	got.SetConfig("c", "k", "")
	if len(got.Disabled) != 0 || len(got.Config) != 0 || !got.trusted("b") {
		t.Fatalf("got %+v", got)
	}
}
