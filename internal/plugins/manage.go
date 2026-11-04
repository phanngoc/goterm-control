package plugins

import (
	"fmt"
	"os"
	"path/filepath"
)

// scaffold is what `bomclaw plugins new` writes: every field present and
// commented, so the agent extending its own toolkit edits rather than
// guesses — and nothing active that would run before it is filled in.
const scaffold = `# A plugin: everything below is optional except name.
name: %s
version: 0.1.0
# The routing line: what this is and WHEN to use it. Shown on every turn.
description: ""

# Capabilities other plugins can inject. Your own name is always provided.
provides: []
# What this needs from other plugins. Missing required ones keep it pending.
inject:
  required: []
  optional: []

# What the machine must have. Missing ones keep it pending, with the reason shown.
requires:
  bins: []   # commands that must be on PATH
  env: []    # environment variables that must be set
  os: []     # darwin, linux, windows; empty = any

# Settings, validated before the plugin loads. Each reaches the plugin's
# commands and servers as BOMCLAW_<PLUGIN>_<KEY>, and can be used below as
# ${config.<key>}. Set one with: bomclaw plugins config %s <key>=<value>
config: {}
#  token: {description: "API token", required: true, secret: true, env: SOME_TOKEN}

# Always-on instructions (keep short; long ones belong in a skill).
prompt: ""

# skills/<name>/SKILL.md are read on demand; bin/* goes on the PATH.
skills: skills
bin: bin

env: {}
#  TOOL_HOME: ${plugin_dir}/data

# Tool servers the CLI starts for each turn — local (command) or remote (url).
mcp: {}
#  myserver:
#    command: ${plugin_dir}/bin/server
#    args: []
#    env: {TOKEN: "${config.token}"}
`

// Scaffold creates an empty plugin in an agent's workspace and returns its
// folder. It refuses to touch an existing one.
func Scaffold(workspace, name string) (string, error) {
	if !ValidName(name) {
		return "", fmt.Errorf("plugins: %q is not usable — lowercase letters, digits, - and _", name)
	}
	if workspace == "" {
		return "", fmt.Errorf("plugins: this agent has no workspace")
	}
	dir := filepath.Join(workspace, Dir, name)
	if _, err := os.Stat(dir); err == nil {
		return "", fmt.Errorf("plugins: %s already exists", dir)
	}
	for _, sub := range []string{"bin", "skills"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return "", err
		}
	}
	body := fmt.Sprintf(scaffold, name, name)
	if err := os.WriteFile(filepath.Join(dir, File), []byte(body), 0o644); err != nil {
		return "", err
	}
	return dir, nil
}
