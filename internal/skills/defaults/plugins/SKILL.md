---
name: plugins
description: 'Extend your own toolkit: commands, MCP tool servers, settings. Use when: a tool would beat repeating a job, someone hands you an API or token, or a plugin shows "Not loaded". NOT for knowledge without code (write a skill).'
---

# plugins

Everything you can do beyond the CLI's own tools is a plugin: a folder under
`plugins/` in your workspace with a `plugin.yaml`. A bare skill folder counts
as one too. Plugins are re-read before every turn, so anything you add,
configure or switch off takes effect on your next turn. Nothing restarts.

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

## Status

- **active**: in use.
- **pending**: something is missing. The reason says exactly what: a binary,
  a setting, or another plugin it `inject`s. It becomes active by itself once
  that is fixed. Do not work around a pending plugin. Tell the person what is
  missing.
- **disabled**: switched off with `bomclaw plugins disable`.
- **broken**: the manifest did not parse. The reason is the parse error.

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
