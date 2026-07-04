//go:build darwin

// bomtray is a macOS menu bar companion for the bomclaw gateway.
// It polls the gateway's /api/status endpoint, shows live agent state in the
// menu bar, exposes quick actions, and can keep the Mac awake while the
// agent is running (IOKit power assertion, kwota pattern).
//
// It is a separate binary on purpose: when the gateway dies, the tray icon
// survives and turns red instead of vanishing.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/systray"
	"github.com/ngocp/goterm-control/internal/gateway"
	"github.com/ngocp/goterm-control/internal/memory"
)

type awakeMode string

const (
	awakeOff    awakeMode = "off"
	awakeAuto   awakeMode = "auto" // hold only while the agent is running
	awakeAlways awakeMode = "always"
)

type app struct {
	url          string
	workspace    string
	gatewayLabel string
	logPath      string
	interval     time.Duration

	mem   *memory.Manager
	awake *awake
	mode  awakeMode

	statusItem *systray.MenuItem
	runItem    *systray.MenuItem
	memItem    *systray.MenuItem
	modeOff    *systray.MenuItem
	modeAuto   *systray.MenuItem
	modeAlways *systray.MenuItem

	// previous poll state, for transition notifications
	upKnown    bool
	wasUp      bool
	prevTask   string
	prevRunning bool
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "install":
			runInstall()
			return
		case "uninstall":
			runUninstall()
			return
		}
	}

	home, _ := os.UserHomeDir()
	url := flag.String("url", "http://127.0.0.1:18789", "Gateway base URL")
	workspace := flag.String("workspace", filepath.Join(home, "goterm-workspace"), "Agent workspace (memory files)")
	label := flag.String("gateway-label", "com.nanoclaw.gateway", "launchd label of the gateway service")
	logPath := flag.String("log", filepath.Join(home, ".goterm/logs/gateway.err.log"), "Gateway log file for Tail Logs")
	interval := flag.Int("interval", 3, "Poll interval in seconds")
	flag.Parse()

	a := &app{
		url:          strings.TrimRight(*url, "/"),
		workspace:    *workspace,
		gatewayLabel: *label,
		logPath:      *logPath,
		interval:     time.Duration(*interval) * time.Second,
		awake:        &awake{},
		mode:         loadAwakeMode(),
		mem: memory.NewManager(memory.Config{
			Enabled: true,
			Dir:     *workspace,
		}),
	}

	systray.Run(a.onReady, a.onExit)
}

func (a *app) onReady() {
	systray.SetTitle("🤖")
	systray.SetTooltip("bomclaw gateway")

	a.statusItem = systray.AddMenuItem("⏳ Connecting...", "")
	a.statusItem.Disable()
	a.runItem = systray.AddMenuItem("💤 Idle", "")
	a.runItem.Disable()
	a.memItem = systray.AddMenuItem("💾 Memory: ...", "Open MEMORY.md")

	systray.AddSeparator()
	awakeMenu := systray.AddMenuItem("Keep Mac Awake", "Prevent system sleep (display may still sleep)")
	a.modeOff = awakeMenu.AddSubMenuItemCheckbox("Off", "", a.mode == awakeOff)
	a.modeAuto = awakeMenu.AddSubMenuItemCheckbox("While bot is running", "Hold a power assertion only during agent runs", a.mode == awakeAuto)
	a.modeAlways = awakeMenu.AddSubMenuItemCheckbox("Always", "", a.mode == awakeAlways)

	systray.AddSeparator()
	openDash := systray.AddMenuItem("Open Dashboard", "")
	openWs := systray.AddMenuItem("Open Workspace", "")
	tailLogs := systray.AddMenuItem("Tail Logs", "Open Terminal tailing the gateway log")
	restart := systray.AddMenuItem("Restart Gateway", "launchctl kickstart -k")

	systray.AddSeparator()
	quit := systray.AddMenuItem("Quit bomtray", "")

	// Click handler loop
	go func() {
		for {
			select {
			case <-a.memItem.ClickedCh:
				openPath(filepath.Join(a.workspace, "MEMORY.md"))
			case <-a.modeOff.ClickedCh:
				a.setMode(awakeOff)
			case <-a.modeAuto.ClickedCh:
				a.setMode(awakeAuto)
			case <-a.modeAlways.ClickedCh:
				a.setMode(awakeAlways)
			case <-openDash.ClickedCh:
				openPath(a.url)
			case <-openWs.ClickedCh:
				openPath(a.workspace)
			case <-tailLogs.ClickedCh:
				openTerminalTail(a.logPath)
			case <-restart.ClickedCh:
				restartGateway(a.gatewayLabel)
			case <-quit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()

	// Poll loop
	go func() {
		a.poll()
		ticker := time.NewTicker(a.interval)
		defer ticker.Stop()
		for range ticker.C {
			a.poll()
		}
	}()
}

func (a *app) onExit() {
	a.awake.Release()
}

// poll fetches gateway status and updates the menu, awake assertion, and
// transition notifications.
func (a *app) poll() {
	st, err := fetchStatus(a.url)
	up := err == nil
	running := up && len(st.Runs) > 0

	// --- transition notifications ---
	if a.upKnown && up != a.wasUp {
		if up {
			notify("Bomclaw", "Gateway is back up ✅")
		} else {
			notify("Bomclaw", "Gateway is DOWN ⛔")
		}
	}
	if a.prevRunning && !running && up {
		task := a.prevTask
		if task == "" {
			task = "agent task"
		}
		notify("Bomclaw", "✅ Done: "+task)
	}
	a.upKnown = true
	a.wasUp = up
	a.prevRunning = running
	if running {
		a.prevTask = st.Runs[0].Task
	}

	// --- awake assertion ---
	switch a.mode {
	case awakeAlways:
		a.holdAwake()
	case awakeAuto:
		if running {
			a.holdAwake()
		} else {
			a.awake.Release()
		}
	default:
		a.awake.Release()
	}

	// --- menu bar title ---
	title := "🤖"
	switch {
	case !up:
		title = "🤖⛔"
	case running:
		title = "🤖⚡"
	}
	if a.awake.Held() {
		title += "☕"
	}
	systray.SetTitle(title)

	// --- menu items ---
	if up {
		a.statusItem.SetTitle(fmt.Sprintf("🟢 Gateway up · %s · %s", st.Uptime, st.DefaultModel))
	} else {
		a.statusItem.SetTitle("🔴 Gateway down")
	}

	if running {
		r := st.Runs[0]
		line := fmt.Sprintf("⚡ %s · %d tools", truncate(r.Task, 40), r.ToolCount)
		if r.LastTool != "" {
			line += " · " + truncate(r.LastTool, 20)
		}
		a.runItem.SetTitle(line)
	} else {
		a.runItem.SetTitle("💤 Idle")
	}

	if stats, err := a.mem.Stats(time.Now()); err == nil {
		a.memItem.SetTitle(fmt.Sprintf("💾 Memory: %s · %d notes", humanBytes(stats.MemoryMDBytes), stats.DailyNoteCount))
	}
}

func (a *app) holdAwake() {
	if err := a.awake.Hold(); err != nil {
		log.Printf("bomtray: awake: %v", err)
	}
}

// setMode switches the awake mode, updates checkboxes, and persists it.
func (a *app) setMode(m awakeMode) {
	a.mode = m
	a.modeOff.Uncheck()
	a.modeAuto.Uncheck()
	a.modeAlways.Uncheck()
	switch m {
	case awakeOff:
		a.modeOff.Check()
	case awakeAuto:
		a.modeAuto.Check()
	case awakeAlways:
		a.modeAlways.Check()
	}
	saveAwakeMode(m)
	a.poll() // apply immediately
}

// fetchStatus GETs /api/status with a short timeout.
func fetchStatus(baseURL string) (*gateway.StatusResult, error) {
	client := &http.Client{Timeout: 2500 * time.Millisecond}
	resp, err := client.Get(baseURL + "/api/status")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var st gateway.StatusResult
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return nil, err
	}
	return &st, nil
}

// --- awake mode persistence (~/.goterm/tray.json) ---

func trayStatePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".goterm", "tray.json")
}

func loadAwakeMode() awakeMode {
	data, err := os.ReadFile(trayStatePath())
	if err != nil {
		return awakeOff
	}
	var s struct {
		AwakeMode string `json:"awake_mode"`
	}
	if json.Unmarshal(data, &s) != nil {
		return awakeOff
	}
	switch awakeMode(s.AwakeMode) {
	case awakeAuto, awakeAlways:
		return awakeMode(s.AwakeMode)
	}
	return awakeOff
}

func saveAwakeMode(m awakeMode) {
	path := trayStatePath()
	_ = os.MkdirAll(filepath.Dir(path), 0755)
	data, _ := json.Marshal(map[string]string{"awake_mode": string(m)})
	_ = os.WriteFile(path, data, 0644)
}

// --- helpers ---

func truncate(s string, max int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
