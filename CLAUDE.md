# GoTerm Control — Project Notes

## Known Ops Issues

### Telegram bot conflict on restart
When restarting the gateway service, stale Claude CLI subprocesses (spawned by previous bot sessions) can hold Telegram's `getUpdates` polling connection. This causes `"Conflict: terminated by other getUpdates request"` errors and the bot stops receiving messages.

**Fix checklist before restart:**
1. Kill any orphaned claude subprocesses: `pgrep -lf "claude.*--resume" | grep -v grep` then `kill <pid>`
2. Clear Telegram state: `curl "https://api.telegram.org/bot${TOKEN}/deleteWebhook?drop_pending_updates=true"`
3. Then restart: `./bomclaw gateway restart`

**Root cause:** `client.go` spawns `claude -p --resume <session>` as a child process. If the parent gateway dies (crash, restart) without killing the child, the orphaned claude process keeps its Telegram long-poll alive. The new gateway instance then conflicts with it.

**TODO:** Add child process cleanup on gateway shutdown (kill all spawned claude subprocesses in `Bot.Shutdown()`).
