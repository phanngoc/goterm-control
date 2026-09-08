---
allowed-tools: Bash(go build *), Bash(go vet *), Bash(go test *), Bash(git *), Bash(launchctl *), Bash(pgrep *), Bash(pkill *), Bash(kill *), Bash(curl *), Bash(sleep *), Bash(tail *), Bash(cat *), Bash(cp *), Bash(rm *), Bash(mkdir *), Bash(ls *), Bash(codesign *), Bash(npm *), Bash(schtasks *), Bash(taskkill *), Bash(netstat *), PowerShell, Read
description: "Pull main, build, install artifacts to ~/.bomclaw, and reload the gateway service — macOS (LaunchAgent) or Windows 11 (Scheduled Task)"
---

# /deploy — Pull, Build, Install to ~/.bomclaw & Reload Gateway

**First: work out which host you are on**, because almost everything below
differs. `uname` / `$env:OS`, or just look at the working directory.

| | macOS | Windows 11 |
|---|---|---|
| Service | LaunchAgent `com.bomclaw.gateway` | Scheduled Task `\BomClaw\bomclaw-gateway` |
| Runtime dir | `~/.bomclaw/` | `~/.bomclaw/` |
| Binary | `bomclaw` | `bomclaw.exe` (+ `bomtray.exe`) |
| Code signing | **Required** — see below | Not applicable |
| Logs | `gateway.log` + `gateway.err.log` | `gateway.log` only |
| Default port | 18789 | **18790** on this machine (18789 is openclaw) |

The shape is the same on both: **the service runs from `~/.bomclaw/`, NOT from
the repo**, so deploy = build in the repo, copy the artifacts into
`~/.bomclaw/`, restart. Only macOS has a hard reason for that (TCC blocks
launchd jobs from reading `~/Documents/**`); on Windows it is still the right
habit, because a service pointing into a git worktree changes under you on
every branch switch.

---

# macOS

macOS TCC blocks launchd jobs from reading `~/Documents/**` — the binary hangs
forever in `dyld → open()` with no logs — which is why the copy is mandatory
rather than tidy.

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

## macOS notes

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

---

# Windows 11

**No code signing, no TCC, no `launchctl`.** Do not port the macOS ritual —
there is nothing to sign and nothing blocking reads from `~/Documents`. What
replaces it is a Scheduled Task, and `bomclaw gateway install` does the whole
registration in one command.

## Prerequisite: Go must be installed

`go` is **not** on this machine by default, and step 2 fails without it:

```powershell
winget install GoLang.Go     # then reopen the shell so PATH picks it up
go version                   # must be >= the `go` line in go.mod
```

## Steps

1. **Checkout main & pull latest**:
   ```bash
   cd ~/Documents/goterm-control
   git checkout main
   git fetch origin && git merge --ff-only origin/main
   ```
   If `git fetch` fails with `connect to host github.com port 22: Connection
   timed out`, that is this network, not the repo. `~/.ssh/config` already
   points `github.com-personal` at `ssh.github.com:443` for that reason; retry
   or check the host block.

2. **Verify before building.** Cheap, and it has caught a broken `main` more
   than once (a POSIX-only `syscall.Kill` landed and Windows stopped compiling):
   ```bash
   go vet ./... && go test ./...
   ```

3. **Build straight into the runtime directory.** Two binaries — the gateway
   and the tray:
   ```bash
   go build -trimpath -o ~/.bomclaw/bomclaw.exe ./cmd/bomclaw/
   go build -trimpath -o ~/.bomclaw/bomtray.exe ./cmd/bomtray/
   # only if dashboard/src changed:
   cd dashboard && npm ci && npm run build && cd ..
   rm -rf ~/.bomclaw/dashboard/dist && mkdir -p ~/.bomclaw/dashboard && cp -R dashboard/dist ~/.bomclaw/dashboard/dist
   ```
   Do NOT copy `config.yaml` / `.env` over the live ones. `~/.bomclaw/config.yaml`
   carries a **Windows** system prompt (PowerShell, no AppleScript, no
   `screencapture`) that the repo template does not. Diff and merge by hand.

4. **Refresh the Chrome extension copy** if `extension/` changed. It lives
   outside the repo on purpose: Chrome remembers the unpacked path forever, so
   pointing it at the worktree breaks the extension on every branch switch.
   ```bash
   rm -rf ~/.bomclaw/extension && mkdir -p ~/.bomclaw/extension
   cp ~/Documents/goterm-control/extension/* ~/.bomclaw/extension/
   ```
   Then hit ↻ on its card in `chrome://extensions`. The pairing survives a
   reload, so no token is needed again.

5. **Reinstall the service.** One command rewrites the task definition,
   re-registers it, restarts it and waits for health — there is no separate
   stop/start/verify dance:
   ```bash
   ~/.bomclaw/bomclaw.exe gateway install --force \
     --config 'C:/Users/phan.ngoc/.bomclaw/config.yaml' \
     --env 'C:/Users/phan.ngoc/.bomclaw/.env' \
     --port 18790
   ```
   **The port matters.** 18789 is taken by openclaw on this machine, so the
   gateway lives on 18790. Pass it every time; the flag default is 18789.

6. **Restart the tray** so it runs the new build:
   ```bash
   powershell -NoProfile -Command "Get-Process bomtray -EA SilentlyContinue | Stop-Process -Force"
   ~/.bomclaw/bomtray.exe install
   ```
   `install` writes the HKCU Run entry (start at logon) and launches it now.
   Windows 11 hides newly registered tray icons — click the **`^`** chevron and
   drag it onto the taskbar to keep it visible.

7. **Verify.** The health endpoint is the source of truth:
   ```bash
   curl -s http://127.0.0.1:18790/health
   ~/.bomclaw/bomclaw.exe gateway status --port 18790
   tail -5 ~/.goterm/logs/gateway.log
   ```
   Expect `Status: running (Running)`, and in the log: `bot: listening for
   updates`, `reporter: reporting delegated results`, `browser bridge:
   extension endpoint`.

   Also confirm there is **no console window** and only one process:
   ```powershell
   Get-Process bomclaw | Select-Object Id, MainWindowHandle
   ```
   `MainWindowHandle` must be `0`. A visible black window means `--hide-console`
   is not reaching the gateway — you are running an old binary.

## When to kill stale processes on Windows

Usually never: the task owns the gateway directly, so `/End` takes its process
tree with it. Check first, and only act if something is actually holding the
port or Telegram's poll:

```bash
netstat -ano -p tcp | grep ":18790.*LISTENING"     # who has the port
```

If an orphaned `claude` child is holding `getUpdates`, filter on the command
line — **not** on the image name. Every `bomclaw` subcommand is also
`bomclaw.exe`, and every interactive Claude Code is also `claude`:

```powershell
# LOOK first. The gateway spawns `claude -p --resume <id>`; interactive
# Claude Code never passes -p, and that is the only reliable difference.
Get-CimInstance Win32_Process -Filter "Name='claude.exe'" |
  Where-Object { $_.CommandLine -like '* -p *' -and $_.CommandLine -like '*--resume*' } |
  Select-Object ProcessId, CommandLine
# only then:
#   taskkill /PID <pid> /T /F
```

Then clear Telegram state if you saw a Conflict error. **Use this gateway's
token, not openclaw's** — they are different bots (`8775702070…` here):

```bash
curl "https://api.telegram.org/bot${TOKEN}/deleteWebhook?drop_pending_updates=true"
```

## Reading Task Scheduler without being alarmed

These look like failures and are not:

| Signal | Meaning |
|---|---|
| `LastTaskResult 267009` (`0x41301`) | The task is running right now. Normal. |
| `LastTaskResult 2147946720` (`0x800710E0`) once a minute | The keep-alive tick was refused because an instance is already running — `MultipleInstancesPolicy: IgnoreNew` doing its job. Normal. |
| `Status: stopped (Ready)` while `/health` answers | You are on a pre-fix binary. The task used to wrap the gateway in `cmd.exe`, so its state was cmd's. Rebuild. |

The keep-alive is a `TimeTrigger` repeating every minute, because
`RestartOnFailure` does **not** restart an action that exited non-zero — Task
Scheduler counts that as a completed run. Consequence to know: a gateway that
crash-loops on a bad config retries every minute and appends to
`gateway.log` indefinitely. While you fix the config, pause it:

```powershell
schtasks /Change /TN "\BomClaw\bomclaw-gateway" /DISABLE   # /ENABLE to resume
```

`bomclaw gateway stop` also disables the task — deliberately, because the
keep-alive would otherwise restart it within a minute. `gateway start`
re-enables. So if the gateway will not start, check the task is not `Disabled`.

## Windows notes

- Task: `\BomClaw\bomclaw-gateway` (definition at `%LOCALAPPDATA%\BomClaw\bomclaw-gateway.xml`)
- Runtime layout: `~/.bomclaw/{bomclaw.exe,bomtray.exe,config.yaml,.env,dashboard/dist,extension/}`
- Tray autostart: `HKCU\Software\Microsoft\Windows\CurrentVersion\Run\BomClawTray`
- **One combined log**, `~/.goterm/logs/gateway.log` — the gateway writes it
  itself via `--log-file`. There is no `gateway.err.log` on Windows; an old one
  left over from the `cmd.exe` era is stale and frozen, ignore it.
- The task runs with an **InteractiveToken** on purpose: a real Windows service
  would land in session 0 and see neither the desktop nor your browser, which
  is most of what the agent is for.
- `bomclaw browser …` has **no `--port` flag**. It reads `BOMCLAW_GATEWAY_ADDR`
  and otherwise defaults to 18789 — which is openclaw here, so it returns
  `Not Found` and an empty token. Export it first:
  ```powershell
  $env:BOMCLAW_GATEWAY_ADDR = "http://127.0.0.1:18790"
  ```
  Agents spawned by the gateway inherit it correctly; only manual CLI use needs this.
- After every restart the **browser bridge disconnects**. The extension is
  Manifest V3, so its service worker is evicted when idle and its reconnect
  timer dies with it. Opening the extension popup wakes it. Check with
  `bomclaw browser status`.
- `bomclaw status` reports "offline" whenever dashboard auth is enabled (it
  dials `/ws` unauthenticated) — same as macOS. `/health` is the truth.
- Coordination is shared at `~/.goterm-shared/data/coord.db`. Opening it
  applies pending migrations, so a deploy that adds a column takes effect on
  first start with no separate step.

## Expected output

Report:
- Git: branch, commit hash, pull result
- `go vet` / `go test` result
- Build status (both binaries; dashboard and extension if rebuilt)
- Service: task state, port, health response
- Tray: running, Run entry present
- Anything degraded — browser bridge needing a popup click is the common one
