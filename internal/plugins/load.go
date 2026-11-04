package plugins

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/ngocp/goterm-control/internal/skills"
)

// Kind tells a real plugin from a bare skill folder wearing a plugin's shape.
type Kind string

const (
	KindPlugin Kind = "plugin"
	KindSkill  Kind = "skill"
)

// Status is where a plugin stands for this turn.
type Status string

const (
	Active   Status = "active"
	Pending  Status = "pending"  // something it needs is missing; see Reasons
	Disabled Status = "disabled" // turned off in the agent's state
	Broken   Status = "broken"   // its manifest could not be read
)

// Scope says whose a plugin is, which decides what it may contribute.
type Scope string

const (
	// ScopeAgent is the agent's own workspace: trusted with everything.
	ScopeAgent Scope = "agent"
	// ScopeProject is the folder a turn runs in, usually somebody's repo:
	// skills and prompt always, code only when the agent trusts it.
	ScopeProject Scope = "project"
	// ScopeInstalled is a plugin fetched from elsewhere into the agent's own
	// folder (it carries a SourceFile). Same rule as a project plugin: it is
	// someone else's code until the owner says otherwise.
	ScopeInstalled Scope = "installed"
)

// Plugin is one plugin as resolved for one turn.
type Plugin struct {
	Manifest
	Kind  Kind   `json:"kind"`
	Scope Scope  `json:"scope"`
	Dir   string `json:"dir"`

	Status  Status   `json:"status"`
	Reasons []string `json:"reasons,omitempty"`
	// Untrusted is a project plugin whose commands, env and MCP servers were
	// left out. It is still active: its skills and prompt apply.
	Untrusted bool `json:"untrusted,omitempty"`

	// What it contributes, resolved. Filled for every status so a listing
	// can say what a pending plugin would bring.
	SkillList  []skills.Skill       `json:"skill_list,omitempty"`
	Commands   []string             `json:"commands,omitempty"`
	BinDir     string               `json:"bin_dir,omitempty"`
	Values     map[string]string    `json:"-"` // resolved config, secrets included
	Servers    map[string]MCPServer `json:"-"` // expanded MCP servers
	EnvVars    map[string]string    `json:"-"` // expanded env
	HookList   []Hook               `json:"-"` // expanded hooks
	PromptText string               `json:"-"`
}

// Load resolves the toolkit for one turn: the agent's plugins, the turn
// directory's, and every bare skill in either, each checked against what it
// needs and against the agent's switches.
//
// Later roots win a name collision, as they do for skills: the project's way
// of doing a thing beats the general one.
//
// Never fails. A plugin that cannot be read is listed as broken; a root that
// cannot be read is reported in Kit.Errors. A broken toolkit must not take the
// agent's turn down with it.
func Load(agentWorkspace, turnWorkspace string) *Kit {
	// Absolute, because everything derived from these — ${plugin_dir}, skill
	// paths, PATH entries — is used from a turn running in another folder.
	agentWorkspace, turnWorkspace = absOrEmpty(agentWorkspace), absOrEmpty(turnWorkspace)
	k := &Kit{AgentWorkspace: agentWorkspace}
	st, err := LoadState(agentWorkspace)
	if err != nil {
		k.Errors = append(k.Errors, err.Error())
	}

	type root struct {
		dir   string
		scope Scope
		bare  bool // skills, not plugins
	}
	var roots []root
	seen := map[string]bool{}
	for _, ws := range []string{agentWorkspace, turnWorkspace} {
		if ws == "" || seen[ws] {
			continue
		}
		seen[ws] = true
		scope := ScopeProject
		if ws == agentWorkspace {
			scope = ScopeAgent
		}
		roots = append(roots,
			root{ws, scope, true}, // skills/ and .agents/skills, via skills.Load
			root{filepath.Join(ws, Dir), scope, false},
			// A repo's own plugins are project plugins even inside the agent's
			// workspace: they arrived with the code, not from the owner.
			root{filepath.Join(ws, ProjectDir), ScopeProject, false},
		)
	}

	byName := map[string]*Plugin{}
	var order []string
	add := func(p *Plugin) {
		if _, ok := byName[p.Name]; !ok {
			order = append(order, p.Name)
		}
		byName[p.Name] = p
	}
	for _, r := range roots {
		if r.bare {
			list, err := skills.Load(r.dir)
			if err != nil {
				k.Errors = append(k.Errors, err.Error())
			}
			for _, s := range list {
				add(&Plugin{
					Manifest:  Manifest{Name: s.Name, Description: s.Description},
					Kind:      KindSkill,
					Scope:     r.scope,
					Dir:       s.Dir,
					SkillList: []skills.Skill{s},
				})
			}
			continue
		}
		entries, err := os.ReadDir(r.dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			k.Errors = append(k.Errors, fmt.Sprintf("plugins: read %s: %v", r.dir, err))
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			dir := filepath.Join(r.dir, e.Name())
			if _, err := os.Stat(filepath.Join(dir, File)); err != nil {
				continue // a folder with no manifest is just a folder
			}
			scope := r.scope
			if _, err := os.Stat(filepath.Join(dir, SourceFile)); err == nil && scope == ScopeAgent {
				scope = ScopeInstalled
			}
			add(open(dir, scope, st))
		}
	}

	for _, name := range order {
		k.Plugins = append(k.Plugins, byName[name])
	}
	sort.Slice(k.Plugins, func(i, j int) bool { return k.Plugins[i].Name < k.Plugins[j].Name })
	resolve(k.Plugins, st)
	k.build()
	return k
}

// open reads one plugin folder and everything it would contribute.
func open(dir string, scope Scope, st State) *Plugin {
	m, err := ParseManifest(dir)
	p := &Plugin{Manifest: m, Kind: KindPlugin, Scope: scope, Dir: dir}
	if err != nil {
		if p.Name == "" {
			p.Name = filepath.Base(dir)
		}
		p.Status, p.Reasons = Broken, []string{err.Error()}
		return p
	}

	p.Values = configValues(m, st.Config[m.Name])
	expand := expander(dir, m.Name, p.Values, nil)

	if list, err := skills.LoadDir(filepath.Join(dir, m.Skills)); err == nil {
		p.SkillList = list
	}
	// A SKILL.md at the plugin's root is the plugin's own skill.
	if list, err := skills.LoadDir(dir); err == nil {
		for _, s := range list {
			if s.Dir == dir {
				p.SkillList = skills.Merge(p.SkillList, []skills.Skill{s})
			}
		}
	}

	if bin := filepath.Join(dir, m.Bin); isDir(bin) {
		p.BinDir = bin
		p.Commands = executables(bin)
	}
	p.EnvVars = map[string]string{}
	for k, v := range m.Env {
		p.EnvVars[k] = expand(v)
	}
	p.Servers = map[string]MCPServer{}
	for name, s := range m.MCP {
		p.Servers[name] = expandServer(s, expand)
	}
	p.HookList = hooksOf(p, expand)

	text := m.Prompt
	if m.PromptFile != "" {
		if b, err := os.ReadFile(filepath.Join(dir, m.PromptFile)); err == nil {
			text = string(b)
		} else {
			p.Reasons = append(p.Reasons, "prompt_file: "+err.Error())
		}
	}
	// Prompts are expanded WITHOUT secrets: a prompt is shown to the model
	// and recorded in transcripts.
	p.PromptText = cut(strings.TrimSpace(expander(dir, m.Name, p.Values, m.Config)(text)), MaxPromptRunes)
	return p
}

// configValues applies the schema: the agent's setting, else the gateway
// environment variable the field names, else the default.
func configValues(m Manifest, set map[string]string) map[string]string {
	out := map[string]string{}
	for key, f := range m.Config {
		v := set[key]
		if v == "" && f.Env != "" {
			v = os.Getenv(f.Env)
		}
		if v == "" {
			v = f.Default
		}
		if v != "" {
			out[key] = v
		}
	}
	return out
}

// resolve settles every plugin's status: switches first, then what the
// machine lacks, then — repeatedly, until nothing changes — what other
// plugins fail to provide. A plugin waiting on a pending plugin is pending
// too, with the chain spelled out, so the fix is always findable from the one
// you are looking at.
func resolve(list []*Plugin, st State) {
	for _, p := range list {
		if p.Status == Broken {
			continue
		}
		if st.disabled(p.Name) {
			p.Status = Disabled
			continue
		}
		p.Reasons = append(p.Reasons, unmet(p)...)
		// A bin folder counts even when nothing in it looks runnable today:
		// it goes on PATH all the same, and what is runnable is up to the OS.
		if p.Scope != ScopeAgent && p.Kind == KindPlugin && !st.trusted(p.Name) && (p.runsCode() || p.BinDir != "") {
			p.Untrusted = true
		}
		if len(p.Reasons) > 0 && !onlySoft(p) {
			p.Status = Pending
		} else {
			p.Status = Active
		}
	}

	for changed := true; changed; {
		changed = false
		provided := map[string]bool{}
		for _, p := range list {
			if p.Status == Active {
				provided[p.Name] = true
				for _, c := range p.Provides {
					provided[c] = true
				}
			}
		}
		for _, p := range list {
			if p.Status != Active {
				continue
			}
			for _, dep := range p.Inject.Required {
				if !provided[dep] {
					p.Status = Pending
					p.Reasons = append(p.Reasons, "waits for "+dep+waitingOn(list, dep))
					changed = true
				}
			}
		}
	}
}

// onlySoft says the plugin's reasons are notes, not blockers — a missing
// prompt_file degrades the plugin rather than stopping it.
func onlySoft(p *Plugin) bool {
	for _, r := range p.Reasons {
		if !strings.HasPrefix(r, "prompt_file: ") {
			return false
		}
	}
	return true
}

// waitingOn explains why a dependency is not there, when it is there but
// stuck — the difference between "install X" and "configure X".
func waitingOn(list []*Plugin, dep string) string {
	for _, q := range list {
		if q.Name == dep || contains(q.Provides, dep) {
			if q.Status != Active {
				return fmt.Sprintf(" (%s is %s)", q.Name, q.Status)
			}
		}
	}
	return " (no plugin provides it)"
}

// unmet lists what the machine or the agent's settings lack for this plugin.
func unmet(p *Plugin) []string {
	if p.Kind != KindPlugin {
		return nil
	}
	var out []string
	if len(p.Requires.OS) > 0 && !contains(p.Requires.OS, runtime.GOOS) {
		out = append(out, fmt.Sprintf("runs on %s, not %s", strings.Join(p.Requires.OS, "/"), runtime.GOOS))
	}
	for _, bin := range p.Requires.Bins {
		if _, err := exec.LookPath(bin); err != nil && !contains(p.Commands, bin) {
			out = append(out, "needs `"+bin+"` on PATH")
		}
	}
	for _, env := range p.Requires.Env {
		if os.Getenv(env) == "" && p.EnvVars[env] == "" {
			out = append(out, "needs $"+env)
		}
	}
	keys := make([]string, 0, len(p.Config))
	for k := range p.Config {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		f := p.Config[key]
		v, set := p.Values[key]
		switch {
		case f.Required && !set:
			out = append(out, fmt.Sprintf("needs config %s (bomclaw plugins config %s %s=…)", key, p.Name, key))
		case set && len(f.Enum) > 0 && !contains(f.Enum, v):
			out = append(out, fmt.Sprintf("config %s=%q is not one of %v", key, v, f.Enum))
		}
	}
	return out
}

func (p *Plugin) runsCode() bool {
	return len(p.Commands) > 0 || len(p.EnvVars) > 0 || len(p.Servers) > 0 || len(p.HookList) > 0
}

// expander substitutes ${plugin_dir} and ${config.<key>}. Settings the
// redact schema marks secret become a placeholder instead of their value.
func expander(dir, name string, values map[string]string, redact map[string]ConfigField) func(string) string {
	return func(s string) string {
		return os.Expand(s, func(key string) string {
			if key == "plugin_dir" {
				return dir
			}
			if c, ok := strings.CutPrefix(key, "config."); ok {
				if redact[c].Secret {
					return "<secret: $" + ConfigEnvName(name, c) + ">"
				}
				return values[c]
			}
			return "${" + key + "}" // not ours: leave it for the shell
		})
	}
}

func expandServer(s MCPServer, expand func(string) string) MCPServer {
	out := MCPServer{Command: expand(s.Command), URL: expand(s.URL)}
	for _, a := range s.Args {
		out.Args = append(out.Args, expand(a))
	}
	if len(s.Env) > 0 {
		out.Env = map[string]string{}
		for k, v := range s.Env {
			out.Env[k] = expand(v)
		}
	}
	if len(s.Headers) > 0 {
		out.Headers = map[string]string{}
		for k, v := range s.Headers {
			out.Headers[k] = expand(v)
		}
	}
	return out
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// executables lists the runnable files directly in a bin folder.
func executables(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if runnable(e.Name(), info.Mode()) {
			out = append(out, e.Name())
		}
	}
	return out
}

func cut(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return strings.TrimSpace(string(r[:max])) + "…"
}

func absOrEmpty(dir string) string {
	if dir == "" {
		return ""
	}
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return dir
}

// runnable says whether a file in bin/ is a command. On Windows there is no
// executable bit — what makes a file runnable from PATH is an extension in
// PATHEXT, so that is the test; anything else (a README, a .py the command
// wraps) would be offered to the agent as a command that cannot run.
func runnable(name string, mode os.FileMode) bool {
	if runtime.GOOS != "windows" {
		return mode&0o111 != 0
	}
	exts := os.Getenv("PATHEXT")
	if exts == "" {
		exts = ".COM;.EXE;.BAT;.CMD"
	}
	ext := strings.ToUpper(filepath.Ext(name))
	for _, e := range strings.Split(exts, ";") {
		if ext != "" && strings.EqualFold(strings.TrimSpace(e), ext) {
			return true
		}
	}
	return false
}
