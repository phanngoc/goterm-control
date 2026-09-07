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

</div>

<div class="doc-content">

<details open>
<summary><strong>🍎 macOS — Apple Silicon (M1/M2/M3/M4)</strong></summary>
<div markdown="1">

**Step 1 — Download**

Open **Terminal** (press `Cmd + Space`, type `Terminal`, press Enter):

```bash
curl -L https://github.com/phanngoc/goterm-control/releases/download/v0.1.0/bomclaw-v0.1.0-darwin-arm64.tar.gz | tar xz
```

**Step 2 — Allow it to run**

macOS blocks apps from unknown developers. Remove the block:

```bash
xattr -d com.apple.quarantine bomclaw-darwin-arm64
chmod +x bomclaw-darwin-arm64
```

**Step 3 — Install Claude CLI**

```bash
npm install -g @anthropic-ai/claude-code
claude login
```

> Don't have `npm`? Install Node.js first: [https://nodejs.org](https://nodejs.org) (download the LTS version, double-click the installer).

**Step 4 — Set up Telegram (optional)**

```bash
echo 'TELEGRAM_TOKEN=your-token-here' > .env
```

> Get a token: open Telegram, search **@BotFather**, send `/newbot`, follow the instructions.

**Step 5 — Run**

```bash
./bomclaw-darwin-arm64 chat --env .env
# Or: ./bomclaw-darwin-arm64 gateway --env .env
```

**Optional — Install permanently**

```bash
sudo mv bomclaw-darwin-arm64 /usr/local/bin/bomclaw
bomclaw chat
```

</div>
</details>

<details>
<summary><strong>🍎 macOS — Intel</strong></summary>
<div markdown="1">

**Step 1 — Download**

Open **Terminal** (press `Cmd + Space`, type `Terminal`, press Enter):

```bash
curl -L https://github.com/phanngoc/goterm-control/releases/download/v0.1.0/bomclaw-v0.1.0-darwin-amd64.tar.gz | tar xz
```

**Step 2 — Allow it to run**

```bash
xattr -d com.apple.quarantine bomclaw-darwin-amd64
chmod +x bomclaw-darwin-amd64
```

**Step 3 — Install Claude CLI**

```bash
npm install -g @anthropic-ai/claude-code
claude login
```

> Don't have `npm`? Install Node.js first: [https://nodejs.org](https://nodejs.org) (download the LTS version, double-click the installer).

**Step 4 — Set up Telegram (optional)**

```bash
echo 'TELEGRAM_TOKEN=your-token-here' > .env
```

**Step 5 — Run**

```bash
./bomclaw-darwin-amd64 chat --env .env
# Or: ./bomclaw-darwin-amd64 gateway --env .env
```

**Optional — Install permanently**

```bash
sudo mv bomclaw-darwin-amd64 /usr/local/bin/bomclaw
bomclaw chat
```

</div>
</details>

<details>
<summary><strong>🐧 Ubuntu / Linux</strong></summary>
<div markdown="1">

**Step 1 — Download**

Open a terminal (`Ctrl + Alt + T`):

```bash
curl -L https://github.com/phanngoc/goterm-control/releases/download/v0.1.0/bomclaw-v0.1.0-linux-amd64.tar.gz | tar xz
chmod +x bomclaw-linux-amd64
```

**Step 2 — Install Claude CLI**

```bash
sudo apt update && sudo apt install -y nodejs npm
sudo npm install -g @anthropic-ai/claude-code
claude login
```

**Step 3 — Set up Telegram (optional)**

```bash
echo 'TELEGRAM_TOKEN=your-token-here' > .env
```

> Get a token: open Telegram, search **@BotFather**, send `/newbot`, follow the instructions.

**Step 4 — Run**

```bash
./bomclaw-linux-amd64 chat --env .env
# Or: ./bomclaw-linux-amd64 gateway --env .env
```

**Optional — Install permanently**

```bash
sudo mv bomclaw-linux-amd64 /usr/local/bin/bomclaw
bomclaw chat
```

**Optional — Run as a background service**

```bash
bomclaw gateway install --config /path/to/config.yaml --env /path/to/.env
bomclaw gateway status
bomclaw gateway restart
bomclaw gateway stop
```

</div>
</details>

<details>
<summary><strong>🪟 Windows 11 (native)</strong></summary>
<div markdown="1">

No WSL needed. Open **PowerShell** from the Start menu — none of this needs
Administrator.

**Step 1 — Download**

```powershell
Invoke-WebRequest -Uri https://github.com/phanngoc/goterm-control/releases/download/v0.1.0/bomclaw-v0.1.0-windows-amd64.zip -OutFile bomclaw.zip
Expand-Archive .\bomclaw.zip -DestinationPath .
```

If SmartScreen flags the download, right-click `bomclaw.exe` →
**Properties** → tick **Unblock** → **OK**.

**Step 2 — Install Claude CLI**

```powershell
winget install OpenJS.NodeJS       # skip if you already have Node
npm install -g @anthropic-ai/claude-code
claude login
```

**Step 3 — Set up Telegram (optional)**

```powershell
'TELEGRAM_TOKEN=your-token-here' | Out-File -Encoding utf8 .env
```

**Step 4 — Run**

```powershell
.\bomclaw.exe chat --env .env
# Or: .\bomclaw.exe gateway --env .env
```

**Step 5 — Run in the background (optional)**

```powershell
.\bomclaw.exe gateway install --config .\config.yaml --env .\.env
```

This registers a Scheduled Task (`\BomClaw\bomclaw-gateway`) that starts at
logon and restarts on failure. It runs as you, in your own desktop session,
which is what lets screenshots, the clipboard and browser control work.

Task Scheduler stores no environment block, so keep `TELEGRAM_TOKEN` and any
API key in the `--env` file (or persist them with `setx`) — a value exported
only in the shell you ran `install` from will not reach the service. Logs land
in `%USERPROFILE%\.goterm\logs\gateway.log` and `gateway.err.log`.

</div>
</details>

</div>

<div class="doc-content" markdown="1">

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

BomClaw builds natively for all three:

- **Linux** &mdash; x86_64, ARM64. Background service via `systemd --user`.
- **macOS** &mdash; Intel, Apple Silicon. Background service via LaunchAgent.
  Adds AppleScript, plus the `bomtray` menu-bar companion.
- **Windows 11** &mdash; x86_64, ARM64, no WSL. Background service via a
  Scheduled Task. `run_shell` runs PowerShell instead of bash; AppleScript has
  no counterpart and is not offered.

Screenshots, clipboard and "open app / URL" work on macOS and Windows. They are
not implemented on Linux, where the answer depends on X11 vs Wayland and on
which helpers are installed; the tools report that rather than failing obscurely.

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
