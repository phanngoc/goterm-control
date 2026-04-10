---
title: Features & Tools
layout: default
---

<section class="doc-page">
<div class="doc-content" markdown="1">

# Features & Tools

<p class="lead">25 built-in tools give Claude full control over your machine.</p>

## Core Features

### Agentic Loop

NanoClaw runs an agentic loop that allows Claude to:

- Call tools, read results, and decide what to do next
- Chain up to **50 iterations** per request
- Handle complex, multi-step tasks autonomously
- Auto-retry on rate limits with exponential backoff
- Auto-compact context on overflow

### Multi-Channel Access

Interact with your agent through three channels, all backed by the same gateway:

- **Telegram Bot** &mdash; Chat from anywhere on your phone
- **Web Dashboard** &mdash; Real-time React SPA with session management
- **CLI Chat** &mdash; Direct terminal interaction, no gateway needed

### Cross-Session Memory

NanoClaw maintains a keyword-based memory system:

- Memories persist across sessions in JSONL format
- Up to 5 relevant memories injected per prompt
- Keyword matching for contextual relevance
- Stored at `~/.goterm/data/memory/memory.jsonl`

### Streaming Responses

All channels support real-time streaming:

- Telegram: messages update live as Claude generates text
- Dashboard: WebSocket streaming with partial updates
- CLI: Token-by-token terminal output

## System Tools

| Tool | Description |
|---|---|
| `run_shell` | Execute any bash command with configurable timeout |
| `read_file` | Read file contents from disk |
| `write_file` | Write or append to files |
| `list_dir` | List directory contents (recursive, hidden files) |
| `search_files` | Search by filename or content with regex support |
| `take_screenshot` | Capture the current screen |
| `get_clipboard` | Read clipboard contents |
| `set_clipboard` | Write to clipboard |
| `run_applescript` | Control macOS apps via AppleScript |
| `open_app` | Open applications or files (`open`/`xdg-open`) |
| `get_system_info` | Hardware, OS, CPU, memory, disk information |
| `list_processes` | List running processes with filter and sort |
| `kill_process` | Kill process by PID or name (TERM/KILL) |
| `browse_url` | Fetch URL content or open in default browser |

## Browser Automation Tools

NanoClaw includes a native Chrome DevTools Protocol (CDP) client &mdash; no Puppeteer or Playwright dependency needed.

| Tool | Description |
|---|---|
| `browser_navigate` | Navigate to a URL (auto-launches Chrome if needed) |
| `browser_snapshot` | DOM snapshot with interactive element refs |
| `browser_click` | Click an element by reference |
| `browser_fill` | Clear and type into an input field |
| `browser_type` | Append text to an input field |
| `browser_select` | Select a dropdown option |
| `browser_scroll` | Scroll the page in any direction |
| `browser_screenshot` | Screenshot the current browser page |
| `browser_get_text` | Get text, HTML, value, or URL from elements |
| `browser_eval` | Execute JavaScript in the browser context |
| `browser_wait` | Wait for an element, text, or timeout |

### How Browser Automation Works

1. When a browser tool is called, NanoClaw auto-launches Chrome with remote debugging enabled
2. It connects via the Chrome DevTools Protocol (raw WebSocket, no libraries)
3. The `browser_snapshot` tool returns a structured DOM with numbered element references
4. Other tools use these references to interact with specific elements
5. This allows Claude to browse the web, fill forms, scrape data, and automate workflows

### Example: Web Scraping Workflow

```
User: "Go to Hacker News and summarize the top 5 stories"

Claude's tool calls:
  1. browser_navigate("https://news.ycombinator.com")
  2. browser_snapshot()              → sees DOM with element refs
  3. browser_get_text(refs=[1,2,3,4,5])  → extracts headlines
  4. (generates summary from extracted text)
```

## Token Budget Management

NanoClaw intelligently manages the model's context window:

- Uses ~80% of available context for assembled messages
- Automatically trims old messages when budget is exceeded
- Compacts (summarizes) conversation history as a last resort
- System prompt + memory always included
- Token counting per model (different tokenizers)

## Per-Session Model Switching

Switch models on the fly without restarting:

- Telegram: `/model opus` or `/model haiku`
- Dashboard: Model selector dropdown
- CLI: `--model` flag
- Each session maintains its own model override
- Use aliases for convenience: `opus`, `sonnet`, `haiku`, `o4`, `s4`, `h4`

## FIFO Execution Queue

Each session has a FIFO execution queue that:

- Prevents concurrent API calls to Claude
- Queues messages that arrive while a request is in-flight
- Processes queued messages in order after completion
- Ensures consistent conversation state

<div class="doc-nav">
  <a href="{{ '/architecture' | relative_url }}">
    <div class="label">Previous</div>
    <div class="title">Architecture</div>
  </a>
  <a href="{{ '/telegram-bot' | relative_url }}" class="next">
    <div class="label">Next</div>
    <div class="title">Telegram Bot</div>
  </a>
</div>

</div>
</section>
