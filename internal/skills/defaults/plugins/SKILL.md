---
name: plugins
description: 'Extend your own toolkit: commands, MCP tool servers, settings. Use when: a tool would beat repeating a job, someone hands you an API or token, or a plugin shows "Not loaded". NOT for knowledge without code (write a skill).'
---

# plugins

Everything you can do beyond the CLI's own tools is a plugin: a folder under
`plugins/` in your workspace with a `plugin.yaml`. Your skills live there too:
each is a plugin whose `SKILL.md` sits at its root. A skill you write the old
way, as `skills/<name>/SKILL.md`, works at once and is moved into `plugins/` on
the next gateway start (or by `bomclaw plugins migrate`). Plugins are re-read
before every turn, so anything you add, configure or switch off takes effect
on your next turn. Nothing restarts.

## What a plugin can give you

| In plugin.yaml / the folder | What you get                                         |
|-----------------------------|------------------------------------------------------|
| `bin/<cmd>` (executable)    | `<cmd>` on your PATH                                 |
| `mcp: {name: {command…}}`   | that server's tools appear among your own tools      |
| `mcp: {name: {url: …}}`     | a remote MCP server's tools                          |
| `skills/<name>/SKILL.md`    | a skill, read on demand like this one                |
| `prompt:`                   | a few always-on lines (keep them short)              |
| `config:`                   | validated settings → `$BOMCLAW_<PLUGIN>_<KEY>`       |
| `env:`                      | variables in every turn                              |
| `hooks: {pre_tool, post_tool}` | a command run around your tool calls; may refuse one |

This works the same on claude, codex and opencode.

## The loop

```
bomclaw plugins new <name>          # scaffold: plugin.yaml (commented), bin/, skills/
$EDITOR plugins/<name>/plugin.yaml  # fill in what it needs and brings
bomclaw plugins show <name>         # status, and the exact reason if not active
bomclaw plugins config <name> key=value
bomclaw plugins list                # everything, with status
bomclaw plugins prompt              # what you will see on your next turn
bomclaw plugins install <git-url>   # fetch someone's plugin (--ref, --path); update / uninstall
```

Use `${plugin_dir}` for paths inside the plugin and `${config.key}` for
settings. Write secrets as `config` fields with `secret: true` (or
`env: SOME_VAR` to read an existing variable), never in the manifest itself.

## Hooks

```yaml
hooks:
  pre_tool:
    - match: bash                  # regex on the tool: bash, edit, write, read, … (or the CLI's own name)
      run: ${plugin_dir}/guard.sh  # gets JSON on stdin: {event, tool, raw_tool, input, cwd, backend, plugin}
      timeout: 10                  # seconds (max 120)
      fail_closed: false           # true = refuse the call if the hook itself breaks
  post_tool:
    - run: ${plugin_dir}/audit.sh  # also gets "output"; cannot change anything
```

A pre_tool hook refuses a call by exiting 2 with the reason on stderr, or by
printing `{"decision":"deny","reason":"…"}`. You then see that reason as the
tool's error. Do not try to get around a refusal: it is a rule someone set.
Every decision is logged in `.bomclaw/hooks.log` in your workspace.

## Status

- **active**: in use.
- **pending**: something is missing. The reason says exactly what: a binary,
  a setting, or another plugin it `inject`s. It becomes active by itself once
  that is fixed. Do not work around a pending plugin. Tell the person what is
  missing.
- **disabled**: switched off with `bomclaw plugins disable`.
- **broken**: the manifest did not parse. The reason is the parse error.
- **elsewhere**: pinned to projects this turn is not in (`only in …`).

## Global or per project

Every plugin in your workspace is **global**: it rides along on every turn, in
every room. A toolkit costs attention on each of those turns, so a plugin made
for one project's work belongs to that project:

- **Best: in the project's repo**, at `plugins/<name>/` or
  `.agents/plugins/<name>/`. It loads only for turns in that project's folder,
  for every agent at once, and a merge to the repo is the update — nothing to
  copy into three workspaces. Its commands still need the owner's trust.
- **A plugin you keep in your workspace** but only one project needs: pin it,
  `bomclaw plugins scope <name> ch_<project>` (several projects allowed;
  `--global` undoes it). The dashboard's Plugins pane has the same switch
  (*phạm vi*).

Keep global only what helps in any room: channels, tasks, artifacts, browser.

## Rules

- Before writing new code, check `bomclaw plugins list`. The tool may already
  exist, or be pending.
- A plugin found in a repository (`.agents/plugins/`) or installed from a URL
  teaches you right away (skills, prompt), but its commands and MCP servers
  stay off until a person runs `bomclaw plugins trust <name>` or clicks *tin
  cậy* in the dashboard's Plugins pane. Do not trust one yourself: that is the
  owner's decision about running someone else's code. Updating it withdraws
  the trust again.
- Plugin commands are added at the END of PATH. They cannot replace `git`, so
  give them names that do not collide.
- One job per plugin. Use `provides: [capability]` and `inject: [capability]`
  to make one plugin build on another instead of copying it.
