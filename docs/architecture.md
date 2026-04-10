---
title: Architecture
layout: default
---

<section class="doc-page">
<div class="doc-content" markdown="1">

# Architecture

<p class="lead">How NanoClaw is structured &mdash; channels, gateway, agent loop, and tools.</p>

## Overview

NanoClaw follows a simple layered architecture:

1. **Channels** receive user messages (Telegram, Web, CLI)
2. **Gateway** routes messages, manages sessions, resolves models
3. **Agent Loop** streams model responses and executes tool calls
4. **Tools** interact with the operating system and browser
5. **Storage** persists sessions, transcripts, and cross-session memory

```
┌───────────┐  ┌──────────────┐  ┌───────────────┐
│  Telegram  │  │ Web Dashboard│  │   CLI Chat    │
│  Bot       │  │  (React SPA) │  │               │
└─────┬──────┘  └──────┬───────┘  └───────┬───────┘
      │           WebSocket          direct call
      │               │                  │
      ▼               ▼                  ▼
┌─────────────────────────────────────────────────┐
│              Gateway (JSON-RPC)                  │
│         session mgmt · model resolver            │
└──────────────────────┬──────────────────────────┘
                       │
              ┌─────────────────┐
              │   Agent Loop    │  ← up to 50 iterations
              │  stream + tool  │
              │   call cycle    │
              └────────┬────────┘
                       │
          ┌────────────┼────────────┐
          ▼            ▼            ▼
   ┌────────────┐ ┌──────────┐ ┌──────────┐
   │ Claude CLI │ │  Direct  │ │   Tool   │
   │  (OAuth2)  │ │   API    │ │ Executor │
   └────────────┘ └──────────┘ └──────────┘
```

## The Agent Loop

The heart of NanoClaw lives in `internal/agent/loop.go`. This is what makes it an *agent*, not just a chatbot:

```
user message
  → assemble context (under token budget)
  → stream model response
  → if model calls tools:
      → execute tools
      → feed results back to model
      → loop again (up to 50 iterations)
  → if model says "end_turn":
      → return final response
  → if context overflow:
      → compact (summarize old messages)
      → retry
  → if rate limited:
      → exponential backoff → retry
```

The model can chain multiple tool calls in a single turn, read results, and keep working until the task is complete. This allows complex workflows like "find all TODO comments in the codebase, create a summary, and email it to me" to execute autonomously.

## Packages

| Package | Purpose |
|---|---|
| `agent/` | Core agentic loop with retry and tool execution |
| `anthropic/` | Direct Anthropic Messages API client (streaming) |
| `claude/` | Claude CLI subprocess provider (OAuth2 subscription) |
| `browser/` | Chrome DevTools Protocol (CDP) client for browser automation |
| `bot/` | Telegram bot with streaming message edits |
| `channel/` | Channel abstraction (Telegram, CLI) |
| `config/` | YAML config with env var overrides |
| `context/` | Token counting, budget assembly, compaction |
| `execution/` | Per-session FIFO queue (prevents concurrent calls) |
| `gateway/` | WebSocket JSON-RPC server + static file serving (dashboard) |
| `memory/` | Cross-session keyword memory |
| `models/` | Model catalog with aliases and per-session override |
| `session/` | Persistent session management |
| `tools/` | System control tools (shell, files, screenshot, browser, ...) |
| `transcript/` | JSONL event recording per session |

## Project Structure

```
cmd/
  nanoclaw/main.go          CLI entry point (gateway, chat, send, status, models)

dashboard/                  React web dashboard (Vite + TailwindCSS)

internal/
  agent/                    Core agent loop + types
  anthropic/                Direct Anthropic API client
  bot/                      Telegram bot (handler, streamer)
  browser/                  Chrome DevTools Protocol (CDP) client
  channel/                  Channel interface + CLI implementation
  claude/                   Claude CLI subprocess (OAuth2 provider)
  config/                   YAML config loading
  context/                  Context engine (tokens, assembly, compaction)
  execution/                Per-session FIFO execution queue
  gateway/                  WebSocket JSON-RPC server + dashboard hosting
  memory/                   Cross-session keyword memory
  models/                   Model catalog + resolver
  session/                  Persistent session management
  tools/                    System + browser control tools
  transcript/               JSONL event recording
```

## Data Flow

### Message Processing

1. User sends a message via any channel (Telegram, Web, CLI)
2. Gateway creates or retrieves the session
3. Context engine assembles the prompt:
   - System prompt
   - Cross-session memory (keyword-matched, up to 5 entries)
   - Session history (trimmed to fit token budget)
   - New user message
4. Agent loop streams the response from Claude
5. If Claude calls tools, the executor runs them and feeds results back
6. Response streams back to the user in real-time
7. Transcript records all events as JSONL

### Session Lifecycle

- Sessions are created per Telegram chat ID or per WebSocket client
- Each session has its own conversation history and token count
- Sessions persist to disk as JSON metadata + JSONL transcripts
- Idle sessions auto-reset after the configured timeout (default: 30 min)

### Context Budget

NanoClaw uses ~80% of the model's context window for assembled messages. When the budget is exceeded:

1. Old messages are trimmed from the beginning of the conversation
2. If still over budget, messages are compacted (summarized)
3. The model always sees the system prompt + recent context + current message

## Data Persistence

All state lives under `~/.goterm/data/`:

```
~/.goterm/data/
  sessions.json           # Session metadata (atomic writes)
  transcripts/
    chat_<id>.jsonl       # Per-session conversation log (append-only)
  memory/
    memory.jsonl          # Cross-session keyword memory
```

- **sessions.json** &mdash; Atomic writes via temp-file-then-rename to prevent corruption
- **transcripts/** &mdash; Append-only JSONL, one event per line, never rewritten
- **memory/** &mdash; Keyword-indexed entries for cross-session context injection

## Compared to openclaw

| | openclaw | NanoClaw |
|---|---|---|
| Language | TypeScript | Go |
| Binary | Node.js + many deps | Single static binary (13MB) |
| Source files | 1000+ | ~45 |
| Auth | API keys only | Claude CLI OAuth2 or API key |
| Providers | 40+ (plugin system) | Claude (CLI OAuth + direct API) |
| Channels | 20+ (plugin system) | Telegram + Web Dashboard + CLI |
| Memory | LanceDB + embeddings | JSONL + keyword search |
| Config | ~200 fields | ~15 fields |
| Target user | Teams, multi-tenant | Individual, single machine |

<div class="doc-nav">
  <a href="{{ '/getting-started' | relative_url }}">
    <div class="label">Previous</div>
    <div class="title">Getting Started</div>
  </a>
  <a href="{{ '/features' | relative_url }}" class="next">
    <div class="label">Next</div>
    <div class="title">Features & Tools</div>
  </a>
</div>

</div>
</section>
