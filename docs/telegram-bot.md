---
title: Telegram Bot
layout: default
---

<section class="doc-page">
<div class="doc-content" markdown="1">

# Telegram Bot

<p class="lead">Control your AI agent from anywhere using Telegram.</p>

## Setup

### 1. Create a bot with BotFather

1. Open Telegram and search for [@BotFather](https://t.me/BotFather)
2. Send `/newbot` and follow the prompts
3. Copy the bot token (looks like `123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11`)

### 2. Configure NanoClaw

Add your token to `.env`:

```bash
TELEGRAM_TOKEN=123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11
```

### 3. Start the gateway

```bash
./nanoclaw gateway
```

The bot starts polling Telegram automatically when a token is configured.

## Commands

| Command | Description |
|---|---|
| `/start` | Show welcome message and help |
| `/models` | List available models with pricing |
| `/model <name>` | Switch model for this session |
| `/model default` | Reset to the default model |
| `/status` | Show session info, token usage, queue depth |
| `/reset` | Clear conversation history |
| `/cancel` | Cancel the current in-flight request |

### Model Switching

Switch models per-session without restarting the gateway:

```
/model opus     → Switch to Claude Opus 4.6
/model sonnet   → Switch to Claude Sonnet 4.6
/model haiku    → Switch to Claude Haiku 4.5
/model default  → Reset to configured default
```

Aliases are supported: `opus`, `o4`, `sonnet`, `s4`, `haiku`, `h4`.

### Status Information

The `/status` command shows:

- Current session ID
- Active model
- Messages in conversation
- Total tokens used (input + output)
- Queue depth (pending messages)
- Session age

## How It Works

1. The bot uses **long-polling** to receive updates from Telegram
2. Each Telegram chat ID gets its own session with separate history
3. Messages are routed through the gateway's agent loop
4. Responses **stream back in real-time** via Telegram message edits
5. Tool calls execute on your machine and results feed back to Claude
6. All events are recorded in per-session JSONL transcripts

### Streaming Updates

NanoClaw streams Claude's responses to Telegram by editing the message as new tokens arrive. This means you see the response being generated in real-time, just like in the Claude web interface.

### Message Queue

If you send a message while Claude is still processing a previous one, it enters a FIFO queue. Messages are processed in order after the current request completes.

## Security

### Restricting Access

By default, anyone who finds your bot can use it. To restrict access to specific Telegram users:

```yaml
# config.yaml
security:
  allowed_user_ids:
    - 123456789    # Your Telegram user ID
    - 987654321    # Another authorized user
```

To find your Telegram user ID, send a message to [@userinfobot](https://t.me/userinfobot).

> **Important:** NanoClaw gives Claude full access to your machine (shell, files, browser). Always restrict bot access to trusted users.

### What the Bot Can Do

When someone sends a message to your bot, Claude can:

- Run any shell command on your machine
- Read, write, and delete files
- Take screenshots
- Control your browser
- Manage processes

This is powerful but carries security implications. Keep your bot token secret and whitelist authorized users.

## Tips

- **Long tasks:** Claude can chain up to 50 tool calls per message, so complex tasks work well
- **Cancel:** Use `/cancel` if a task is taking too long or going in the wrong direction
- **Reset:** Use `/reset` to start a fresh conversation if context gets cluttered
- **Model switching:** Use `/model haiku` for quick, cheap queries and `/model opus` for complex tasks

<div class="doc-nav">
  <a href="{{ '/features' | relative_url }}">
    <div class="label">Previous</div>
    <div class="title">Features & Tools</div>
  </a>
  <a href="{{ '/dashboard' | relative_url }}" class="next">
    <div class="label">Next</div>
    <div class="title">Dashboard</div>
  </a>
</div>

</div>
</section>
