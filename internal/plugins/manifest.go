// Package plugins turns an agent's toolkit into plugins: folders that can each
// contribute skills, always-on instructions, commands, environment and MCP
// tool servers, and that declare what they need before they can run.
//
// The model is Cordis's ("everything is a plugin", the architecture behind
// Koishi and DeepSeek Harness), cut down to what a gateway driving three CLI
// backends can honour:
//
//   - A plugin is the unit of everything. A bare skill folder is a plugin too —
//     one whose only contribution is that skill — so there is one list to read,
//     one switch to turn things off, and one place a dependency can point at.
//   - Plugins provide capabilities and inject them. A plugin that needs one
//     nobody provides is not an error and not silently dropped: it is PENDING,
//     with the reason on screen, and it becomes active on the first turn after
//     its dependency appears. That is Cordis's "a plugin waits for its
//     service", made per-turn instead of event-driven.
//   - Every contribution is scoped to the plugin and disappears with it.
//     Nothing a plugin adds is written anywhere else — the whole toolkit is
//     recomputed for each turn — so disabling one is the same as it never
//     having been there. That is Cordis's dispose, with no cleanup to forget.
//   - Configuration is declared, validated, and defaulted by the plugin's own
//     schema; a required value that is missing is a pending reason, not a
//     runtime failure halfway through a turn.
//
// What it deliberately does not take from Cordis: in-process code. A plugin
// here never runs inside the gateway. Its code runs as commands the agent
// calls or as MCP servers the CLI starts, both inside the turn's own process
// group — so a broken plugin can fail a turn but never the gateway.
package plugins

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// File is the manifest that makes a directory a plugin.
const File = "plugin.yaml"

// Dir is the folder inside an agent's workspace that holds its plugins.
const Dir = "plugins"

// ProjectDir is where a source repository keeps plugins that belong to it,
// next to its skills (see skills.ProjectDir).
const ProjectDir = ".agents/plugins"

// MaxManifestBytes bounds one plugin.yaml. A manifest is a declaration; a
// long one is instructions that belong in a skill or prompt file.
const MaxManifestBytes = 64 << 10

// MaxPromptRunes bounds what one plugin adds to every prompt. Always-on text
// is paid on every turn; anything longer belongs in a skill, read on demand.
const MaxPromptRunes = 1500

var nameOK = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ValidName reports whether a plugin, capability or MCP server may be called
// this. Names become directory names, environment variable names and TOML
// keys, so anything that is not safe in all three is refused.
func ValidName(name string) bool { return nameOK.MatchString(name) }

// Manifest is a plugin.yaml.
type Manifest struct {
	Name        string `yaml:"name" json:"name,omitempty"`
	Version     string `yaml:"version" json:"version,omitempty"`
	Description string `yaml:"description" json:"description,omitempty"`

	// Provides names capabilities other plugins can inject. A plugin always
	// provides its own name; this is for the ones it stands in for — a
	// "github" plugin and a "gitea" plugin can both provide "forge".
	Provides []string `yaml:"provides" json:"provides,omitempty"`
	Inject   Inject   `yaml:"inject" json:"inject,omitempty"`
	Requires Requires `yaml:"requires" json:"requires,omitempty"`

	// Config is the schema for this plugin's settings, keyed by setting name.
	Config map[string]ConfigField `yaml:"config" json:"config,omitempty"`

	// Prompt is always-on text added to every turn while the plugin is
	// active; PromptFile is the same, read from a file in the plugin folder.
	Prompt     string `yaml:"prompt" json:"-"`
	PromptFile string `yaml:"prompt_file" json:"prompt_file,omitempty"`

	// Skills is the folder of skills inside the plugin (default "skills").
	// A SKILL.md at the plugin's root is a skill as well.
	Skills string `yaml:"skills" json:"skills,omitempty"`
	// Bin is the folder of commands put on the turn's PATH (default "bin").
	Bin string `yaml:"bin" json:"bin,omitempty"`
	// Env is set in the turn's environment. Values may use ${plugin_dir}
	// and ${config.<key>}.
	Env map[string]string `yaml:"env" json:"-"`
	// MCP is the tool servers this plugin starts, by server name.
	MCP map[string]MCPServer `yaml:"mcp" json:"-"`
}

// Inject lists what a plugin needs from others — plugin names or provided
// capabilities. A required one that is missing keeps the plugin pending; an
// optional one is used if present and does not block anything.
type Inject struct {
	Required []string `yaml:"required" json:"required,omitempty"`
	Optional []string `yaml:"optional" json:"optional,omitempty"`
}

// UnmarshalYAML accepts the short form `inject: [a, b]` as all-required, the
// way Cordis's `inject: ['database']` reads.
func (in *Inject) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.SequenceNode {
		return n.Decode(&in.Required)
	}
	type plain Inject
	return n.Decode((*plain)(in))
}

// Requires is what the machine must have for the plugin to do anything.
type Requires struct {
	Bins []string `yaml:"bins" json:"bins,omitempty"` // commands that must be on PATH
	Env  []string `yaml:"env" json:"-"`               // environment variables that must be set
	OS   []string `yaml:"os" json:"os,omitempty"`     // runtime.GOOS values it runs on; empty = any
}

// ConfigField is one setting in a plugin's schema.
type ConfigField struct {
	Description string `yaml:"description" json:"description,omitempty"`
	Default     string `yaml:"default" json:"default,omitempty"`
	Required    bool   `yaml:"required" json:"required,omitempty"`
	// Secret keeps the value out of prompts and listings. It still reaches
	// the plugin's commands and servers — that is what it is for.
	Secret bool `yaml:"secret" json:"secret,omitempty"`
	// Env names a gateway environment variable to take the value from when
	// none is configured, so a token can live where tokens already live.
	Env string `yaml:"env" json:"-"`
	// Enum, when set, is the complete list of allowed values.
	Enum []string `yaml:"enum" json:"enum,omitempty"`
}

// MCPServer is one tool server, local (Command) or remote (URL).
type MCPServer struct {
	Command string            `yaml:"command" json:"command,omitempty"`
	Args    []string          `yaml:"args" json:"args,omitempty"`
	Env     map[string]string `yaml:"env" json:"env,omitempty"`
	URL     string            `yaml:"url" json:"url,omitempty"`
	Headers map[string]string `yaml:"headers" json:"headers,omitempty"`
}

// ParseManifest reads and checks one plugin.yaml. The folder name is the
// fallback name, as it is for a skill.
func ParseManifest(dir string) (Manifest, error) {
	path := filepath.Join(dir, File)
	info, err := os.Stat(path)
	if err != nil {
		return Manifest{}, err
	}
	if info.Size() > MaxManifestBytes {
		return Manifest{}, fmt.Errorf("%s is %d bytes (max %d)", path, info.Size(), MaxManifestBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("%s: %w", path, err)
	}
	m.Name = strings.TrimSpace(m.Name)
	if m.Name == "" {
		m.Name = filepath.Base(dir)
	}
	if m.Skills == "" {
		m.Skills = "skills"
	}
	if m.Bin == "" {
		m.Bin = "bin"
	}
	return m, m.validate()
}

func (m Manifest) validate() error {
	if !ValidName(m.Name) {
		return fmt.Errorf("plugin name %q: lowercase letters, digits, - and _", m.Name)
	}
	for _, list := range [][]string{m.Provides, m.Inject.Required, m.Inject.Optional} {
		for _, c := range list {
			if !ValidName(c) {
				return fmt.Errorf("capability %q: lowercase letters, digits, - and _", c)
			}
		}
	}
	for name, s := range m.MCP {
		if !ValidName(name) {
			return fmt.Errorf("mcp server %q: lowercase letters, digits, - and _", name)
		}
		if (s.Command == "") == (s.URL == "") {
			return fmt.Errorf("mcp server %q needs exactly one of command or url", name)
		}
	}
	for key, f := range m.Config {
		if !ValidName(key) {
			return fmt.Errorf("config key %q: lowercase letters, digits, - and _", key)
		}
		if f.Default != "" && len(f.Enum) > 0 && !contains(f.Enum, f.Default) {
			return fmt.Errorf("config %q: default %q is not one of %v", key, f.Default, f.Enum)
		}
	}
	for _, rel := range []string{m.Skills, m.Bin, m.PromptFile} {
		if rel != "" && !local(rel) {
			return fmt.Errorf("%q must be a path inside the plugin folder", rel)
		}
	}
	return nil
}

// local reports whether rel stays inside the folder it is relative to.
func local(rel string) bool {
	return !filepath.IsAbs(rel) && filepath.IsLocal(rel)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
