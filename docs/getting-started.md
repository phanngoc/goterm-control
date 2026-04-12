---
title: Getting Started
layout: default
---

<section class="doc-page">
<div class="doc-content" markdown="1">

# Getting Started

<p class="lead">Get BomClaw running on your machine in 5 minutes. No coding required.</p>

## Install (Pre-built Binary)

Choose your platform:

<div class="platform-tabs" style="margin: 1.5rem 0;">
<details open>
<summary><strong>🍎 macOS &mdash; Apple Silicon (M1/M2/M3/M4)</strong></summary>

<br/>

**Step 1 &mdash; Download**

Open **Terminal** (press `Cmd + Space`, type `Terminal`, press Enter):

```bash
curl -L https://github.com/phanngoc/goterm-control/releases/download/v0.1.0/bomclaw-v0.1.0-darwin-arm64.tar.gz | tar xz
```

**Step 2 &mdash; Allow it to run**

macOS blocks apps from unknown developers. Remove the block:

```bash
xattr -d com.apple.quarantine bomclaw-darwin-arm64
chmod +x bomclaw-darwin-arm64
```

**Step 3 &mdash; Install Claude CLI**

```bash
npm install -g @anthropic-ai/claude-code
claude login
```

> Don't have `npm`? Install Node.js first: [https://nodejs.org](https://nodejs.org) (download the LTS version, double-click the installer).

**Step 4 &mdash; Set up Telegram (optional)**

```bash
echo 'TELEGRAM_TOKEN=your-token-here' > .env
```

> Get a token: open Telegram, search **@BotFather**, send `/newbot`, follow the instructions.

**Step 5 &mdash; Run**

```bash
./bomclaw-darwin-arm64 chat --env .env
# Or: ./bomclaw-darwin-arm64 gateway --env .env
```

**Optional &mdash; Install permanently**

```bash
sudo mv bomclaw-darwin-arm64 /usr/local/bin/bomclaw
bomclaw chat
```

</details>

<details>
<summary><strong>🍎 macOS &mdash; Intel</strong></summary>

<br/>

**Step 1 &mdash; Download**

Open **Terminal** (press `Cmd + Space`, type `Terminal`, press Enter):

```bash
curl -L https://github.com/phanngoc/goterm-control/releases/download/v0.1.0/bomclaw-v0.1.0-darwin-amd64.tar.gz | tar xz
```

**Step 2 &mdash; Allow it to run**

```bash
xattr -d com.apple.quarantine bomclaw-darwin-amd64
chmod +x bomclaw-darwin-amd64
```

**Step 3 &mdash; Install Claude CLI**

```bash
npm install -g @anthropic-ai/claude-code
claude login
```

> Don't have `npm`? Install Node.js first: [https://nodejs.org](https://nodejs.org) (download the LTS version, double-click the installer).

**Step 4 &mdash; Set up Telegram (optional)**

```bash
echo 'TELEGRAM_TOKEN=your-token-here' > .env
```

**Step 5 &mdash; Run**

```bash
./bomclaw-darwin-amd64 chat --env .env
# Or: ./bomclaw-darwin-amd64 gateway --env .env
```

**Optional &mdash; Install permanently**

```bash
sudo mv bomclaw-darwin-amd64 /usr/local/bin/bomclaw
bomclaw chat
```

</details>

<details>
<summary><strong>🐧 Ubuntu / Linux</strong></summary>

<br/>

**Step 1 &mdash; Download**

Open a terminal (`Ctrl + Alt + T`):

```bash
curl -L https://github.com/phanngoc/goterm-control/releases/download/v0.1.0/bomclaw-v0.1.0-linux-amd64.tar.gz | tar xz
chmod +x bomclaw-linux-amd64
```

**Step 2 &mdash; Install Claude CLI**

```bash
sudo apt update && sudo apt install -y nodejs npm
sudo npm install -g @anthropic-ai/claude-code
claude login
```

**Step 3 &mdash; Set up Telegram (optional)**

```bash
echo 'TELEGRAM_TOKEN=your-token-here' > .env
```

> Get a token: open Telegram, search **@BotFather**, send `/newbot`, follow the instructions.

**Step 4 &mdash; Run**

```bash
./bomclaw-linux-amd64 chat --env .env
# Or: ./bomclaw-linux-amd64 gateway --env .env
```

**Optional &mdash; Install permanently**

```bash
sudo mv bomclaw-linux-amd64 /usr/local/bin/bomclaw
bomclaw chat
```

**Optional &mdash; Run as a background service**

```bash
bomclaw gateway install --config /path/to/config.yaml --env /path/to/.env
bomclaw gateway status
bomclaw gateway restart
bomclaw gateway stop
```

</details>

<details>
<summary><strong>🪟 Windows (via WSL)</strong></summary>

<br/>

**Step 1 &mdash; Install WSL**

Open **PowerShell as Administrator** and run:

```powershell
wsl --install
```

Restart your computer, then open **Ubuntu** from the Start menu.

**Step 2 &mdash; Download (inside WSL)**

```bash
curl -L https://github.com/phanngoc/goterm-control/releases/download/v0.1.0/bomclaw-v0.1.0-linux-amd64.tar.gz | tar xz
chmod +x bomclaw-linux-amd64
```

**Step 3 &mdash; Install Claude CLI**

```bash
sudo apt update && sudo apt install -y nodejs npm
sudo npm install -g @anthropic-ai/claude-code
claude login
```

**Step 4 &mdash; Set up Telegram (optional)**

```bash
echo 'TELEGRAM_TOKEN=your-token-here' > .env
```

**Step 5 &mdash; Run**

```bash
./bomclaw-linux-amd64 chat --env .env
# Or: ./bomclaw-linux-amd64 gateway --env .env
```

</details>
</div>

---

## Build from Source (Developers)

If you prefer to build from source or want to modify the code:

### Prerequisites

- **Go 1.22+** &mdash; [Download Go](https://go.dev/dl/)
- **Claude CLI** &mdash; installed and logged in ([docs](https://docs.anthropic.com/en/docs/claude-code))
- **Telegram bot token** (optional) &mdash; from [@BotFather](https://t.me/BotFather)
- **Chrome/Chromium** (optional) &mdash; for browser automation tools

### 1. Clone the repository

```bash
git clone https://github.com/phanngoc/goterm-control.git
cd goterm-control
```

### 2. Authenticate with Claude

```bash
claude login
```

### 3. Set up Telegram (optional)

```bash
cp .env.example .env
# Edit .env and add: TELEGRAM_TOKEN=your-token-here
```

### 4. Build and run

```bash
go build -o bomclaw ./cmd/bomclaw/
./bomclaw chat
# Or: ./bomclaw gateway
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
