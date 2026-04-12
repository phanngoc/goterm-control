---
allowed-tools: Bash(go build *), Bash(git *), Bash(launchctl *), Bash(pgrep *), Bash(pkill *), Bash(kill *), Bash(curl *), Bash(sleep *), Bash(tail *), Bash(cat *), Read
description: "Checkout main, pull latest, build bomclaw, and reload the macOS gateway service"
---

# /deploy — Pull, Build & Reload Gateway

Checkout main, pull latest code, build the bomclaw binary, and restart the launchd gateway service on macOS.

## Steps

1. **Checkout main & pull latest**:
   ```bash
   cd /Users/ngocp/Documents/projects/meClaw/goterm-control
   git checkout main
   git pull origin main
   ```

2. **Build** the binary:
   ```bash
   cd /Users/ngocp/Documents/projects/meClaw/goterm-control
   go build -o bomclaw ./cmd/bomclaw/
   ```

3. **Stop** the running gateway service:
   ```bash
   launchctl stop com.nanoclaw.gateway
   ```

4. **Kill stale processes** — any leftover bomclaw/nanoclaw or claude subprocesses:
   ```bash
   sleep 1
   pkill -f "bomclaw gateway" 2>/dev/null || true
   pkill -f "nanoclaw gateway" 2>/dev/null || true
   pkill -f "./goterm" 2>/dev/null || true
   pkill -f "claude.*--resume" 2>/dev/null || true
   sleep 1
   ```
   Verify no stale processes remain:
   ```bash
   pgrep -lf "bomclaw|nanoclaw|goterm" || echo "clean — no stale processes"
   ```

5. **Start** the gateway service:
   ```bash
   launchctl start com.nanoclaw.gateway
   ```

6. **Verify** the service is running:
   ```bash
   sleep 2
   pgrep -lf bomclaw
   ```

7. **Health check** — confirm the gateway responds:
   ```bash
   curl -s http://127.0.0.1:18789/health
   ```

8. **Show recent logs** if health check fails:
   ```bash
   tail -20 ~/.goterm/logs/gateway.log
   tail -10 ~/.goterm/logs/gateway.err.log
   ```

## Expected output

Report:
- Git: branch, commit hash, pull result
- Build status (success/fail)
- Stale processes killed (if any)
- New PID
- Health check result
- If anything failed, show the relevant logs

## Notes

- The launchd service label is `com.nanoclaw.gateway` (will migrate to `com.bomclaw.gateway` after reinstall)
- Binary output path: `/Users/ngocp/Documents/projects/meClaw/goterm-control/bomclaw`
- Config: `/Users/ngocp/Documents/projects/meClaw/goterm-control/config.yaml`
- Logs: `~/.goterm/logs/gateway.log` and `gateway.err.log`
- Stale claude subprocesses (from `client.go` spawning `claude -p --resume`) are also killed to prevent Telegram polling conflicts
