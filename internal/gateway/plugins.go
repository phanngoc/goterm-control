package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/ngocp/goterm-control/internal/plugins"
	"github.com/ngocp/goterm-control/internal/skills"
)

// The plugins hub: every agent's toolkit, as the agent's next turn will see
// it, with the switches that change it.
//
// Like the skills hub it reads and writes workspaces directly, so it answers
// from the dashboard process while an agent's gateway is down — which is when
// someone is likely to be asking why a plugin is not doing anything. Every
// row is computed by plugins.Load, the same call a turn makes, so the status
// and reason shown are the ones the agent is acting under.

// AgentPlugins is one agent's toolkit.
type AgentPlugins struct {
	AgentID   string      `json:"agent_id"`
	Workspace string      `json:"workspace"`
	Online    bool        `json:"online"`
	Provider  string      `json:"provider,omitempty"`
	Plugins   []HubPlugin `json:"plugins"`
	// Missing names bundled defaults this agent no longer has, so the hub
	// can offer to put them back.
	Missing  []string `json:"missing,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	Error    string   `json:"error,omitempty"`
}

// HubPlugin is a plugin as the hub shows it. Settings are listed with their
// schema; a secret's value never leaves the server, only whether it is set.
type HubPlugin struct {
	Name        string          `json:"name"`
	Kind        plugins.Kind    `json:"kind"`
	Scope       plugins.Scope   `json:"scope"`
	Status      plugins.Status  `json:"status"`
	Reasons     []string        `json:"reasons,omitempty"`
	Description string          `json:"description,omitempty"`
	Version     string          `json:"version,omitempty"`
	Dir         string          `json:"dir"`
	Trusted     bool            `json:"trusted"`
	Untrusted   bool            `json:"untrusted"`
	NeedsTrust  bool            `json:"needs_trust"`
	Skills      []PluginSkill   `json:"skills,omitempty"`
	Commands    []string        `json:"commands,omitempty"`
	MCP         []string        `json:"mcp,omitempty"`
	Hooks       []string        `json:"hooks,omitempty"` // "pre_tool bash", "post_tool *"
	Prompt      string          `json:"prompt,omitempty"`
	Provides    []string        `json:"provides,omitempty"`
	Injects     []string        `json:"injects,omitempty"`
	Source      *plugins.Source `json:"source,omitempty"`
	Settings    []HubSetting    `json:"settings,omitempty"`
}

// PluginSkill is one skill a plugin carries, as the hub lists it. Skills
// live inside plugins now, so this is where they are read and managed.
type PluginSkill struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Path        string `json:"path"`
	// Bundled says a default of this name ships with bomclaw; Edited says
	// the agent's copy no longer matches it — the mark of something this
	// agent learned that its peers have not.
	Bundled bool `json:"bundled,omitempty"`
	Edited  bool `json:"edited,omitempty"`
}

// HubSetting is one field of a plugin's config schema and its current value.
type HubSetting struct {
	Key         string   `json:"key"`
	Description string   `json:"description,omitempty"`
	Required    bool     `json:"required,omitempty"`
	Secret      bool     `json:"secret,omitempty"`
	Enum        []string `json:"enum,omitempty"`
	Default     string   `json:"default,omitempty"`
	Env         string   `json:"env,omitempty"` // the gateway variable it falls back to
	Var         string   `json:"var"`           // what the plugin's code reads
	Value       string   `json:"value,omitempty"`
	// Set is whether the plugin has a value at all, from any source;
	// Configured is whether the agent's own state holds one — the only kind
	// clearing can remove.
	Set        bool `json:"set"`
	Configured bool `json:"configured"`
}

func handlePluginsList(deps Deps, _ json.RawMessage) (json.RawMessage, error) {
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	agents, err := deps.Coord.ListAgents()
	if err != nil {
		return nil, err
	}
	out := make([]AgentPlugins, 0, len(agents))
	for _, a := range agents {
		row := AgentPlugins{AgentID: a.ID, Workspace: a.Workspace, Online: a.Online,
			Provider: a.Provider, Plugins: []HubPlugin{}}
		if a.Workspace == "" {
			row.Error = "no workspace registered — it has not started since workspaces were recorded"
			out = append(out, row)
			continue
		}
		row.Plugins, row.Warnings = hubPlugins(a.Workspace)
		row.Missing = missingDefaults(row.Plugins)
		out = append(out, row)
	}
	return json.Marshal(out)
}

// hubPlugins is one workspace's toolkit, shaped for the hub. Only the agent's
// own folder: a project's plugins depend on which project a turn is in, and
// those show up on that project's turns, not in the agent's standing kit.
func hubPlugins(workspace string) ([]HubPlugin, []string) {
	k := plugins.Load(workspace, "")
	st, _ := plugins.LoadState(workspace)
	trusted := map[string]bool{}
	for _, n := range st.Trust {
		trusted[n] = true
	}
	list := make([]HubPlugin, 0, len(k.Plugins))
	for _, p := range k.Plugins {
		h := HubPlugin{
			Name: p.Name, Kind: p.Kind, Scope: p.Scope, Status: p.Status, Reasons: p.Reasons,
			Description: p.Description, Version: p.Version, Dir: p.Dir,
			Trusted: trusted[p.Name], Untrusted: p.Untrusted,
			NeedsTrust: p.Kind == plugins.KindPlugin && p.Scope != plugins.ScopeAgent,
			Commands:   p.Commands, Prompt: p.PromptText, Provides: p.Provides,
		}
		for _, s := range p.SkillList {
			ps := PluginSkill{Name: s.Name, Description: s.Description, Path: s.Path}
			if orig, err := skills.Default(s.Name); err == nil {
				ps.Bundled = true
				if body, err := os.ReadFile(s.Path); err == nil {
					ps.Edited = !bytes.Equal(bytes.TrimSpace(body), bytes.TrimSpace(orig))
				}
			}
			h.Skills = append(h.Skills, ps)
		}
		for name := range p.Servers {
			h.MCP = append(h.MCP, name)
		}
		sort.Strings(h.MCP)
		for _, hk := range p.HookList {
			m := hk.Match
			if m == "" {
				m = "*"
			}
			line := hk.Event + " " + m
			if hk.FailClosed {
				line += " (fail-closed)"
			}
			h.Hooks = append(h.Hooks, line)
		}
		h.Injects = append(h.Injects, p.Inject.Required...)
		for _, o := range p.Inject.Optional {
			h.Injects = append(h.Injects, o+"?")
		}
		if src, err := plugins.ReadSource(p.Dir); err == nil {
			h.Source = &src
		}
		keys := make([]string, 0, len(p.Config))
		for key := range p.Config {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			f := p.Config[key]
			v, set := p.Values[key]
			s := HubSetting{Key: key, Description: f.Description, Required: f.Required, Secret: f.Secret,
				Enum: f.Enum, Default: f.Default, Env: f.Env, Var: plugins.ConfigEnvName(p.Name, key), Set: set,
				Configured: st.Config[p.Name][key] != ""}
			if !f.Secret {
				s.Value = v
			}
			h.Settings = append(h.Settings, s)
		}
		list = append(list, h)
	}
	return list, k.Errors
}

type pluginParams struct {
	AgentID string `json:"agent_id"`
	Name    string `json:"name"`
	// plugins.set: either or both.
	Enabled *bool `json:"enabled,omitempty"`
	Trusted *bool `json:"trusted,omitempty"`
	// plugins.config: key → value; "" clears a setting.
	Values map[string]string `json:"values,omitempty"`
	// plugins.install
	URL     string `json:"url,omitempty"`
	Ref     string `json:"ref,omitempty"`
	Path    string `json:"path,omitempty"`
	Replace bool   `json:"replace,omitempty"`
	Trust   bool   `json:"trust,omitempty"`
	// plugins.copy: the agent whose plugin to copy.
	From string `json:"from,omitempty"`
}

// handlePluginsMutate answers every plugins.* method that changes something,
// and returns the agent's toolkit as it now stands so the hub can redraw
// without a second round trip.
func handlePluginsMutate(deps Deps, method string, params json.RawMessage) (json.RawMessage, error) {
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	var p pluginParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	ws, err := workspaceOf(deps, p.AgentID)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"ok": true, "agent_id": p.AgentID}

	switch method {
	case "plugins.set":
		if p.Name == "" || (p.Enabled == nil && p.Trusted == nil) {
			return nil, fmt.Errorf("name and enabled or trusted are required")
		}
		st, err := plugins.LoadState(ws)
		if err != nil {
			return nil, err
		}
		if p.Enabled != nil {
			st.SetEnabled(p.Name, *p.Enabled)
		}
		if p.Trusted != nil {
			st.SetTrusted(p.Name, *p.Trusted)
		}
		if err := st.Save(ws); err != nil {
			return nil, err
		}
	case "plugins.config":
		if err := setPluginConfig(ws, p.Name, p.Values); err != nil {
			return nil, err
		}
	case "plugins.install":
		inst, err := plugins.Install(ws, plugins.Source{URL: p.URL, Ref: p.Ref, Path: p.Path},
			plugins.InstallOptions{Name: p.Name, Replace: p.Replace, Trust: p.Trust})
		if err != nil {
			return nil, err
		}
		result["installed"] = inst
	case "plugins.update":
		inst, err := plugins.Update(ws, p.Name, p.Trust)
		if err != nil {
			return nil, err
		}
		result["installed"] = inst
	case "plugins.uninstall":
		if err := plugins.Uninstall(ws, p.Name); err != nil {
			return nil, err
		}
	case "plugins.copy":
		from, err := workspaceOf(deps, p.From)
		if err != nil {
			return nil, err
		}
		inst, err := plugins.Copy(from, ws, p.Name)
		if err != nil {
			return nil, err
		}
		result["installed"] = inst
	default:
		return nil, fmt.Errorf("unknown method %s", method)
	}

	list, warnings := hubPlugins(ws)
	result["plugins"], result["warnings"] = list, warnings
	return json.Marshal(result)
}

// setPluginConfig stores settings, refusing keys the plugin's schema does
// not declare: a typo saved silently is a setting that never takes effect,
// and the hub would show it as set.
func setPluginConfig(ws, name string, values map[string]string) error {
	if len(values) == 0 {
		return fmt.Errorf("values is required")
	}
	p := plugins.Load(ws, "").Find(name)
	if p == nil || p.Kind != plugins.KindPlugin {
		return fmt.Errorf("no plugin %q", name)
	}
	for key := range values {
		if _, ok := p.Config[key]; !ok {
			return fmt.Errorf("%s has no setting %q", name, key)
		}
	}
	st, err := plugins.LoadState(ws)
	if err != nil {
		return err
	}
	for key, v := range values {
		st.SetConfig(name, key, v)
	}
	return st.Save(ws)
}

type pluginFilesParams struct {
	AgentID string `json:"agent_id"`
	Name    string `json:"name"`
	// Path set reads that file; empty lists the whole plugin.
	Path string `json:"path,omitempty"`
}

// handlePluginFiles lets the hub look inside a plugin: the whole tree in one
// call (plugins are small), then one file at a time. Read-only.
func handlePluginFiles(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	var p pluginFilesParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	ws, err := workspaceOf(deps, p.AgentID)
	if err != nil {
		return nil, err
	}
	pl := plugins.Load(ws, "").Find(p.Name)
	if pl == nil {
		return nil, fmt.Errorf("%s has no plugin %q", p.AgentID, p.Name)
	}
	if p.Path == "" {
		entries, truncated, err := plugins.Tree(pl.Dir)
		if err != nil {
			return nil, err
		}
		if entries == nil {
			entries = []plugins.Entry{}
		}
		return json.Marshal(map[string]any{"dir": pl.Dir, "entries": entries, "truncated": truncated})
	}
	body, truncated, binary, err := plugins.ReadFile(pl.Dir, p.Path)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"path": p.Path, "body": body, "truncated": truncated, "binary": binary})
}

// missingDefaults lists the bundled defaults no plugin of this agent carries.
func missingDefaults(list []HubPlugin) []string {
	has := map[string]bool{}
	for _, p := range list {
		has[p.Name] = true
		for _, s := range p.Skills {
			has[s.Name] = true
		}
	}
	var out []string
	for _, n := range skills.DefaultNames() {
		if !has[n] {
			out = append(out, n)
		}
	}
	return out
}
