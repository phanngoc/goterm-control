---
title: Getting Started
layout: default
---

<section class="doc-page">
<div class="doc-content" markdown="1">

# Getting Started

<p class="lead">Get BomClaw running on your machine in under a minute.</p>

## Prerequisites

- **Go 1.22+** &mdash; [Download Go](https://go.dev/dl/)
- **Claude CLI** &mdash; installed and logged in ([docs](https://docs.anthropic.com/en/docs/claude-code))
- **Telegram bot token** (optional) &mdash; from [@BotFather](https://t.me/BotFather)
- **Chrome/Chromium** (optional) &mdash; for browser automation tools

## Quick Start

### 1. Clone the repository

```bash
git clone https://github.com/phanngoc/goterm-control.git
cd goterm-control
```

### 2. Authenticate with Claude

```bash
claude login
```

This uses your Claude Pro/Max subscription via OAuth2 &mdash; no API key needed.

### 3. Set up Telegram (optional)

If you want Telegram access, add your bot token to `.env`:

```bash
# In .env
TELEGRAM_TOKEN=123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11
```

### 4. Build

```bash
go build -o bomclaw ./cmd/bomclaw/
```

### 5. Run

**Interactive CLI chat** (simplest way to start):

```bash
./bomclaw chat
```

**Full gateway** (Telegram bot + Web Dashboard + WebSocket RPC):

```bash
./bomclaw gateway
```

That's it! You now have a personal AI agent with full computer control.

## Commands

| Command | Description |
|---|---|
| `bomclaw gateway` | Start gateway (Telegram bot + WebSocket RPC + Dashboard) |
| `bomclaw chat` | Interactive CLI chat (direct, no gateway needed) |
| `bomclaw send "<msg>"` | Send a message to the running gateway |
| `bomclaw status` | Show gateway health and session info |
| `bomclaw models` | List available models |

### Examples

```bash
# Chat with a specific model
./bomclaw chat --model opus

# Start gateway on a custom port
./bomclaw gateway --port 9000 --bind 0.0.0.0

# Send a message to the running gateway
./bomclaw send "list all running docker containers"

# Quick task with a fast model
./bomclaw send --model haiku "what time is it"

# Check gateway health
./bomclaw status
```

## Authentication Modes

BomClaw auto-detects your authentication method at startup:

| Mode | Token prefix | How it works |
|---|---|---|
| **Claude CLI** (recommended) | `sk-ant-oat...` | Uses `claude` subprocess with your Pro/Max subscription. No per-token cost. |
| **Direct API** | `sk-ant-api03...` | Calls Anthropic Messages API directly. Pay-per-use. |

> **Tip:** Claude CLI OAuth is recommended for most users. It uses your existing subscription, so there's no additional cost per token.

## Environment Variables

| Variable | Required | Description |
|---|---|---|
| `ANTHROPIC_API_KEY` | No | Anthropic API key (only if not using Claude CLI OAuth) |
| `TELEGRAM_TOKEN` | For Telegram | Telegram bot token from @BotFather |

## Supported Platforms

BomClaw runs anywhere Go compiles:

- **Linux** &mdash; Full support (x86_64, ARM64)
- **macOS** &mdash; Full support (Intel, Apple Silicon) + AppleScript tools
- **Windows** &mdash; Via WSL (Windows Subsystem for Linux)

## Next Steps

- [Architecture]({{ '/architecture' | relative_url }}) &mdash; Understand how BomClaw works
- [Features & Tools]({{ '/features' | relative_url }}) &mdash; See all 25 tools
- [Telegram Bot]({{ '/telegram-bot' | relative_url }}) &mdash; Set up your Telegram bot
- [Web Dashboard]({{ '/dashboard' | relative_url }}) &mdash; Use the React web UI
- [Configuration]({{ '/configuration' | relative_url }}) &mdash; Customize your setup

<div class="doc-nav">
  <a href="{{ '/' | relative_url }}">
    <div class="label">Previous</div>
    <div class="title">Home</div>
  </a>
  <a href="{{ '/architecture' | relative_url }}" class="next">
    <div class="label">Next</div>
    <div class="title">Architecture</div>
  </a>
</div>

</div>
</section>
