//go:build darwin || windows

// bomtray is a tray companion for the bomclaw gateways: a macOS menu bar item,
// or a Windows 11 notification-area icon. It polls each gateway's /api/status
// endpoint, shows live agent state, exposes quick actions, and can keep the
// machine awake while an agent is running.
//
// It is a separate binary on purpose: when a gateway dies, the tray icon
// survives and turns red instead of vanishing.
//
// How that state is shown differs per platform, and not just cosmetically:
// macOS puts a text title in the menu bar, whereas systray's SetTitle is a
// no-op on Windows, where the notification area has room for an icon and a
// tooltip and nothing else. Both sit behind setTrayPresentation — see
// state_darwin.go and state_windows.go.
//
// A machine normally runs more than one gateway (agent 1 on :18789, agent 2 on
// :18790), so the tray polls a list. An address that never answers is hidden
// rather than shown as down: someone running a single agent should see exactly
// one agent in the menu, not one plus a permanent error.
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
	"sync"
	"time"

	"fyne.io/systray"
	"github.com/ngocp/goterm-control/internal/gateway"
	"github.com/ngocp/goterm-control/internal/memory"
)

type awakeMode string

const (
	awakeOff    awakeMode = "off"
	awakeAuto   awakeMode = "auto" // hold only while an agent is running
	awakeAlways awakeMode = "always"
)

// trayState is everything the icon or the menu bar title has to convey at a
// glance. Each platform renders it its own way; see setTrayPresentation.
type trayState struct {
	down    bool // a gateway that answered before has stopped answering
	running bool // some agent has a run in flight
	awake   bool // a sleep-prevention hold is currently active
}

// downAfter is how many consecutive failed polls it takes to call an agent
// down, and therefore to notify about it.
//
// One is not enough. The status request has a 2 second timeout against a
// gateway that spawns CLI subprocesses heavy enough to starve it for that long,
// so a single miss is routine — and it used to fire "… is DOWN" immediately,
// followed by "… is back up" a few seconds later, over and over, about a
// gateway that never stopped serving. Three misses is roughly ten seconds of
// silence, which a gateway that is actually up does not produce.
const downAfter = 3

// defaultAgents is the standard local layout. Both are probed; whichever
// answers is shown.
var defaultAgents = []string{"http://127.0.0.1:18789", "http://127.0.0.1:18790"}

// agent is one gateway the tray watches, plus the menu rows that render it.
type agent struct {
	url string

	// Menu rows. Created once at startup — systray cannot grow a menu later —
	// and hidden until the gateway first answers.
	head    *systray.MenuItem // 🟢 name · uptime · model · sessions
	run     *systray.MenuItem // ⚡ current task, or 💤 Idle
	browser *systray.MenuItem // 🌐 the paired browser, or nothing paired

	mu        sync.Mutex
	st        *gateway.StatusResult
	up        bool
	fails     int  // consecutive failed polls; down is declared at downAfter
	seen      bool // has this address EVER answered? unseen agents stay hidden
	upKnown   bool
	wasUp     bool
	wasRun    bool
	prevTask  string
	workspace string
}

// name is what to call this agent in menus and notifications.
func (ag *agent) name() string {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	if ag.st != nil {
		if ag.st.AgentName != "" {
			return ag.st.AgentName
		}
		if ag.st.AgentID != "" {
			return ag.st.AgentID
		}
	}
	return shortURL(ag.url)
}

// serviceTarget names the service to restart for this agent. What a target
// looks like is platform-specific — a launchd label on macOS, a Task Scheduler
// path on Windows — so the shape is decided by serviceLabelFor.
//
// The tray used to carry a single hardcoded default that matched no installed
// service, so "Restart Gateway" silently did nothing; the id now comes from the
// gateway itself.
func (ag *agent) serviceTarget(override string) string {
	if override != "" {
		return override
	}
	ag.mu.Lock()
	id := ""
	if ag.st != nil {
		id = ag.st.AgentID
	}
	ag.mu.Unlock()
	return serviceLabelFor(id)
}

type app struct {
	agents   []*agent
	logPath  string
	label    string // optional override for every agent's launchd label
	interval time.Duration

	mem   *memory.Manager
	awake *awake
	mode  awakeMode

	memItem    *systray.MenuItem
	modeOff    *systray.MenuItem
	modeAuto   *systray.MenuItem
	modeAlways *systray.MenuItem
}

// urlList collects a repeatable flag.
type urlList []string

func (u *urlList) String() string     { return strings.Join(*u, ",") }
func (u *urlList) Set(v string) error { *u = append(*u, strings.TrimRight(v, "/")); return nil }

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
	var urls urlList
	flag.Var(&urls, "agent", "Gateway base URL; repeat for each agent (default: :18789 and :18790)")
	workspace := flag.String("workspace", filepath.Join(home, "goterm-workspace"), "Fallback workspace for memory stats when a gateway does not report one")
	label := flag.String("gateway-label", "", "Override the service to restart (default: derived from the agent id the gateway reports)")
	logPath := flag.String("log", filepath.Join(home, ".goterm/logs/gateway.err.log"), "Gateway log file for Tail Logs")
	interval := flag.Int("interval", 3, "Poll interval in seconds")
	flag.Parse()

	if len(urls) == 0 {
		urls = defaultAgents
	}

	a := &app{
		logPath:  *logPath,
		label:    *label,
		interval: time.Duration(*interval) * time.Second,
		awake:    &awake{},
		mode:     loadAwakeMode(),
		mem: memory.NewManager(memory.Config{
			Enabled: true,
			Dir:     *workspace,
		}),
	}
	for _, u := range urls {
		a.agents = append(a.agents, &agent{url: u})
	}

	systray.Run(a.onReady, a.onExit)
}

func (a *app) onReady() {
	// Windows shows nothing at all until an icon is set, so paint the resting
	// state before the first poll rather than after it.
	setTrayPresentation(trayState{}, "bomclaw gateways")

	// One block of rows per agent. Hidden until that gateway answers, so a
	// single-agent machine shows a single-agent menu.
	for _, ag := range a.agents {
		ag.head = systray.AddMenuItem("⏳ "+shortURL(ag.url), "Open this agent's dashboard")
		ag.run = systray.AddMenuItem("", "")
		ag.run.Disable()
		ag.browser = systray.AddMenuItem("", "")
		ag.browser.Disable()
		ag.head.Hide()
		ag.run.Hide()
		ag.browser.Hide()
	}

	systray.AddSeparator()
	a.memItem = systray.AddMenuItem("💾 Memory: ...", "Open MEMORY.md")

	systray.AddSeparator()
	awakeMenu := systray.AddMenuItem(awakeMenuLabel(), "Prevent system sleep (display may still sleep)")
	a.modeOff = awakeMenu.AddSubMenuItemCheckbox("Off", "", a.mode == awakeOff)
	a.modeAuto = awakeMenu.AddSubMenuItemCheckbox("While bot is running", "Hold a power assertion only during agent runs", a.mode == awakeAuto)
	a.modeAlways = awakeMenu.AddSubMenuItemCheckbox("Always", "", a.mode == awakeAlways)

	systray.AddSeparator()
	openWs := systray.AddMenuItem("Open Workspace", "")
	tailLogs := systray.AddMenuItem("Tail Logs", "Open Terminal tailing the gateway log")

	// Restart is per agent: restarting the wrong one of two gateways is a
	// confusing way to lose a conversation.
	restartMenu := systray.AddMenuItem("Restart Gateway", "")
	restartItems := make([]*systray.MenuItem, len(a.agents))
	for i, ag := range a.agents {
		restartItems[i] = restartMenu.AddSubMenuItem(shortURL(ag.url), "Restart this gateway's background service")
	}

	systray.AddSeparator()
	quit := systray.AddMenuItem("Quit bomtray", "")

	// Click handlers. Each agent's header opens its own dashboard.
	for _, ag := range a.agents {
		go func(ag *agent) {
			for range ag.head.ClickedCh {
				openPath(ag.url)
			}
		}(ag)
	}
	for i, ag := range a.agents {
		go func(i int, ag *agent) {
			for range restartItems[i].ClickedCh {
				target := ag.serviceTarget(a.label)
				if target == "" {
					notify("Bomclaw", "Cannot restart "+ag.name()+": its service name is unknown")
					continue
				}
				restartGateway(target)
			}
		}(i, ag)
	}

	go func() {
		for {
			select {
			case <-a.memItem.ClickedCh:
				openPath(filepath.Join(a.memDir(), "MEMORY.md"))
			case <-a.modeOff.ClickedCh:
				a.setMode(awakeOff)
			case <-a.modeAuto.ClickedCh:
				a.setMode(awakeAuto)
			case <-a.modeAlways.ClickedCh:
				a.setMode(awakeAlways)
			case <-openWs.ClickedCh:
				openPath(a.memDir())
			case <-tailLogs.ClickedCh:
				openTerminalTail(a.logPath)
			case <-quit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()

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

// memDir is the workspace memory stats are read from: the first agent that
// reports one, else the -workspace flag.
func (a *app) memDir() string {
	for _, ag := range a.agents {
		ag.mu.Lock()
		ws := ag.workspace
		ag.mu.Unlock()
		if ws != "" {
			return ws
		}
	}
	return a.mem.Dir()
}

// tooltip is the one-line summary of every agent that has ever answered.
//
// On Windows this is the only place detail can go: the notification area shows
// an icon and a tooltip and nothing else, so it has to carry what the macOS
// menu bar title does. Windows caps a tooltip at 128 UTF-16 units, which the
// Windows side trims to.
func (a *app) tooltip() string {
	parts := make([]string, 0, len(a.agents)+1)
	for _, ag := range a.agents {
		st, up, seen := ag.snapshot()
		if !seen {
			continue
		}
		switch {
		case !up:
			parts = append(parts, ag.name()+": down")
		case st != nil && len(st.Runs) > 0:
			parts = append(parts, ag.name()+": "+truncate(st.Runs[0].Task, 28))
		default:
			parts = append(parts, ag.name()+": idle")
		}
	}
	if len(parts) == 0 {
		return "bomclaw — no gateway answering"
	}
	if a.awake.Held() {
		parts = append(parts, "awake held")
	}
	return "bomclaw · " + strings.Join(parts, " · ")
}

// poll refreshes every agent in parallel, then updates the menu, the awake
// assertion and any transition notifications.
func (a *app) poll() {
	var wg sync.WaitGroup
	for _, ag := range a.agents {
		wg.Add(1)
		go func(ag *agent) {
			defer wg.Done()
			st, err := fetchStatus(ag.url)
			ag.mu.Lock()
			if err == nil {
				ag.st, ag.fails, ag.seen = st, 0, true
				if st.Workspace != "" {
					ag.workspace = st.Workspace
				}
			} else {
				ag.fails++
				// The last good status is kept on purpose. It carries the
				// agent's name, and a missed poll should not turn "BomClaw"
				// back into an IP address in the menu and the notification.
			}
			ag.up = ag.fails < downAfter
			ag.mu.Unlock()
		}(ag)
	}
	wg.Wait()

	anyRunning := false
	anyDown := false
	for _, ag := range a.agents {
		running := a.renderAgent(ag)
		anyRunning = anyRunning || running
		ag.mu.Lock()
		if ag.seen && !ag.up {
			anyDown = true
		}
		ag.mu.Unlock()
	}

	// --- awake assertion ---
	switch a.mode {
	case awakeAlways:
		a.holdAwake()
	case awakeAuto:
		if anyRunning {
			a.holdAwake()
		} else {
			a.awake.Release()
		}
	default:
		a.awake.Release()
	}

	// --- tray icon / title ---
	setTrayPresentation(trayState{
		down:    anyDown,
		running: anyRunning,
		awake:   a.awake.Held(),
	}, a.tooltip())

	if stats, err := a.mem.StatsIn(a.memDir(), time.Now()); err == nil {
		a.memItem.SetTitle(fmt.Sprintf("💾 Memory: %s · %d notes", humanBytes(stats.MemoryMDBytes), stats.DailyNoteCount))
	}
}

// renderAgent updates one agent's rows and fires its transition
// notifications. Returns whether it is running.
func (ag *agent) snapshot() (*gateway.StatusResult, bool, bool) {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	return ag.st, ag.up, ag.seen
}

func (a *app) renderAgent(ag *agent) bool {
	st, up, seen := ag.snapshot()
	if !seen {
		return false // never answered: stay hidden
	}
	ag.head.Show()
	ag.run.Show()

	running := up && st != nil && len(st.Runs) > 0
	name := ag.name()

	// --- transitions, named so two agents are distinguishable ---
	if ag.upKnown && up != ag.wasUp {
		if up {
			notify("Bomclaw", name+" is back up ✅")
		} else {
			notify("Bomclaw", name+" is DOWN ⛔")
		}
	}
	if ag.wasRun && !running && up {
		task := ag.prevTask
		if task == "" {
			task = "agent task"
		}
		notify("Bomclaw", "✅ "+name+": "+task)
	}
	ag.upKnown = true
	ag.wasUp = up
	ag.wasRun = running
	if running {
		ag.prevTask = st.Runs[0].Task
	}

	// --- header ---
	if !up {
		ag.head.SetTitle("🔴 " + name + " · down")
	} else {
		head := fmt.Sprintf("🟢 %s · %s · %s", name, st.Uptime, shortModel(st.DefaultModel))
		if st.ActiveSessions > 0 {
			head += fmt.Sprintf(" · %d sess", st.ActiveSessions)
		}
		ag.head.SetTitle(head)
	}

	// --- current run ---
	if running {
		r := st.Runs[0]
		line := fmt.Sprintf("   ⚡ %s · %d tools", truncate(r.Task, 34), r.ToolCount)
		if r.LastTool != "" {
			line += " · " + truncate(r.LastTool, 18)
		}
		ag.run.SetTitle(line)
	} else if up {
		ag.run.SetTitle("   💤 Idle")
	} else {
		ag.run.SetTitle("   —")
	}

	// --- browser bridge ---
	// Only shown when the gateway reports the bridge at all, so a build with
	// it disabled does not grow a permanently blank row.
	if up && st.Browser != nil {
		ag.browser.Show()
		b := st.Browser
		if b.Connected {
			line := "   🌐 " + orFirst(b.BrowserName, b.Browser, "browser")
			if b.Actions > 0 {
				line += fmt.Sprintf(" · %d actions", b.Actions)
			}
			if b.LastAction != "" {
				line += " · " + b.LastAction
			}
			ag.browser.SetTitle(line)
		} else {
			ag.browser.SetTitle("   🌐 no browser paired")
		}
	} else {
		ag.browser.Hide()
	}

	return running
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
		a.awake.Release()
	case awakeAuto:
		a.modeAuto.Check()
	case awakeAlways:
		a.modeAlways.Check()
		a.holdAwake()
	}
	saveAwakeMode(m)
}

func fetchStatus(baseURL string) (*gateway.StatusResult, error) {
	client := &http.Client{Timeout: 2 * time.Second}
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

func trayStatePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".goterm", "tray-state.json")
}

func loadAwakeMode() awakeMode {
	b, err := os.ReadFile(trayStatePath())
	if err != nil {
		return awakeOff
	}
	var s struct {
		Awake string `json:"awake"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return awakeOff
	}
	switch awakeMode(s.Awake) {
	case awakeAuto:
		return awakeAuto
	case awakeAlways:
		return awakeAlways
	}
	return awakeOff
}

func saveAwakeMode(m awakeMode) {
	path := trayStatePath()
	_ = os.MkdirAll(filepath.Dir(path), 0755)
	b, _ := json.Marshal(map[string]string{"awake": string(m)})
	if err := os.WriteFile(path, b, 0644); err != nil {
		log.Printf("bomtray: save state: %v", err)
	}
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// shortModel drops the vendor prefix so the header stays readable:
// "claude-opus-5" → "opus-5".
func shortModel(id string) string {
	for _, p := range []string{"claude-", "openai-"} {
		if strings.HasPrefix(id, p) {
			return strings.TrimPrefix(id, p)
		}
	}
	return id
}

// shortURL is the host:port, used before a gateway has told us its name.
func shortURL(u string) string {
	s := strings.TrimPrefix(strings.TrimPrefix(u, "http://"), "https://")
	return strings.TrimSuffix(s, "/")
}

func orFirst(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
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
