# Plugins: everything an agent can do is a plugin

Status: shipped on `feat/plugins` (2026-11-04). Code: `internal/plugins`,
`cmd/bomclaw/plugincmd.go`, bundled skill `internal/skills/defaults/plugins`.

## Where the idea comes from

[DeepSeek Harness](https://github.com/deepseek-ai/deepseek-harness) (DSH) is
built on [Cordis](https://github.com/cordiverse/cordis) and takes "everything is
a plugin" literally. The model adapter, the tool registry, the prompt assembly
and even the agent loop are ordinary plugins on one context. The parts that
matter here:

| Cordis / DSH mechanism | What it does |
|---|---|
| `ctx.plugin(p, config)` → Fiber (PENDING → ACTIVE → DISPOSED / FAILED) | Every plugin has a visible lifecycle state |
| `inject: ['tools']` | A plugin stays PENDING until each service it needs exists, then loads by itself |
| Effects / disposers | Everything a plugin registered goes away when it unloads; nothing leaks |
| `Config` schema (Schemastery) | Settings are validated and defaulted before `apply`; invalid config puts the plugin in FAILED with the exact path |
| Skills catalog (`dsh-skill`, `dsh-tool-skill`) | The model sees name + short description only, and the body loads on demand. Nearest scope wins a name collision |
| Scope layers, `isolate` | Global, then per-agent, then per-session registrations; nearest wins |
| `--dump-config`, PENDING listing | Dependency-driven systems fail silently, so the inspection tool exists from day one |

## The constraint that shapes the port

In DSH the plugins run inside the harness process, and tools are JavaScript
functions on `ctx.tools`. Here the agent loop belongs to a third-party CLI
(claude, codex or opencode), and the gateway only starts it. The gateway can't
register an in-process tool, intercept a tool call, or keep a plugin alive
between turns. What it controls for each turn is:

1. the system prompt,
2. the subprocess environment (PATH and variables),
3. the MCP servers the CLI starts.

All three backends accept MCP servers per invocation:
- claude: `--mcp-config` with `--strict-mcp-config`;
- codex: `-c mcp_servers.<n>.*`;
- opencode: `OPENCODE_CONFIG_CONTENT`.

So a plugin here is a declaration of what to put into those three channels,
plus skills. That gives real tools (MCP), real commands (PATH) and knowledge
(skills and prompt) on every backend, with no plugin code in the gateway.

## Mapping

| Cordis / DSH | Here |
|---|---|
| Plugin | `plugins/<name>/plugin.yaml` in the agent workspace, or `.agents/plugins/<name>` in a repo |
| A bare skill folder | An implicit plugin (`kind: skill`). Same list, same enable/disable switch, and it can satisfy an `inject` |
| Service / `provides` | `provides: [capability]`. A plugin always provides its own name |
| `inject` | `inject: [a]`, or `{required: [...], optional: [...]}` |
| PENDING (waiting for a service) | `pending` with a reason, e.g. `waits for forge (gitea is disabled)`. It wakes up on the first turn after the cause is fixed |
| FAILED (bad config) | `broken` for an unreadable manifest. A missing or out-of-enum setting makes it `pending` with the exact `bomclaw plugins config …` to run |
| Config schema | `config: {key: {default, required, secret, env, enum}}`. Values reach commands and servers as `$BOMCLAW_<PLUGIN>_<KEY>` and `${config.key}` |
| Effects and dispose | The toolkit is recomputed for every turn and nothing is written elsewhere, so disabling a plugin leaves nothing behind |
| Hot reload | Same mechanism: any change applies on the next turn, with no restart |
| Scope layers | Agent workspace, then the turn's folder, then the repo's `.agents/plugins`. Later wins a name collision |
| `--dump-config` | `bomclaw plugins list / show / prompt`, which use the same `plugins.Load` a turn uses |
| Progressive skill disclosure | Unchanged: the index lists name, description and path, and the agent reads the body |

## Trust

A plugin in a repository is someone else's code. Project-scope plugins
contribute skills and prompt right away. Their commands, environment and MCP
servers stay off until the owner runs `bomclaw plugins trust <name>`. DSH has
no equivalent because it only loads what its config names. Here the agent
walks into arbitrary repos.

Even trusted plugins add their `bin/` to the end of PATH, so a plugin can add
commands but can't shadow `git`. Secret settings are replaced with a
placeholder anywhere they would reach the prompt. `plugins.yaml` is written
with mode 0600.

## Deliberately not ported

- **Waterfall hooks around tool execution** (`tools/pre-execute`). The tools
  run inside the CLI, so the gateway never sees the call in time. Claude Code
  has its own hooks; a plugin could contribute those later, for claude only.
- **In-process plugin code.** A broken plugin can fail a turn, never the
  gateway. Plugin code runs as commands or MCP servers in the turn's own
  process group.
- **Prompt-cache-aware catalog diffing.** claude re-sends the appended prompt
  every turn and it is cached as a prefix. codex and opencode fold the prompt
  into the first message of a thread, so a plugin change reaches their prompt
  on the next new thread. Env and MCP changes reach them on the next turn.

## Install and the hub (2026-11-04, `feat/plugins-hub`)

- `bomclaw plugins install <git-url|folder> [--ref] [--path] [--name] [--replace] [--trust]`,
  `update <name>`, `uninstall <name>` (`internal/plugins/install.go`).
  - `git clone --depth 1` with `protocol.ext.allow=never` and no terminal
    prompt. Only https/http/ssh/git/file and scp-style URLs are accepted, and
    nothing starting with `-`.
  - Symlinks and special files are never copied. Size is capped at 64 MB and
    5000 files.
  - The new folder is staged next to the old one and swapped in with renames.
  - `.source.yaml` records url, ref, path and commit, so `update` and copies
    can fetch from upstream again.
- An installed plugin is its own scope (`installed`) and needs trust, the same
  rule as a repo plugin. Installing or updating **withdraws** trust: trust was
  given to the code that was there. Uninstalling forgets the plugin's settings,
  so secrets meant for it never flow to a later plugin with the same name.
- Gateway: `plugins.list|set|config|install|update|uninstall|copy`. They are
  answered by the dashboard process straight from the workspaces, so they keep
  working while an agent is down. A secret never leaves the server: the hub
  only learns whether it is set.
- Dashboard: an Admin → Plugins pane with one column per agent.
  - Each row shows status and reasons, plus chips for what the plugin brings,
    provides and injects.
  - Settings forms are generated from the plugin's schema.
  - Per row: install, enable/disable, trust, update, copy to another agent,
    uninstall.

## Next steps, if wanted

- Hooks around tool calls, normalised through one `bomclaw hook` shim.
  - Claude: through `--settings`; verified to block a Bash call under
    `bypassPermissions`.
  - opencode: a generated plugin, passed in `OPENCODE_CONFIG_CONTENT`.
  - codex: `-c features.hooks=true -c hooks.*`. Decided:
    `--dangerously-bypass-hook-trust` on codex calls, because the plugin
    trust gate already vets hook sources.
- Peer roster: list a peer's plugins next to its skills, so delegation can
  route on tools as well as knowledge.

## Verification

- `go test ./internal/plugins` covers:
  - resolution: pending chains, provides/inject, disable;
  - trust, secret redaction, PATH appending, collisions, broken manifests;
  - the per-backend renderings.
- `PLUGINS_LIVE=claude,codex go test ./internal/plugins -run Live -v` drives
  the real CLIs. A plugin ships a stdio MCP server and a `bin/` command, and
  the model must return both answers, including a configured value.
  - Passed on claude (`mcp__oracle__get_magic_word`) and codex
    (`oracle.get_magic_word`).
  - opencode was checked at the config layer: `opencode mcp list` with the
    kit's `OPENCODE_CONFIG_CONTENT` reports the server connected. The live
    turn could not run because this machine's opencode API key has expired.
