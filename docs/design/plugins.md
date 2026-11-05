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

## Skills migrated to plugins (2026-11-04, `feat/skills-to-plugins`)

On disk, an agent's toolkit is now only plugins.
- **Defaults:** the bundled defaults ship as plugins (`internal/skills/defaults/<name>/{SKILL.md,plugin.yaml}`),
  each naming what it `provides`. They are seeded into `<workspace>/plugins/<name>/`.
- **Migration:** `plugins.MigrateSkills` runs on every gateway start and as `bomclaw plugins migrate`.
  - It moves each folder in `skills/` (categories included) into `plugins/<name>/` whole: files beside
    `SKILL.md` and the agent's own edits go with it.
  - It adds the bundled manifest for a default, or a generated one otherwise.
  - It leaves a symlink at the old path. A codex or opencode thread got the skill index once, at its
    start, and still holds the old path.
  - A name already taken by a plugin is left in place and reported, never overwritten.
  - A repository's `.agents/skills` is never touched.
- **Lookup by name:** the hub, the peer roster and `skills.Read/Install/Remove` go through `skills.All`.
  The plugins package provides it via `skills.SetFinder`: the leaf package asks for a skill finder and the
  plugins package provides one, which is Cordis's service injection with Go's import graph in the way.
- **Still supported:** `skills/` keeps working for a skill written the old way. It is offered on the very
  next turn and moved on the next start.

## Hooks around tool calls (2026-11-04, `feat/plugin-hooks`)

A plugin declares `hooks: {pre_tool: [...], post_tool: [...]}`. Each entry
has `match` (a regex on the tool name), `run`, `timeout` and `fail_closed`.

All three CLIs are pointed at one dispatcher, `bomclaw hook <event> --backend
<cli>` (`internal/plugins/hooks.go`). It:
- reads the turn's hooks from `$BOMCLAW_HOOKS`;
- normalises the payload: `Bash`, `shell` and `exec_command` all become
  `bash`, and an argv command is joined into one line;
- runs the matching hooks;
- answers in the form that CLI expects.

| CLI | Registration, per invocation | Refusal | Verified |
|---|---|---|---|
| claude | `--settings {"hooks":{"PreToolUse":…}}` | exit 2 + stderr | live test |
| codex | `--dangerously-bypass-hook-trust -c features.hooks=true -c hooks.PreToolUse=[…]` | exit 2 + stderr | live test |
| opencode | the bridge `<ws>/.bomclaw/opencode-hooks.mjs` in `OPENCODE_CONFIG_CONTENT.plugin` | the bridge throws | the bridge runs under node in a test; the hook signature was read from the opencode binary; no live turn (the API key has expired) |

Rules:
- Hooks are code that runs before every tool call. They only run for active
  plugins that pass the trust gate. That gate is also why bypassing codex's
  own hook-trust prompt is acceptable.
- A hook that breaks (crash, timeout, garbage output) lets the call through
  and is logged, unless it sets `fail_closed`. The dispatcher's own failures
  always let the call through.
- `post_tool` hooks only observe.
- Every decision is appended to `<agent ws>/.bomclaw/hooks.log`, rotated at
  5 MB.
- With no hooks, nothing is registered and every CLI runs exactly as before.

## Next steps, if wanted

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

## Global and per-project plugins (2026-11-05)

A plugin in an agent's workspace is global: every turn loads it, whatever the
room. On 2026-11-05 agent 1 carried the film kit (6 skills and a prompt) and
five game skills into ch_trading and every other room. Two ways to make a
plugin per project:

1. **Live in the project's repo** (`plugins/` or `.agents/plugins/` at the
   project root). Channel turns and tasks run in the project's folder, so the
   repo's plugins load there and nowhere else, for all agents, and a merge is
   the update. This was always supported; the hub now lists these under their
   project.
2. **Pin an agent plugin**: `State.Only` (`only:` in `plugins.yaml`) maps a
   plugin to project folders. Outside them it is `elsewhere` — it contributes
   nothing and is not mentioned in the prompt. Set with
   `bomclaw plugins scope <name> <channel|folder>...` / `--global`, or
   `plugins.set {projects: [...]}` from the hub.

The pin is per agent, like every other switch. Trust is still by name, so
trusting an installed plugin also trusts a project plugin of the same name.
