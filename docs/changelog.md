---
layout: default
title: Changelog
nav_order: 10
---

# Changelog

## [Unreleased] - 2026-04-10

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
- **`cmd/nanoclaw/main.go`** — gateway command uses SQLite-backed session and memory stores

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
