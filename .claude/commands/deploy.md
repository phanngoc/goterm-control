---
allowed-tools: Bash(go build *), Bash(git *), Bash(launchctl *), Bash(pgrep *), Bash(pkill *), Bash(kill *), Bash(curl *), Bash(sleep *), Bash(tail *), Bash(cat *), Bash(cp *), Bash(codesign *), Bash(npm *), Read
description: "Checkout main, pull latest, build bomclaw, install artifacts to ~/.bomclaw, and reload the macOS gateway service"
---

# /deploy — Pull, Build, Install to ~/.bomclaw & Reload Gateway

The gateway service **runs from `~/.bomclaw/`, NOT from the repo**. macOS TCC
blocks launchd jobs from reading `~/Documents/**` (the binary hangs forever in
`dyld → open()` with no logs), so deploy = build in the repo, then copy the
artifacts into `~/.bomclaw/` and restart.

## Steps

1. **Checkout main & pull latest** (fetch + ff-only survives dirty docs files):
   ```bash
   cd /Users/ngocp/Documents/projects/meClaw/goterm-control
   git checkout main
   git fetch origin && git merge --ff-only origin/main
   ```

2. **Build** the binary (and dashboard if `dashboard/src` changed):
   ```bash
   go build -o bomclaw ./cmd/bomclaw/
   # only if dashboard/src changed:
   cd dashboard && npm run build && cd ..
   ```

3. **Install artifacts to ~/.bomclaw** and re-sign (launchd refuses unsigned
   swaps). Sign with the "BomClaw Code Signing" keychain identity — it keeps
   the TCC identity stable across deploys so Documents/Desktop grants survive
   binary swaps. Ad-hoc (`-s -`) is the fallback, but every ad-hoc rebuild is
   a NEW TCC identity and macOS re-asks for folder permissions.

   **Verify the signature landed BEFORE restarting.** `codesign` can hang on a
   keychain "allow access" dialog; a `timeout` kills it, and the file is left
   with Go's linker ad-hoc signature (`Identifier=a.out`). launchd then kills
   the new process at exec with `OS_REASON_CODESIGNING` — both gateways went
   down for four minutes this way (2026-09-06). Never pipe `codesign` into
   `tail` inside an `if`: the `if` tests `tail`'s exit code, not codesign's.
   ```bash
   cp bomclaw ~/.bomclaw/bomclaw
   timeout 30 codesign -f -s "BomClaw Code Signing" --identifier com.bomclaw.gateway ~/.bomclaw/bomclaw
   echo "codesign exit=$?"       # 124 = hung on the keychain dialog, see below
   codesign -dv ~/.bomclaw/bomclaw 2>&1 | grep Identifier
   # MUST print Identifier=com.bomclaw.gateway. If it prints Identifier=a.out,
   # do NOT restart. Either click "Always Allow" on the keychain dialog and
   # sign again, or fall back to ad-hoc to restore service:
   #   codesign -f -s - --identifier com.bomclaw.gateway ~/.bomclaw/bomclaw
   rm -rf ~/.bomclaw/dashboard/dist && cp -R dashboard/dist ~/.bomclaw/dashboard/dist
   # Do NOT copy config.yaml / .env over the live ones. ~/.bomclaw/config.yaml
   # and ~/.bomclaw2/config.yaml have diverged from the repo template
   # (agent name, auto_claim, system prompt edits). Diff and merge by hand:
   #   diff ~/.bomclaw/config.yaml config.yaml
   # Agents call `bomclaw task/note/msg` from their own shell, and the launchd
   # PATH does not include ~/.bomclaw. The symlink target is stable, so this
   # only has to be created once — but check it, a wiped ~/.local/bin breaks
   # every coordination command the agents run.
   ln -sf ~/.bomclaw/bomclaw ~/.local/bin/bomclaw
   ```

4. **Stop** the gateway and kill stale processes (orphaned `claude -p --resume`
   subprocesses hold Telegram's getUpdates poll and cause Conflict errors).

   The pattern MUST be `claude -p .*--resume`, not `claude.*--resume`. The
   broad one also matches your own interactive Claude Code sessions — which
   resume too — and has killed one mid-deploy. The gateway always spawns with
   `-p` immediately after the binary (`internal/claude/client.go` `buildArgs`),
   and interactive Claude Code never does, so `-p` is what separates them.
   Check before you kill: `pgrep -lf "claude -p .*--resume"`.
   ```bash
   launchctl stop com.bomclaw.gateway
   sleep 1
   pkill -f "bomclaw gateway" 2>/dev/null || true
   pkill -f "claude -p .*--resume" 2>/dev/null || true
   sleep 1
   ```

5. **Start** (KeepAlive usually respawns on its own — start is idempotent):
   ```bash
   launchctl start com.bomclaw.gateway
   sleep 3
   pgrep -lf "bomclaw gateway"
   ```
   The process command line must show `/Users/ngocp/.bomclaw/bomclaw`.

6. **Health check**:
   ```bash
   curl -s http://127.0.0.1:18789/health
   ```
   Note: `bomclaw status` reports "offline" when dashboard auth is enabled
   (it dials /ws unauthenticated) — the health endpoint is the source of truth.

7. **If health fails**, check logs and the dyld-stall signature:
   ```bash
   tail -20 ~/.goterm/logs/gateway.err.log
   ```
   - No startup lines at all + process alive + no TCP sockets → dyld stall.
     Verify the process runs from `~/.bomclaw` (NOT the repo path). Diagnose
     with: `launchctl submit -l t -o /tmp/t.out -- <binary> help` — if a repo
     path stalls but a `~/.bomclaw` copy prints usage, it's the TCC block.
   - Process gone, `launchctl print gui/$(id -u)/com.bomclaw.gateway` shows
     `last exit reason = OS_REASON_CODESIGNING` → the binary's signature is
     not the identity's (step 3 verification skipped). Re-sign, verify the
     Identifier, `launchctl kickstart -k gui/$(id -u)/com.bomclaw.gateway`.
   - `Conflict: terminated by other getUpdates` → redo step 4, then
     `curl "https://api.telegram.org/bot${TOKEN}/deleteWebhook?drop_pending_updates=true"`

## Expected output

Report:
- Git: branch, commit hash, pull result
- Build status (Go + dashboard if built)
- Artifacts copied + codesign status
- New PID (must be under ~/.bomclaw)
- Health check result

## Notes

- Service label: `com.bomclaw.gateway` (plist: `~/Library/LaunchAgents/com.bomclaw.gateway.plist`)
- Runtime layout: `~/.bomclaw/{bomclaw,config.yaml,.env,dashboard/dist}`
- Data/logs stay in `~/.goterm/`; workspace in `~/goterm-workspace` — all outside TCC paths
- Second agent: label `com.bomclaw2.gateway`, config `~/.bomclaw2/`, data `~/.goterm2/`,
  port 18790 — **shares the same binary**, so every deploy restarts both
- Coordination (traces, tasks, notes, messages) is shared at `~/.goterm-shared/data/coord.db`;
  `agent.id` must differ per gateway or they overwrite each other's registration
- NEVER point the LaunchAgent at a binary/config inside `~/Documents` — TCC
  blocks launchd reads there (Apple-signed tools get EPERM; ad-hoc binaries
  hang in dyld with zero logs)
- Re-install from scratch if the plist is missing:
  `cd ~/.bomclaw && ./bomclaw gateway install --config ~/.bomclaw/config.yaml --env ~/.bomclaw/.env`
