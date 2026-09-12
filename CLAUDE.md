# GoTerm Control — Project Notes

## Known Ops Issues

### Telegram bot conflict on restart
When restarting the gateway service, stale Claude CLI subprocesses (spawned by previous bot sessions) can hold Telegram's `getUpdates` polling connection. This causes `"Conflict: terminated by other getUpdates request"` errors and the bot stops receiving messages.

**Fix checklist before restart:**
1. Kill any orphaned claude subprocesses: `pgrep -lf "claude -p .*--resume"` then `kill <pid>`

   Match on `claude -p .*--resume`, never `claude.*--resume`. The broad pattern
   also matches your own interactive Claude Code sessions, which resume too,
   and has killed one during a deploy. The gateway spawns the CLI with `-p`
   right after the binary (see `buildArgs` in `internal/claude/client.go`);
   interactive Claude Code does not, and that is the only reliable difference.
2. Clear Telegram state: `curl "https://api.telegram.org/bot${TOKEN}/deleteWebhook?drop_pending_updates=true"`
3. Then restart: `./bomclaw gateway restart`

**Root cause — there are two, and this file only had one until 2026-09-10:**

1. **Orphaned subprocesses.** `client.go` spawns `claude -p --resume <session>`
   as a child process. If the parent gateway dies (crash, restart) without
   killing the child, the orphaned claude process keeps its Telegram long-poll
   alive. The new gateway instance then conflicts with it.

   **Fixed 2026-09-12.** Children are spawned into their own process group and
   registered in `internal/execution/children.go`; `Bot.Shutdown()` and the
   gateway's own exit path both call `KillSpawned()`, which signals the group
   so a CLI's own helpers die with it. Two gaps were part of the same bug:
   `defer tgBot.Shutdown()` sat inside the `telegram.poll` branch, so agents 2
   and 3 never ran it, and `log.Fatalf` on a failed start skipped every defer.
   A `kill -9` of the gateway is still beyond reach — the checklist below stays
   useful for that.

2. **Two gateways, one token.** Telegram serves `getUpdates` to exactly one
   consumer per token. Agents 1 and 2 both had `TELEGRAM_TOKEN` in their `.env`
   — which overrides `telegram.token: ""` in config (`config.go` Load) — so
   both were polling the same bot, permanently. Fixed by `telegram.poll`
   (default true): agent 1 polls, agents 2 and 3 set it false. They still build
   the bot object, because that object is the shared turn engine `deps.Turn`;
   dropping the token instead would push the dashboard and `bomclaw send` onto
   the older non-streaming `agent.RunAgent` path.

   Check with: `grep -c "telegram polling off" ~/.goterm<N>/logs/gateway.err.log`

**Agents on this machine:** `bomclaw` (:18789, claude), `bomclaw2` (:18790,
codex), `bomclaw3` (:18791, claude, shares agent 1's OAuth quota). One shared
binary at `~/.bomclaw/bomclaw`, so a deploy restarts all three. Adding one:
`docs/adding-an-agent.md`.

Both causes are now fixed in code. What remains is the ungraceful case (SIGKILL, power loss), where nothing in the process can run cleanup — that is what the checklist above is for.
