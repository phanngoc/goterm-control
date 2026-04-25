---
title: Changelog
layout: default
---

<section class="doc-page">
<div class="doc-content" markdown="1">

# Changelog

<p class="lead">Release notes for BomClaw. The current release is <strong>v0.2.0</strong> — tagged 2026-04-25. <a href="https://github.com/phanngoc/goterm-control/releases/tag/v0.2.0" target="_blank" rel="noopener">View on GitHub →</a></p>

## Latest release

**v0.2.0** rolls up every change since v0.1.0: multi-session Telegram UI, SQLite hybrid storage, live `/status` reporting, rolling tool-progress window, typing + streamer heartbeat, session recovery fix, and the memory-package removal.

- **Multi-session** — `/sessions` inline keyboard, `/new`, tap to switch
- **Live `/status`** — elapsed time, last tool call, collected/queued message counts
- **SQLite storage** — sessions + messages persisted in `~/.goterm/data/goterm.db`
- **Rolling tool progress** — first 8 tools + sampled checkpoints every 10
- **Configurable execution timeout** — `claude.execution_timeout` (default 20 min)
- **Typing + heartbeat** — Telegram `typing…` keepalive + elapsed-time placeholder while Claude thinks

Full notes below.

## [0.2.0] - 2026-04-25

Second release since v0.1.0, rolling up multi-session support, the SQLite storage migration, live status reporting, and a batch of streaming/indicator fixes.

### Added

- **Multi-session support** — each chat can hold multiple parallel Claude sessions. `/sessions` lists them with an inline keyboard; `/new` starts a new one; tapping a row switches the active session.
- **Live `/status`** — reports whether the agent is currently running (elapsed, tool count, last tool, current task) and how many messages are waiting in the collector/engine queues. Ported from openclaw's `taskLine` pattern.
- **Rolling-window tool progress** — long runs now show the first 8 tools plus sampled checkpoints every 10, e.g. `Bash → Read → … → (+10 more) → Grep → Edit → …`, instead of truncating to the first few.
- **Configurable execution timeout** — `claude.execution_timeout` (default 20 min) caps stuck Claude CLI turns so the queue lane can recover automatically.
- **Typing indicator** — per-chat keepalive loop sends Telegram `typing…` while the agent works; TTL auto-matches `execution_timeout`, with heartbeat dedup to avoid wasted API calls.
- **Streamer heartbeat** — placeholder updates with elapsed time during Claude's thinking phase so the message doesn't look frozen before the first token arrives.
- **History context injection** — when a session is idle-reset (first message or after `/reset`), recent conversation history is loaded from SQLite and prepended to the system prompt.
- **Custom slash command forwarding** — unknown `/commands` are now passed through to Claude instead of being rejected, so user-defined skills under `.claude/commands/` work from Telegram.

### Changed

- **SQLite hybrid storage** — migrated from file-based storage (JSON + JSONL) to SQLite via `modernc.org/sqlite` (pure Go, no CGO). Sessions, messages, and the meta/version table all live in `~/.goterm/data/goterm.db`; transcripts are still written as JSONL for audit. Existing `sessions.json` is auto-imported on first run and kept in place for rollback.
- **Session recovery** — stale `ActiveSessionID` now falls back to the most recently updated session in the `ChatState` instead of wiping all sessions for the chat.
- **Bash tool labels** — show the subpath (e.g. `cd ../goterm-control`) instead of head-truncating long absolute paths.
- **Telegram chat-action TTL** — removed the separate `chat_action_ttl` config; it now tracks `execution_timeout` automatically.

### Fixed

- `/status` no longer reports `Queue: 0 pending` while a turn is actively running — it now shows `🔄 Running · Xs · tools: N` with the latest tool call.
- Typing indicator stops correctly when the agent finishes, even if the turn was cancelled mid-tool-call.
- Streamer no longer sends duplicate heartbeat edits when the elapsed bucket hasn't advanced.
- GitHub Pages renders markdown inside `<details>` tags (added the required blank line).

### Removed

- **Memory package** — the cross-session memory store (`internal/memory/` + FTS5 table) was dropped. It duplicated Claude's own session history and complicated the context budget; the `messages` table + history injection cover the remaining use case.

### Docs

- Platform-specific install guides for Mac and Ubuntu/Linux aimed at non-technical users.
- Getting-started no longer walks through raw API-key setup (OAuth / subscription path is the default).
- Added a Philosophy section to the GitHub Pages homepage.

---

## [Unreleased - superseded] - 2026-04-10

### SQLite Hybrid Storage Migration

Migrated from file-based storage (JSON + JSONL) to SQLite hybrid model for improved durability, query capability, and scalability.

#### Added

- **`internal/storage/` package** — centralized SQLite backend using `modernc.org/sqlite` (pure Go, no CGO)
  - `db.go` — database lifecycle with WAL mode, busy timeout, auto-migration
  - `schema.go` — DDL for sessions, messages, memory tables with FTS5 full-text search
  - `migrate.go` — auto-import from legacy `sessions.json` and `memory/memory.jsonl` on first run
  - `sessions.go` — `SQLiteSessionStore` implementing `SessionPersister` interface
  - `memory.go` — `SQLiteMemoryStore` with FTS5 search (replaces O(n) linear scan)
  - `messages.go` — `SQLiteMessageStore` for persistent conversation history
  - `db_test.go` — 12 tests covering all stores, migration, and idempotent open

- **Persistent compaction summary** — `compact_summary` field on sessions survives restart
- **Conversation message history** — user + assistant messages stored in SQLite `messages` table per turn
- **FTS5 full-text search** — memory entries indexed for fast keyword search with automatic sync triggers
- **Auto-migration** — existing `sessions.json` and `memory/memory.jsonl` imported into SQLite on first run (original files kept for rollback)

#### Changed

- **`internal/session/manager.go`** — `store *Store` changed to `store SessionPersister` interface for backend flexibility
- **`internal/session/session.go`** — added `CompactSummary` field, `Snapshot()`, `NewFromDB()` methods
- **`internal/memory/store.go`** — added `MemoryBackend` interface (`Append`, `Search`, `ReadAll`)
- **`internal/memory/inject.go`** — `BuildMemoryContext()` accepts `MemoryBackend` interface instead of concrete `*Store`
- **`internal/bot/bot.go`** — wiring switched from JSON file stores to `storage.Open()` + SQLite adapters
- **`internal/bot/handler.go`** — added `MessageStore` interface and message persistence in `runClaude()`
- **`internal/gateway/methods.go`** — `Deps.Memory` changed from `*memory.Store` to `memory.MemoryBackend`
- **`cmd/bomclaw/main.go`** — gateway command uses SQLite-backed session and memory stores

#### Storage Model Comparison

| Component | Before | After |
|-----------|--------|-------|
| Sessions | `sessions.json` (single JSON file) | SQLite `sessions` table |
| Messages | Not stored (Claude CLI owns state) | SQLite `messages` table |
| Memory | `memory/memory.jsonl` (linear scan) | SQLite `memory` table + FTS5 index |
| Transcripts | `transcripts/*.jsonl` | Unchanged (JSONL audit trail kept) |
| Compaction | In-memory only (lost on restart) | Persistent `compact_summary` column |
| Database | None | `~/.goterm/data/goterm.db` (single file) |

#### Technical Details

- **SQLite pragmas**: `journal_mode=WAL`, `busy_timeout=5000`, `foreign_keys=ON`, `synchronous=NORMAL`
- **Schema versioning**: `meta` table tracks `schema_version` for future migrations
- **Dual-write**: JSONL transcripts still written alongside SQLite for audit compliance
- **Backward compatible**: existing `*session.Store` and `*memory.Store` still satisfy the new interfaces
- **Binary size**: +15MB (pure-Go SQLite driver, no CGO dependency)

</div>
</section>
