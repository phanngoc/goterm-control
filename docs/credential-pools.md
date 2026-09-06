---
title: Credential Pools
layout: default
---

<section class="doc-page">
<div class="doc-content" markdown="1">

# Credential Pools

<p class="lead">Rotate an agent's sessions across several logins — separate Claude subscriptions, API keys, or Codex accounts — so one account's rate limit does not stop the agent.</p>

Off by default. With no `accounts:` section the agent runs on the ambient
login, exactly as it always has.

## The one rule that shapes everything

**Both CLIs keep their session store inside the same directory as their
credentials.**

| CLI | Directory | Holds |
|---|---|---|
| claude | `CLAUDE_CONFIG_DIR` | the login **and** `sessions/`, `projects/` |
| codex | `CODEX_HOME` | the login **and** the thread database |

So a conversation cannot be read from any other account. Rotation is therefore
**per session, never per turn**: an account is chosen when a session starts and
pinned to it for life. Every later turn of that conversation returns to the
same account.

A consequence worth stating plainly: when a session's account is rate-limited,
that session waits. It cannot be moved — its history is not there. New sessions
go elsewhere; this one reports the error.

## Setting it up

Each account needs its **own directory, logged in separately**. Two accounts
pointing at one directory are one identity, and rotating between them achieves
nothing.

```bash
# a second Claude subscription
CLAUDE_CONFIG_DIR=~/.claude-work claude     # then /login

# a second Codex account
CODEX_HOME=~/.codex-work codex login
```

Then declare them:

```yaml
accounts:
  cooldown_minutes: 15
  pool:
    - name: "personal"
      config_dir: "~/.claude"
    - name: "work"
      config_dir: "~/.claude-work"
```

Claude can also run on plain API keys instead of the OAuth subscription.
`api_key_env` names an environment variable, keeping the secret out of
`config.yaml`:

```yaml
    - name: "key-a"
      api_key_env: "ANTHROPIC_KEY_A"
```

An agent only uses pool entries matching its own `provider`, so one config file
can describe both backends:

```yaml
    - name: "codex-main"
      provider: "codex"
      config_dir: "~/.codex"
```

## How an account is chosen

- **New session** → the least recently used account that is not cooling down,
  so load spreads instead of piling onto the first entry.
- **Existing session** → always its pinned account, cooldown or not.
- **A rate-limit error** → that account cools down for `cooldown_minutes`, so
  new sessions avoid it. A later success lifts the cooldown early.
- **Every account cooling down** → the agent still tries the one that recovers
  soonest. A cooldown is a guess about someone else's rate limiter, not a fact,
  and idling on a guess is worse than trying.

Only account-specific signals count as rate limits (`429`, `rate limit`,
`quota`, `usage limit`). A server-side `overloaded` error is the provider's own
capacity problem and does not sideline a working account. Cancelling a turn is
not held against the account either.

## Seeing what is going on

```bash
bomclaw accounts
```

```
BomClaw (agent 1) · provider claude

  ACCOUNT   SESSIONS  LAST USED  STATE
  personal  7         14:22:01   ready
  work      3         14:19:44   cooling 12m0s

  work: 429 rate limit exceeded
```

The same data is in `/api/status` under `accounts`, so the dashboard and the
menu bar can show it too.

## Renaming and removing accounts

A session pins the account **by name**. Renaming an entry strands every session
pinned to the old name: the next turn fails with a clear error rather than
silently starting a fresh conversation under a different login. Removing an
account has the same effect.

If you need to retire an account, let its sessions finish (or `/new` them)
before taking it out of the pool.

## Before you pool many accounts

Spreading your **own** accounts to manage rate limits is ordinary capacity
work. Collecting accounts specifically to exceed the limits a provider sets per
account is a different thing, and both Anthropic's and OpenAI's terms speak to
it. This feature does not know the difference — it will rotate whatever you
give it. Check your plan's terms before pooling accounts you do not own, or
many accounts you created for the purpose.

</div>
</section>
