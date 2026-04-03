# NanoClaw

Lean AI agent for remote host control. One binary, one process, no microservices.

NanoClaw gives Claude full control over your Mac through Telegram or a local CLI.
It runs an agentic loop — the model calls tools, sees results, and keeps going
until the task is done. Think of it as a personal, self-hosted Claude Code
that you talk to from anywhere.

**Philosophy:**
- Small enough to understand (~45 source files, 14 packages)
- Full host control — agents run on your machine
- Built for one user — bespoke, not a framework
- Customization = code changes, not config sprawl

## Quick Start

```bash
# Clone
git clone https://github.com/phanngoc/goterm-control.git
cd goterm-control

# Set up credentials
cp .env.example .env
# Edit .env: add TELEGRAM_TOKEN and ANTHROPIC_API_KEY

# Build
go build -o nanoclaw ./cmd/nanoclaw/

# Interactive chat (no Telegram needed)
./nanoclaw chat

# Or start the gateway (Telegram + WebSocket RPC)
./nanoclaw gateway
```

### Prerequisites

- Go 1.22+
- An Anthropic API key (set `ANTHROPIC_API_KEY` in `.env`)
- For Telegram: a bot token from [@BotFather](https://t.me/BotFather)

### Environment Variables

| Variable | Required | Description |
|---|---|---|
| `ANTHROPIC_API_KEY` | Yes | Anthropic API key for Claude |
| `TELEGRAM_TOKEN` | For Telegram | Telegram bot token |

## Commands

```
nanoclaw gateway     Start gateway (Telegram bot + WebSocket RPC server)
nanoclaw chat        Interactive CLI chat (direct API, no gateway needed)
nanoclaw send        Send a message via the gateway
nanoclaw status      Show gateway status
nanoclaw models      List available models
```

### Examples

```bash
# Chat with Claude directly
./nanoclaw chat
./nanoclaw chat --model opus

# Start the gateway on a custom port
./nanoclaw gateway --port 9000 --bind 0.0.0.0

# Send a message to the running gateway
./nanoclaw send "list all running docker containers"
./nanoclaw send --model haiku "what time is it"

# Check gateway health
./nanoclaw status
```

### Telegram Commands

Once the gateway is running with a Telegram token:

| Command | Description |
|---|---|
| `/start` | Show welcome and help |
| `/models` | List available models with pricing |
| `/model <name>` | Switch model (e.g. `/model opus`, `/model haiku`) |
| `/model default` | Reset to default model |
| `/status` | Show session info, token usage, queue depth |
| `/reset` | Clear conversation history |
| `/cancel` | Cancel in-flight request |

## Architecture

```
                    ┌──────────────┐
                    │   Telegram   │
                    │   Channel    │
                    └──────┬───────┘
                           │
┌──────────┐       ┌───────▼────────┐       ┌──────────────┐
│  CLI     │       │    Handler /   │       │   Execution  │
│  Channel ├──────►│    Gateway     ├──────►│   Engine     │
└──────────┘       │    (RPC)       │       │  (FIFO/chat) │
                   └───────┬────────┘       └──────┬───────┘
                           │                       │
                   ┌───────▼────────┐       ┌──────▼───────┐
                   │  Model         │       │   Agent      │
                   │  Resolver      │       │   Loop       │
                   └───────┬────────┘       └──────┬───────┘
                           │                       │
                   ┌───────▼────────┐       ┌──────▼───────┐
                   │  Anthropic     │◄──────│   Context    │
                   │  Direct API    │       │   Engine     │
                   └───────┬────────┘       └──────────────┘
                           │
                   ┌───────▼────────┐
                   │  Tool Executor │
                   │  (14 tools)    │
                   └────────────────┘
```

### The Agent Loop (the core)

The heart of NanoClaw is in `internal/agent/loop.go`. It runs:

```
user message
  → assemble context (under token budget)
  → stream model response
  → if model calls tools:
      → execute tools
      → feed results back to model
      → loop again
  → if model says "end_turn":
      → return response
  → if context overflow:
      → compact (summarize old messages)
      → retry
  → if rate limited:
      → exponential backoff → retry
```

This loop runs up to 50 iterations per request. The model can chain multiple
tool calls, read results, and keep working until the task is done.

### Packages

| Package | Purpose | Inspired by |
|---|---|---|
| `agent/` | Core agentic loop with retry and tool execution | openclaw `pi-embedded-runner` |
| `anthropic/` | Direct Anthropic Messages API with streaming | openclaw provider plugins |
| `channel/` | Channel abstraction (Telegram, CLI) | openclaw `channels/plugins` |
| `context/` | Token counting, budget assembly, compaction | openclaw `context-engine` |
| `gateway/` | WebSocket JSON-RPC server for remote control | openclaw `gateway` |
| `execution/` | Per-session FIFO queue (prevents concurrent calls) | openclaw `process/command-queue` |
| `session/` | Persistent sessions with idle timeout | openclaw `config/sessions` |
| `memory/` | Cross-session keyword memory | openclaw LanceDB memory |
| `models/` | Model catalog with aliases and per-session override | openclaw `model-catalog` |
| `transcript/` | JSONL event recording per session | openclaw session transcripts |
| `tools/` | 14 Mac control tools (shell, files, screenshot, ...) | openclaw agent tools |
| `config/` | YAML config with env var overrides | openclaw config system |
| `bot/` | Telegram bot with streaming message edits | openclaw Telegram channel |
| `claude/` | Claude CLI subprocess (fallback provider) | — |

### Tools

The agent has 14 tools for full Mac control:

| Tool | What it does |
|---|---|
| `run_shell` | Execute any bash command |
| `read_file` | Read file contents |
| `write_file` | Write/append to files |
| `list_dir` | List directory (recursive, hidden files) |
| `search_files` | Search by filename or content (regex) |
| `take_screenshot` | Capture screen |
| `get_clipboard` | Read clipboard |
| `set_clipboard` | Write to clipboard |
| `run_applescript` | Control Mac apps via AppleScript |
| `open_app` | Open applications or files |
| `get_system_info` | Hardware, OS, CPU, memory, disk |
| `list_processes` | Running processes with filter/sort |
| `kill_process` | Kill by PID or name (TERM/KILL) |
| `browse_url` | Open URL in default browser |

### Data Persistence

All state lives under `~/.goterm/data/`:

```
~/.goterm/data/
  sessions.json           # session metadata (persisted on change, atomic writes)
  transcripts/
    chat_<id>.jsonl       # per-session conversation log (append-only)
  memory/
    memory.jsonl          # cross-session keyword memory
```

### Models

Three built-in Claude models with aliases for quick switching:

| Model | Aliases | Context | Cost (in/out per 1M) |
|---|---|---|---|
| `claude-opus-4-6` | `opus`, `o4` | 200k | $15 / $75 |
| `claude-sonnet-4-6` | `sonnet`, `s4` | 200k | $3 / $15 |
| `claude-haiku-4-5` | `haiku`, `h4` | 200k | $0.80 / $4 |

Add custom models in `config.yaml` under `models.custom`.

## Configuration

Edit `config.yaml`:

```yaml
claude:
  api_key: ""                    # or set ANTHROPIC_API_KEY env var
  model: "claude-sonnet-4-6"     # default model
  system_prompt: |
    You are an AI assistant with full control over a Mac...

models:
  default: ""                    # override claude.model
  # custom:                      # add custom models
  #   - id: "deepseek-r1"
  #     name: "DeepSeek R1"
  #     ...

session:
  data_dir: ""                   # default: ~/.goterm/data
  idle_timeout: 30               # minutes before auto-reset

memory:
  enabled: true
  max_entries: 5                 # memories injected per prompt

security:
  allowed_user_ids: []           # Telegram user whitelist (empty = allow all)

tools:
  shell_timeout: 60              # seconds
  max_output_bytes: 8192         # truncation limit
```

Minimal config — most fields have sensible defaults. Set `ANTHROPIC_API_KEY`
and `TELEGRAM_TOKEN` in `.env` and you're running.

## Development

```bash
# Build
go build ./...

# Run tests (21 tests across 5 packages)
go test ./internal/... -v

# Build the binary
go build -o nanoclaw ./cmd/nanoclaw/

# Build the Telegram-only bot (legacy entry point)
go build -o goterm ./cmd/goterm/
```

### Project Structure

```
cmd/
  nanoclaw/main.go          CLI entry point (gateway, chat, send, status, models)
  goterm/main.go            Legacy Telegram-only entry point

internal/
  agent/                    Core agent loop + types
  anthropic/                Direct Anthropic API client
  bot/                      Telegram bot (handler, streamer)
  channel/                  Channel interface + CLI implementation
  claude/                   Claude CLI subprocess (fallback)
  config/                   YAML config loading
  context/                  Context engine (tokens, assembly, compaction)
  execution/                Per-session FIFO execution queue
  gateway/                  WebSocket JSON-RPC server
  memory/                   Cross-session keyword memory
  models/                   Model catalog + resolver
  session/                  Persistent session management
  tools/                    14 Mac control tools
  transcript/               JSONL event recording
```

## Security

- The agent runs with `--permission-mode bypassPermissions` — all tool calls
  execute without confirmation. This is intentional for a personal bot.
- Restrict access via `security.allowed_user_ids` in config.
- The gateway binds to `127.0.0.1` by default (localhost only).
- API keys are read from `.env` (gitignored) or environment variables.

## Compared to openclaw

NanoClaw is inspired by [openclaw](https://github.com/openclaw/openclaw) but
radically simplified:

| | openclaw | NanoClaw |
|---|---|---|
| Language | TypeScript | Go |
| Binary | Node.js + many deps | Single static binary (13MB) |
| Source files | 1000+ | 45 |
| Providers | 40+ (plugin system) | Claude (direct API + CLI fallback) |
| Channels | 20+ (plugin system) | Telegram + CLI + Gateway |
| Memory | LanceDB + embeddings | JSONL + keyword search |
| Config | ~200 config fields | ~15 config fields |
| Context engine | Pluggable with DAG branching | Simple budget-based trimming |
| Target user | Teams, multi-tenant | Individual, single machine |

## License

MIT
