---
title: Configuration
layout: default
---

<section class="doc-page">
<div class="doc-content" markdown="1">

# Configuration

<p class="lead">BomClaw is designed to work with minimal configuration. Most fields have sensible defaults.</p>

## Configuration File

Edit `config.yaml` in the project root:

```yaml
claude:
  api_key: ""                    # Auto-detected from Claude CLI OAuth
  model: "claude-sonnet-4-6"     # Default model
  system_prompt: |
    You are an AI assistant with full control over this computer...

models:
  default: ""                    # Override claude.model
  # custom:                      # Add custom models
  #   - id: "deepseek-r1"
  #     name: "DeepSeek R1"
  #     context_window: 128000
  #     input_price: 0.55
  #     output_price: 2.19

session:
  data_dir: ""                   # Default: ~/.goterm/data
  # Sessions persist until explicit /reset command.

security:
  allowed_user_ids: []           # Telegram user whitelist (empty = all)

tools:
  shell_timeout: 60              # Seconds
  max_output_bytes: 8192         # Truncation limit
```

## Configuration Reference

### `claude`

| Field | Type | Default | Description |
|---|---|---|---|
| `api_key` | string | `""` | API key. Auto-detected from Claude CLI or `ANTHROPIC_API_KEY` env var. |
| `model` | string | `"claude-sonnet-4-6"` | Default model for new sessions. |
| `system_prompt` | string | (built-in) | System prompt sent with every request. |

### `models`

| Field | Type | Default | Description |
|---|---|---|---|
| `default` | string | `""` | Override `claude.model`. Takes precedence. |
| `custom` | list | `[]` | Additional model definitions (id, name, context_window, pricing). |

#### Custom Model Example

```yaml
models:
  custom:
    - id: "deepseek-r1"
      name: "DeepSeek R1"
      context_window: 128000
      input_price: 0.55
      output_price: 2.19
      aliases:
        - "deepseek"
        - "ds"
```

### `session`

| Field | Type | Default | Description |
|---|---|---|---|
| `data_dir` | string | `~/.goterm/data` | Directory for sessions and transcripts. |

### `security`

| Field | Type | Default | Description |
|---|---|---|---|
| `allowed_user_ids` | list | `[]` | Telegram user ID whitelist. Empty = allow everyone. |

### `tools`

| Field | Type | Default | Description |
|---|---|---|---|
| `shell_timeout` | int | `60` | Maximum seconds for shell command execution. |
| `max_output_bytes` | int | `8192` | Maximum output bytes before truncation. |

## Environment Variables

Environment variables override `config.yaml` values:

| Variable | Overrides | Description |
|---|---|---|
| `ANTHROPIC_API_KEY` | `claude.api_key` | Anthropic API key for direct API access |
| `TELEGRAM_TOKEN` | N/A | Telegram bot token (required for Telegram) |

## Data Directory

All persistent data is stored under the configured `session.data_dir` (default: `~/.goterm/data/`):

```
~/.goterm/data/
  goterm.db               # SQLite database (sessions, messages)
  transcripts/
    chat_<id>.jsonl       # Per-session conversation log
```

> **Tip:** Back up `~/.goterm/data/` to preserve your conversation history across reinstalls.

## Gateway Options

Command-line flags for the `gateway` command:

| Flag | Default | Description |
|---|---|---|
| `--port` | `18789` | Port to listen on |
| `--bind` | `127.0.0.1` | Address to bind to |

```bash
# Default: localhost only
./bomclaw gateway

# Expose to local network
./bomclaw gateway --bind 0.0.0.0 --port 9000
```

> **Warning:** Binding to `0.0.0.0` exposes BomClaw to your entire network. Only do this on trusted networks, and always configure `security.allowed_user_ids`.

<div class="doc-nav">
  <a href="{{ '/dashboard' | relative_url }}">
    <div class="label">Previous</div>
    <div class="title">Dashboard</div>
  </a>
  <a href="{{ '/api-reference' | relative_url }}" class="next">
    <div class="label">Next</div>
    <div class="title">API Reference</div>
  </a>
</div>

</div>
</section>
