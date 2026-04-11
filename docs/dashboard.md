---
title: Web Dashboard
layout: default
---

<section class="doc-page">
<div class="doc-content" markdown="1">

# Web Dashboard

<p class="lead">A real-time React web interface for managing sessions and chatting with your agent.</p>

## Overview

The BomClaw dashboard is a React SPA built with Vite and TailwindCSS. It connects to the gateway via WebSocket and provides:

- **Session management** &mdash; Create, view, reset, and delete sessions
- **Real-time chat** &mdash; Stream responses as Claude generates them
- **Tool visibility** &mdash; See which tools Claude is using in real-time
- **Status monitoring** &mdash; Gateway health, uptime, and active sessions

## Accessing the Dashboard

The dashboard is automatically served by the gateway. After starting:

```bash
./bomclaw gateway --port 18789
```

Open your browser to:

```
http://localhost:18789
```

## Building the Dashboard

If you need to rebuild the dashboard from source:

```bash
cd dashboard
npm install
npm run build
```

The build output goes to `dashboard/dist/`, which the gateway serves automatically.

For development with hot reload:

```bash
cd dashboard
npm run dev
```

## Pages

### Sessions

The sessions page lists all active and past sessions with:

- Session ID
- Creation time
- Last update time
- Message count

Click a session to open its chat view, or create a new session.

### Chat

The chat view provides a full conversation interface:

- **Message history** &mdash; All previous messages in the session
- **Streaming responses** &mdash; Real-time token-by-token updates
- **Tool badges** &mdash; Compact badges showing which tools were used (grouped, max 5 shown)
- **Model selector** &mdash; Dropdown to switch models per-session
- **Message input** &mdash; Send new messages to Claude

### Status

The status page shows:

- Gateway health and uptime
- Number of active sessions
- Connection status

## URL Routing

The dashboard uses client-side routing:

| Path | Page |
|---|---|
| `/` | Sessions list |
| `/chat/{session_id}` | Chat view for a specific session |
| `/status` | Status and health |

The gateway handles SPA fallback &mdash; all unmatched routes return `index.html`.

## Tech Stack

| Technology | Version | Purpose |
|---|---|---|
| React | 19 | UI framework |
| Vite | 6 | Build tool and dev server |
| TailwindCSS | 4 | Utility-first styling |
| Zustand | Latest | State management |
| React Markdown | Latest | Message rendering |
| TypeScript | Latest | Type safety |

## WebSocket Protocol

The dashboard communicates with the gateway using JSON-RPC over WebSocket:

```json
// Request
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "send",
  "params": {
    "session_id": "abc123",
    "message": "Hello, Claude!"
  }
}

// Streaming response events
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "stream.text",
  "params": { "text": "Hello! " }
}

// Final response
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": { "status": "complete" }
}
```

<div class="doc-nav">
  <a href="{{ '/telegram-bot' | relative_url }}">
    <div class="label">Previous</div>
    <div class="title">Telegram Bot</div>
  </a>
  <a href="{{ '/configuration' | relative_url }}" class="next">
    <div class="label">Next</div>
    <div class="title">Configuration</div>
  </a>
</div>

</div>
</section>
