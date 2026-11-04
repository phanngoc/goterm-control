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

## Setup-tokens: rotating per turn

A login (`config_dir`) carries its own session store, so it can only rotate
**per session**. A **setup-token** is different: it is authentication alone.
Every token account that shares one session store can answer any conversation
in it. So tokens can take turns **within** a conversation.

```bash
claude setup-token          # once per subscription; prints a one-year token
```

Keep each token in the agent's `.env`, not in `config.yaml`:

```bash
CLAUDE_TOKEN_A=sk-ant-oat01-…
CLAUDE_TOKEN_B=sk-ant-oat01-…
CLAUDE_TOKEN_C=sk-ant-oat01-…
```

```yaml
accounts:
  rotate_every: 5        # each token serves 5 turns, then the next one does
  pool:
    - name: "tok-a"
      oauth_token_env: "CLAUDE_TOKEN_A"
    - name: "tok-b"
      oauth_token_env: "CLAUDE_TOKEN_B"
    - name: "tok-c"
      oauth_token_env: "CLAUDE_TOKEN_C"
```

How it behaves:

- **The count is per agent.** Every turn of every conversation advances it:
  turns 1–5 go to `tok-a`, 6–10 to `tok-b`, 11–15 to `tok-c`, and then back
  to `tok-a`.
- **A capped or refused token is skipped.** Messages like `You've hit your
  session limit`, a 429, or `OAuth token revoked/expired` take the token out of
  the rotation, for `cooldown_minutes` or 6 hours respectively.
- **The turn that hit the cap moves to the next token.** It is retried there
  at once, provided nothing had been shown to the person yet. The conversation
  continues on the new token because the session store is shared. Once output
  has streamed, the error stands rather than a second answer appearing under
  the first.
- **A token whose variable is empty is skipped** and reported by
  `bomclaw accounts`.
- **What doesn't move.** Logins and `rotate: false` accounts keep per-session
  pinning. Tokens only rotate among accounts with the same `config_dir`
  (usually none, which means the default store).
- **The cost: prompt cache.** The API cache is kept per organization, so the
  first turn on a new subscription re-reads the whole conversation without a
  cache hit. Rotating every 5 turns means one uncached turn in five. A larger
  `rotate_every` trades evenness for cache.
- **Precedence.** An `ANTHROPIC_API_KEY` or `ANTHROPIC_AUTH_TOKEN` in the
  environment outranks a setup-token in the CLI, so both are removed for token
  turns. A config-dir login likewise never sees an ambient
  `CLAUDE_CODE_OAUTH_TOKEN`.

`bomclaw accounts` marks the current token and how many turns it has used:

```
  ACCOUNT  KIND   SESSIONS  TURNS  LAST USED  STATE
  tok-a    token  3         10     14:22:01   ready
  tok-b    token  0         7      14:31:40   current (2 used) · ready
  tok-c    token  0         5      14:19:44   cooling 12m0s
```

## Managing tokens from the dashboard

**Admin → Accounts** keeps the credentials agents rotate through, per
provider, without editing `config.yaml`:

| Provider | Kinds | Reaches the CLI as |
|---|---|---|
| Claude Code | setup-token, API key | `CLAUDE_CODE_OAUTH_TOKEN`, `ANTHROPIC_API_KEY` |
| Codex | access token, API key | `CODEX_ACCESS_TOKEN`, `CODEX_API_KEY` |

- **Storage.** Tokens live in `tokens.yaml`, next to the coordination
  database (`~/.goterm-shared/data/tokens.yaml` by default), with mode 0600.
  The page never gets a value back. It shows a mask such as
  `sk-ant-oat01-…abcd`, and an edit with an empty value field keeps the
  current one.
- **Sharing.** Every agent running that provider uses the provider's tokens.
- **Taking effect.** A gateway re-reads the file before its next turn when it
  has changed, so adding, editing or deleting a token needs no restart.
- **Rotation length.** "Đổi token sau N lượt" is the provider's
  `rotate_every`. It overrides the config value; 0 defers to the config.
- **Opting out.** A token outside the rotation ("ngoài vòng xoay") is the
  equivalent of `rotate: false`.
- **Live status.** Each token shows how every agent is using it right now:
  current or not, turns served, cooldown, and the last error. The page reads
  this from each agent's `/api/status`.
- **Config accounts.** Accounts from `config.yaml` are listed read-only below
  the tokens. A stored token whose name matches a config account is ignored
  for that agent.

**Which accounts rotate together.** Every account that shares a session store
can answer any conversation in it. That covers tokens, API keys, and the
default login, meaning the subscription you logged into with plain `claude`
(or `codex`). With a rotation set, all of these take turns. A login with a
`config_dir` of its own keeps its own conversations.

A conversation on the rotation is pinned to the rotation (`@rotation`), not
to the token it started on, so deleting that token does not strand it.

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
