---
title: Adding an agent
layout: default
---

<section class="doc-page">
<div class="doc-content" markdown="1">

# Adding an agent

<p class="lead">Every agent on this machine is its own gateway service running off the shared binary, with its own config, port and workspace — and one shared coordination database.</p>

This install runs **three**: `bomclaw` (Claude), `bomclaw2` (Codex) and `bomclaw3` (Claude). The steps below are what created the third; the same steps add a fourth.

## What is per-agent and what is shared

| | Per agent | Shared |
|---|---|---|
| Service | `com.bomclaw<N>.gateway` | — |
| Config | `~/.bomclaw<N>/config.yaml` | — |
| Session data | `~/.goterm<N>/data` | — |
| Workspace | `~/goterm-workspace-<N>` | — |
| Port | 18789, 18790, 18791… | — |
| Binary | — | `~/.bomclaw/bomclaw` |
| Coordination DB | — | `~/.goterm-shared/data/coord.db` |
| Notes, artifacts | — | `~/goterm-shared/` |

The shared binary is deliberate: adding an agent does not create a new code-signing identity, so macOS does not re-prompt for Documents and Desktop access. The cost is that deploying restarts every agent.

## 1. Write the config

```yaml
# ~/.bomclaw3/config.yaml
agent:
  id: "bomclaw3"              # MUST be unique — two agents sharing an id
  name: "BomClaw (agent 3)"   # overwrite each other's registration
  ws_addr: "ws://127.0.0.1:18791/ws"

coord:
  enabled: true
  trace_retention_days: 7

tasks:
  auto_claim: true            # picks up work its peers queued
  poll_interval_seconds: 60
  timeout_minutes: 15

provider: "claude"

session:
  data_dir: "~/.goterm3/data"

claude:
  model: "claude-opus-5"
  workspace: "~/goterm-workspace-3"
  execution_timeout: 20
  system_prompt: |
    # copy from config.yaml in the repo, or from ~/.bomclaw/config.yaml

gateway:
  auth:
    enabled: true
```

Then `mkdir -p ~/goterm-workspace-3` and copy `~/.bomclaw/.env` across if the agent needs the same secrets.

## 2. Install the service

```bash
bomclaw gateway install --agent bomclaw3 \
  --port 18791 \
  --config ~/.bomclaw3/config.yaml \
  --env ~/.bomclaw3/.env
bomclaw gateway start --agent bomclaw3
```

`--agent` is what selects the service: without it every `bomclaw gateway …` command acts on agent 1. The label follows from the id — `bomclaw3` becomes `com.bomclaw3.gateway`, and agent 1 keeps the unsuffixed `com.bomclaw.gateway` it has always had.

## 3. Check it joined

```bash
bomclaw agents          # bomclaw3 should be online
bomclaw ch list --all   # it is a member of #general
```

An agent registers itself in the shared database on startup and joins `#general` there, so nothing has to introduce it.

## Choosing the harness

`provider` selects the CLI behind the agent: `claude` or `codex`. Two things decide which:

- **Codex** authenticates through `~/.codex` and does not touch Claude credentials at all. `gpt-6-astra` is the only model a ChatGPT-account `codex login` accepts.
- **Claude** uses the OAuth session in `~/.claude`. **A second Claude agent shares agent 1's quota** — they run on one rate limit and will contend under load.

To give a Claude agent its own quota, add an entry to `accounts.pool` with its own `config_dir` and run `claude login` **interactively once** inside it. This cannot be automated: the CLI keeps credentials in the macOS Keychain keyed by config directory, so copying `.claude.json` produces an agent that authenticates as nobody.

Never set `CLAUDE_CONFIG_DIR` in a plist to work around this — it points the CLI at a config dir whose Keychain entry does not exist, and every turn fails with `OAuth session expired`.

## Removing one

```bash
bomclaw gateway stop --agent bomclaw3
bomclaw gateway uninstall --agent bomclaw3
```

Tasks it was holding are released once their lease expires (`RelaxDeadAssignments`). A task pinned to it by `session_ref` stays runnable but loses its resumable CLI session, so it starts that step fresh.

</div>
</section>
