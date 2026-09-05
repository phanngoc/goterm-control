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
   a NEW TCC identity and macOS re-asks for folder permissions:
   ```bash
   cp bomclaw ~/.bomclaw/bomclaw
   codesign -f -s "BomClaw Code Signing" --identifier com.bomclaw.gateway ~/.bomclaw/bomclaw \
     || codesign -f -s - --identifier com.bomclaw.gateway ~/.bomclaw/bomclaw
   cp config.yaml ~/.bomclaw/config.yaml
   cp .env ~/.bomclaw/.env
   rm -rf ~/.bomclaw/dashboard/dist && cp -R dashboard/dist ~/.bomclaw/dashboard/dist
   # Agents call `bomclaw task/note/msg` from their own shell, and the launchd
   # PATH does not include ~/.bomclaw. The symlink target is stable, so this
   # only has to be created once — but check it, a wiped ~/.local/bin breaks
   # every coordination command the agents run.
   ln -sf ~/.bomclaw/bomclaw ~/.local/bin/bomclaw
   ```

4. **Stop** the gateway and kill stale processes (orphaned `claude --resume`
   subprocesses hold Telegram's getUpdates poll and cause Conflict errors):
   ```bash
   launchctl stop com.bomclaw.gateway
   sleep 1
   pkill -f "bomclaw gateway" 2>/dev/null || true
   pkill -f "claude.*--resume" 2>/dev/null || true
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
