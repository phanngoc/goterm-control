package plugins

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ngocp/goterm-control/internal/skills"
)

// MaxSectionRunes bounds the plugins section of the prompt as a whole.
const MaxSectionRunes = 6000

// Kit is everything the active plugins contribute to one turn, rendered for
// whichever backend runs it.
type Kit struct {
	Plugins []*Plugin `json:"plugins"`
	Errors  []string  `json:"errors,omitempty"`

	Skills  []skills.Skill       `json:"skills"`
	Env     map[string]string    `json:"-"`
	Path    []string             `json:"path,omitempty"`
	Servers map[string]MCPServer `json:"-"`
	// Owner says which plugin each MCP server came from, for listings and
	// for the collision warning.
	Owner map[string]string `json:"mcp_owner,omitempty"`
	// AgentWorkspace is exported to every turn as WorkspaceEnv, so the agent's
	// own `bomclaw plugins` finds its toolkit from whatever folder it is in.
	AgentWorkspace string `json:"agent_workspace,omitempty"`
	// Hooks run around this turn's tool calls; see hooks.go.
	Hooks []Hook `json:"hooks,omitempty"`
}

// WorkspaceEnv names the agent's own workspace inside a turn.
const WorkspaceEnv = "BOMCLAW_AGENT_WORKSPACE"

// build collects the active plugins' contributions. Plugins are already
// sorted by name, so a collision between two of them resolves the same way
// every turn.
func (k *Kit) build() {
	k.Env, k.Servers, k.Owner = map[string]string{}, map[string]MCPServer{}, map[string]string{}
	for _, p := range k.Plugins {
		if p.Status != Active {
			continue
		}
		k.Skills = skills.Merge(k.Skills, p.SkillList)
		if p.Untrusted {
			continue
		}
		if p.BinDir != "" {
			k.Path = append(k.Path, p.BinDir)
		}
		for key, v := range p.EnvVars {
			k.Env[key] = v
		}
		for key, v := range p.Values {
			k.Env[ConfigEnvName(p.Name, key)] = v
		}
		k.Hooks = append(k.Hooks, p.HookList...)
		for name, s := range p.Servers {
			if prev, taken := k.Owner[name]; taken {
				k.Errors = append(k.Errors, fmt.Sprintf(
					"mcp server %q is defined by both %s and %s; %s wins", name, prev, p.Name, p.Name))
			}
			k.Servers[name] = s
			k.Owner[name] = p.Name
		}
	}
	for _, e := range k.Errors {
		log.Printf("plugins: %s", e)
	}
}

// ConfigEnvName is the variable a plugin's setting reaches its commands and
// servers as: BOMCLAW_<PLUGIN>_<KEY>, uppercased, dashes as underscores.
func ConfigEnvName(plugin, key string) string {
	up := func(s string) string { return strings.ToUpper(strings.ReplaceAll(s, "-", "_")) }
	return "BOMCLAW_" + up(plugin) + "_" + up(key)
}

// Find returns the plugin of that name, if the kit holds one.
func (k *Kit) Find(name string) *Plugin {
	for _, p := range k.Plugins {
		if p.Name == name {
			return p
		}
	}
	return nil
}

// Prompt is the block appended to the system prompt: the skill index (bodies
// read on demand), then what each code-carrying plugin put in front of the
// agent, then one line per plugin that is not running and why. The last part
// is the point of pending: an agent asked to use something that is waiting on
// a token says so, instead of acting as if the plugin did not exist.
func (k *Kit) Prompt() string {
	var b strings.Builder
	b.WriteString(skills.Index(k.Skills))

	var active, waiting []string
	for _, p := range k.Plugins {
		if p.Kind != KindPlugin {
			continue
		}
		switch p.Status {
		case Active:
			if s := p.section(); s != "" {
				active = append(active, s)
			}
		case Pending, Broken:
			waiting = append(waiting, fmt.Sprintf("- **%s** (%s): %s", p.Name, p.Status, strings.Join(p.Reasons, "; ")))
		}
	}
	if len(active) == 0 && len(waiting) == 0 {
		return b.String()
	}

	var s strings.Builder
	s.WriteString("\n## Your plugins\n\n")
	if len(active) > 0 {
		s.WriteString("Commands listed below are on your PATH; tools from a plugin's MCP servers appear among your own tools.\n\n")
		for _, a := range active {
			s.WriteString(a)
		}
	}
	if len(waiting) > 0 {
		s.WriteString("\nNot loaded — tell the person what is missing rather than working around it:\n")
		for _, w := range waiting {
			s.WriteString(w)
			s.WriteString("\n")
		}
	}
	out := s.String()
	if r := []rune(out); len(r) > MaxSectionRunes {
		out = string(r[:MaxSectionRunes]) + "\n\n[plugin list truncated — `bomclaw plugins list` shows all of them]\n"
	}
	b.WriteString(out)
	return b.String()
}

// section is one active plugin's lines, or "" for a plugin that contributes
// only skills — those are already in the index above, and naming the plugin
// again would be paying twice for the same information.
func (p *Plugin) section() string {
	if p.PromptText == "" && (p.Untrusted || !p.runsCode()) {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "- **%s** — %s\n", p.Name, orNone(p.Description))
	if !p.Untrusted {
		if len(p.Commands) > 0 {
			fmt.Fprintf(&b, "  commands: %s\n", strings.Join(p.Commands, ", "))
		}
		if n := len(p.HookList); n > 0 {
			fmt.Fprintf(&b, "  hooks: %d around your tool calls — a refusal comes back as the tool's error\n", n)
		}
		if len(p.Servers) > 0 {
			names := make([]string, 0, len(p.Servers))
			for n := range p.Servers {
				names = append(names, n)
			}
			sort.Strings(names)
			fmt.Fprintf(&b, "  MCP servers: %s\n", strings.Join(names, ", "))
		}
	}
	if p.PromptText != "" {
		for _, line := range strings.Split(p.PromptText, "\n") {
			b.WriteString("  ")
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	return b.String()
}

func orNone(d string) string {
	if d == "" {
		return "(no description)"
	}
	return d
}

// ApplyEnv adds the kit's environment to a command's: plugin variables set,
// plugin bin folders APPENDED to PATH. Appended, never prepended: a plugin
// adds commands, it does not get to replace `git` with its own.
func (k *Kit) ApplyEnv(env []string) []string {
	if k == nil {
		return env
	}
	if k.AgentWorkspace != "" {
		env = append(env, WorkspaceEnv+"="+k.AgentWorkspace)
	}
	env = append(env, k.hookEnv()...)
	keys := make([]string, 0, len(k.Env))
	for key := range k.Env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if strings.EqualFold(key, "PATH") {
			continue // PATH is ours to manage, below
		}
		env = append(env, key+"="+k.Env[key])
	}
	if len(k.Path) == 0 {
		return env
	}
	path := os.Getenv("PATH")
	for _, kv := range env { // the command's own PATH, the last one wins in exec
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v
		}
	}
	parts := append([]string{path}, k.Path...)
	return append(env, "PATH="+strings.Join(parts, string(filepath.ListSeparator)))
}

// ClaudeMCPConfig is the --mcp-config value for the claude CLI. Always a
// complete config — `{"mcpServers":{}}` when no plugin brings a server — so
// that with --strict-mcp-config the operator's own servers never leak into
// the agent's turn.
func (k *Kit) ClaudeMCPConfig() string {
	type server struct {
		Type    string            `json:"type,omitempty"`
		Command string            `json:"command,omitempty"`
		Args    []string          `json:"args,omitempty"`
		Env     map[string]string `json:"env,omitempty"`
		URL     string            `json:"url,omitempty"`
		Headers map[string]string `json:"headers,omitempty"`
	}
	out := map[string]server{}
	if k != nil {
		for name, s := range k.Servers {
			v := server{Command: s.Command, Args: s.Args, Env: s.Env, URL: s.URL, Headers: s.Headers}
			if s.URL != "" {
				v.Type = "http"
			}
			out[name] = v
		}
	}
	b, _ := json.Marshal(map[string]any{"mcpServers": out})
	return string(b)
}

// CodexArgs are `-c` overrides adding the kit's servers to codex's own
// configuration for this invocation only.
func (k *Kit) CodexArgs() []string {
	if k == nil {
		return nil
	}
	var args []string
	for _, name := range sortedKeys(k.Servers) {
		s := k.Servers[name]
		set := func(field, toml string) {
			args = append(args, "-c", "mcp_servers."+name+"."+field+"="+toml)
		}
		if s.URL != "" {
			set("url", tomlString(s.URL))
			if len(s.Headers) > 0 {
				set("http_headers", tomlTable(s.Headers))
			}
			continue
		}
		set("command", tomlString(s.Command))
		if len(s.Args) > 0 {
			set("args", tomlArray(s.Args))
		}
		if len(s.Env) > 0 {
			set("env", tomlTable(s.Env))
		}
	}
	return args
}

// OpencodeConfig is an OPENCODE_CONFIG_CONTENT value carrying the kit's
// servers, or "" when there are none. opencode merges it over its own config
// files, so the agent's other opencode settings stay as they were.
func (k *Kit) OpencodeConfig() string {
	if k == nil || (len(k.Servers) == 0 && len(k.Hooks) == 0) {
		return ""
	}
	doc := map[string]any{}
	if len(k.Hooks) > 0 {
		if url := k.opencodePluginPath(); url != "" {
			doc["plugin"] = []string{url}
		}
	}
	mcp := map[string]map[string]any{}
	for name, s := range k.Servers {
		if s.URL != "" {
			v := map[string]any{"type": "remote", "url": s.URL, "enabled": true}
			if len(s.Headers) > 0 {
				v["headers"] = s.Headers
			}
			mcp[name] = v
			continue
		}
		v := map[string]any{"type": "local", "command": append([]string{s.Command}, s.Args...), "enabled": true}
		if len(s.Env) > 0 {
			v["environment"] = s.Env
		}
		mcp[name] = v
	}
	if len(mcp) > 0 {
		doc["mcp"] = mcp
	}
	if len(doc) == 0 {
		return ""
	}
	b, _ := json.Marshal(doc)
	return string(b)
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// tomlString is a TOML basic string. JSON's escaping is a subset TOML
// accepts, except that JSON may emit  -style escapes TOML also accepts —
// so json.Marshal of a string is a valid TOML basic string.
func tomlString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func tomlArray(list []string) string {
	parts := make([]string, len(list))
	for i, s := range list {
		parts[i] = tomlString(s)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func tomlTable(m map[string]string) string {
	parts := make([]string, 0, len(m))
	for _, k := range sortedKeys(m) {
		parts = append(parts, tomlString(k)+" = "+tomlString(m[k]))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}
