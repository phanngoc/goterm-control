package plugins

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// StateFile is an agent's own switches for its toolkit: what is turned off,
// which project plugins it trusts to run code, and each plugin's settings.
//
// One file per agent, in the agent's workspace, because the toolkit is
// per-agent (see skills: divergence is the point). It is separate from the
// plugins themselves so that replacing a plugin with a peer's improved copy
// never resets how this agent configured it.
const StateFile = "plugins.yaml"

// State is the contents of StateFile.
type State struct {
	Disabled []string `yaml:"disabled,omitempty"`
	// Trust names project plugins allowed to contribute commands,
	// environment and MCP servers. A repository is someone else's code: its
	// plugins may teach (skills, prompt) without asking, but they do not get
	// to put programs in front of the agent until the agent's owner says so.
	Trust  []string                     `yaml:"trust,omitempty"`
	Config map[string]map[string]string `yaml:"config,omitempty"`
}

// LoadState reads an agent's state. A missing file is the empty state.
func LoadState(workspace string) (State, error) {
	var st State
	if workspace == "" {
		return st, nil
	}
	data, err := os.ReadFile(filepath.Join(workspace, StateFile))
	if os.IsNotExist(err) {
		return st, nil
	}
	if err != nil {
		return st, fmt.Errorf("plugins: %w", err)
	}
	if err := yaml.Unmarshal(data, &st); err != nil {
		return st, fmt.Errorf("plugins: %s: %w", StateFile, err)
	}
	return st, nil
}

// Save writes the state atomically: the gateway may be reading it for a turn
// at this moment, and a half-written file would turn every plugin back on.
func (st State) Save(workspace string) error {
	if workspace == "" {
		return fmt.Errorf("plugins: this agent has no workspace")
	}
	sort.Strings(st.Disabled)
	sort.Strings(st.Trust)
	data, err := yaml.Marshal(st)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(workspace, ".plugins-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// 0600: settings can hold secrets.
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(workspace, StateFile))
}

func (st State) disabled(name string) bool { return contains(st.Disabled, name) }
func (st State) trusted(name string) bool  { return contains(st.Trust, name) }

// SetEnabled turns a plugin (or bare skill) on or off by name.
func (st *State) SetEnabled(name string, on bool) {
	st.Disabled = remove(st.Disabled, name)
	if !on {
		st.Disabled = append(st.Disabled, name)
	}
}

// SetTrusted grants or withdraws a project plugin's right to run code.
func (st *State) SetTrusted(name string, on bool) {
	st.Trust = remove(st.Trust, name)
	if on {
		st.Trust = append(st.Trust, name)
	}
}

// SetConfig stores one setting; an empty value removes it, so the plugin's
// default (or its env fallback) applies again.
func (st *State) SetConfig(plugin, key, value string) {
	if st.Config == nil {
		st.Config = map[string]map[string]string{}
	}
	if value == "" {
		delete(st.Config[plugin], key)
		if len(st.Config[plugin]) == 0 {
			delete(st.Config, plugin)
		}
		return
	}
	if st.Config[plugin] == nil {
		st.Config[plugin] = map[string]string{}
	}
	st.Config[plugin][key] = value
}

func remove(list []string, s string) []string {
	out := list[:0:0]
	for _, v := range list {
		if v != s {
			out = append(out, v)
		}
	}
	return out
}
