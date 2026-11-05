package gateway

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pluginsCall(t *testing.T, deps Deps, method string, params any) (json.RawMessage, error) {
	t.Helper()
	raw, _ := json.Marshal(params)
	if method == "plugins.list" {
		return handlePluginsList(deps, raw)
	}
	return handlePluginsMutate(deps, method, raw)
}

func writePlugin(t *testing.T, dir, manifest string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
}

func findHub(t *testing.T, list []HubPlugin, name string) HubPlugin {
	t.Helper()
	for _, p := range list {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("no %q in %+v", name, list)
	return HubPlugin{}
}

func TestPluginsHubConfiguresWithoutLeakingSecrets(t *testing.T) {
	deps, wsA, _ := hubFixture(t)
	writePlugin(t, filepath.Join(wsA, "plugins", "forge"), `
description: forge
config:
  token: {required: true, secret: true}
  org: {default: acme}
`)
	var rows []AgentPlugins
	out, err := pluginsCall(t, deps, "plugins.list", nil)
	if err != nil || json.Unmarshal(out, &rows) != nil {
		t.Fatalf("list: %v", err)
	}
	var mine AgentPlugins
	for _, r := range rows {
		if r.AgentID == "bomclaw" {
			mine = r
		}
	}
	forge := findHub(t, mine.Plugins, "forge")
	if forge.Status != "pending" || !strings.Contains(strings.Join(forge.Reasons, ";"), "token") {
		t.Fatalf("forge: %+v", forge)
	}
	// Bundled defaults ship as plugins, and are in the same list.
	if b := findHub(t, mine.Plugins, "browser"); b.Kind != "plugin" || len(b.Skills) != 1 || b.Skills[0].Name != "browser" {
		t.Fatalf("bundled browser plugin: %+v", b)
	}

	if _, err := pluginsCall(t, deps, "plugins.config", map[string]any{
		"agent_id": "bomclaw", "name": "forge", "values": map[string]string{"tokn": "x"}}); err == nil {
		t.Fatal("an undeclared setting was accepted")
	}
	out, err = pluginsCall(t, deps, "plugins.config", map[string]any{
		"agent_id": "bomclaw", "name": "forge", "values": map[string]string{"token": "s3cret"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "s3cret") {
		t.Fatalf("a secret left the server: %s", out)
	}
	var res struct{ Plugins []HubPlugin }
	_ = json.Unmarshal(out, &res)
	forge = findHub(t, res.Plugins, "forge")
	if forge.Status != "active" {
		t.Fatalf("configured plugin still %s: %v", forge.Status, forge.Reasons)
	}
	for _, s := range forge.Settings {
		if s.Key == "token" && (!s.Set || s.Value != "") {
			t.Fatalf("token setting: %+v", s)
		}
		if s.Key == "org" && s.Value != "acme" {
			t.Fatalf("org default not shown: %+v", s)
		}
	}
}

func TestPluginsHubInstallTrustCopyUninstall(t *testing.T) {
	deps, wsA, wsB := hubFixture(t)
	src := filepath.Join(t.TempDir(), "tool")
	writePlugin(t, src, "name: tool\nenv: {X: '1'}\n")

	out, err := pluginsCall(t, deps, "plugins.install", map[string]any{"agent_id": "bomclaw", "url": src})
	if err != nil {
		t.Fatal(err)
	}
	var res struct{ Plugins []HubPlugin }
	_ = json.Unmarshal(out, &res)
	tool := findHub(t, res.Plugins, "tool")
	if tool.Scope != "installed" || !tool.NeedsTrust || !tool.Untrusted || tool.Source == nil {
		t.Fatalf("installed: %+v", tool)
	}

	yes := true
	out, err = pluginsCall(t, deps, "plugins.set", map[string]any{"agent_id": "bomclaw", "name": "tool", "trusted": yes})
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(out, &res)
	if tool = findHub(t, res.Plugins, "tool"); tool.Untrusted || !tool.Trusted {
		t.Fatalf("after trust: %+v", tool)
	}

	if _, err := pluginsCall(t, deps, "plugins.copy", map[string]any{"from": "bomclaw", "agent_id": "bomclaw3", "name": "tool"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wsB, "plugins", "tool", "plugin.yaml")); err != nil {
		t.Fatal("copy did not reach the other agent")
	}

	if _, err := pluginsCall(t, deps, "plugins.uninstall", map[string]any{"agent_id": "bomclaw", "name": "tool"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wsA, "plugins", "tool")); !os.IsNotExist(err) {
		t.Fatal("uninstall left the folder")
	}
	if _, err := pluginsCall(t, deps, "plugins.set", map[string]any{"agent_id": "nobody", "name": "tool", "enabled": yes}); err == nil {
		t.Fatal("an unknown agent was accepted")
	}
}

func TestPluginsHubBrowsesAPluginsFiles(t *testing.T) {
	deps, wsA, _ := hubFixture(t)
	writePlugin(t, filepath.Join(wsA, "plugins", "forge"), "name: forge\n")
	if err := os.WriteFile(filepath.Join(wsA, "plugins.yaml"), []byte("config: {forge: {token: s3cret}}"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"agent_id": "bomclaw", "name": "forge"})
	out, err := handlePluginFiles(deps, raw)
	if err != nil || !strings.Contains(string(out), `"path":"plugin.yaml"`) {
		t.Fatalf("tree: %s %v", out, err)
	}
	raw, _ = json.Marshal(map[string]any{"agent_id": "bomclaw", "name": "forge", "path": "plugin.yaml"})
	if out, err = handlePluginFiles(deps, raw); err != nil || !strings.Contains(string(out), "name: forge") {
		t.Fatalf("read: %s %v", out, err)
	}
	raw, _ = json.Marshal(map[string]any{"agent_id": "bomclaw", "name": "forge", "path": "../../plugins.yaml"})
	if _, err := handlePluginFiles(deps, raw); err == nil {
		t.Fatal("read outside the plugin")
	}
}

// Skills are managed from the plugins hub now: each plugin lists its skills
// with the edited mark, and a removed default is offered back.
func TestPluginsHubManagesSkills(t *testing.T) {
	deps, wsA, _ := hubFixture(t)
	// The agent revises its browser skill and loses its preview plugin.
	if err := os.WriteFile(filepath.Join(wsA, "plugins", "browser", "SKILL.md"),
		[]byte("---\nname: browser\ndescription: mine\n---\nlearned\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(wsA, "plugins", "preview")); err != nil {
		t.Fatal(err)
	}
	out, err := pluginsCall(t, deps, "plugins.list", nil)
	if err != nil {
		t.Fatal(err)
	}
	var rows []AgentPlugins
	_ = json.Unmarshal(out, &rows)
	var mine AgentPlugins
	for _, r := range rows {
		if r.AgentID == "bomclaw" {
			mine = r
		}
	}
	b := findHub(t, mine.Plugins, "browser")
	if len(b.Skills) != 1 || !b.Skills[0].Bundled || !b.Skills[0].Edited || b.Skills[0].Description != "mine" {
		t.Fatalf("browser skill: %+v", b.Skills)
	}
	if a := findHub(t, mine.Plugins, "artifacts"); a.Skills[0].Edited {
		t.Fatal("an untouched default is marked edited")
	}
	if len(mine.Missing) != 1 || mine.Missing[0] != "preview" {
		t.Fatalf("missing = %v", mine.Missing)
	}
}

// Per-project plugins, both kinds: an agent plugin pinned to a room shows
// where it applies (and is active there, not "elsewhere"), and a plugin that
// lives in a project's repo shows under that project — it never appeared in
// the hub before, though it is what the room's turns actually load.
func TestPluginsHubShowsPerProjectPlugins(t *testing.T) {
	deps, wsA, _ := hubFixture(t)
	root := t.TempDir()
	film, err := deps.Coord.CreateProject("Phim", "films", "owner", root, nil)
	if err != nil {
		t.Fatal(err)
	}
	writePlugin(t, filepath.Join(wsA, "plugins", "game-kit"), "description: games\n")
	writePlugin(t, filepath.Join(film.Workspace, "plugins", "film-studio"), "description: films\n")

	yes := []string{film.ID}
	if _, err := pluginsCall(t, deps, "plugins.set", map[string]any{"agent_id": "bomclaw", "name": "game-kit", "projects": yes}); err != nil {
		t.Fatal(err)
	}
	mine := func() []HubPlugin {
		var rows []AgentPlugins
		out, err := pluginsCall(t, deps, "plugins.list", nil)
		if err != nil || json.Unmarshal(out, &rows) != nil {
			t.Fatalf("list: %v", err)
		}
		for _, r := range rows {
			if r.AgentID == "bomclaw" {
				return r.Plugins
			}
		}
		return nil
	}
	g := findHub(t, mine(), "game-kit")
	if g.Status != "active" || len(g.Only) != 1 || g.Only[0].ChannelID != film.ID {
		t.Fatalf("pinned plugin: %+v", g)
	}
	f := findHub(t, mine(), "film-studio")
	if f.Project == nil || f.Project.ChannelID != film.ID || f.Scope != "project" {
		t.Fatalf("project plugin: %+v", f)
	}

	none := []string{}
	if _, err := pluginsCall(t, deps, "plugins.set", map[string]any{"agent_id": "bomclaw", "name": "game-kit", "projects": none}); err != nil {
		t.Fatal(err)
	}
	if g := findHub(t, mine(), "game-kit"); len(g.Only) != 0 {
		t.Fatalf("not global again: %+v", g)
	}
	if _, err := pluginsCall(t, deps, "plugins.set", map[string]any{"agent_id": "bomclaw", "name": "game-kit", "projects": []string{"ch_general"}}); err == nil {
		t.Error("pinned to a room with no folder")
	}
}
