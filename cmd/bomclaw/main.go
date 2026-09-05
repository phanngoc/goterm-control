package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/websocket"

	"net"
	"path/filepath"
	"runtime"

	"github.com/ngocp/goterm-control/internal/agent"
	anthropicClient "github.com/ngocp/goterm-control/internal/anthropic"
	"github.com/ngocp/goterm-control/internal/auth"
	"github.com/ngocp/goterm-control/internal/bot"
	"github.com/ngocp/goterm-control/internal/channel"
	"github.com/ngocp/goterm-control/internal/claude"
	"github.com/ngocp/goterm-control/internal/codex"
	"github.com/ngocp/goterm-control/internal/config"
	agentctx "github.com/ngocp/goterm-control/internal/context"
	"github.com/ngocp/goterm-control/internal/coord"
	"github.com/ngocp/goterm-control/internal/daemon"
	"github.com/ngocp/goterm-control/internal/gateway"
	"github.com/ngocp/goterm-control/internal/models"
	"github.com/ngocp/goterm-control/internal/session"
	"github.com/ngocp/goterm-control/internal/storage"
	"github.com/ngocp/goterm-control/internal/taskrunner"
	"github.com/ngocp/goterm-control/internal/tools"
	"github.com/ngocp/goterm-control/internal/trace"
)

// loadEnv reads KEY=VALUE pairs from a .env file into the process environment.
func loadEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "gateway":
		// Check for gateway subcommands (install, uninstall, start, stop, restart, status)
		if len(os.Args) > 2 {
			switch os.Args[2] {
			case "install":
				runGatewayInstall(os.Args[3:])
				return
			case "uninstall":
				runGatewayUninstall(os.Args[3:])
				return
			case "start":
				runGatewayStart(os.Args[3:])
				return
			case "stop":
				runGatewayStop(os.Args[3:])
				return
			case "restart":
				runGatewayRestart(os.Args[3:])
				return
			case "status":
				runGatewayServiceStatus(os.Args[3:])
				return
			}
		}
		runGateway(os.Args[2:])
	case "passwd":
		runPasswdCmd(os.Args[2:])
	case "send":
		runSend(os.Args[2:])
	case "status":
		runStatus(os.Args[2:])
	case "task":
		runTask(os.Args[2:])
	case "note":
		runNote(os.Args[2:])
	case "inbox":
		runInbox(os.Args[2:])
	case "msg":
		runMsg(os.Args[2:])
	case "agents":
		runAgents(os.Args[2:])
	case "models":
		runModels(os.Args[2:])
	case "chat":
		runChat(os.Args[2:])
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`BomClaw — lean AI agent for remote host control

Usage:
  bomclaw <command> [flags]

Commands:
  gateway            Start the gateway in foreground
  gateway install    Install as a background service (systemd/launchd)
  gateway uninstall  Remove the background service
  gateway start      Start the installed service
  gateway stop       Stop the installed service
  gateway restart    Restart the installed service
  gateway status     Show service status and health
  send               Send a message to the agent via gateway
  status             Show gateway status (via WebSocket)
  models             List available models
  chat               Interactive CLI chat with the agent (no gateway needed)
  agents             List agents registered in the shared database
  task               Create, claim and finish work shared between agents
  note               Record and search what the agents have learned
  inbox              Read messages other agents sent to this one
  msg                Send a message to another agent
  passwd             Set the dashboard password (creates the account if none)
  help               Show this help`)
}

// --- gateway command ---

// resolveDashboardDir finds the built dashboard. Under launchd the process
// cwd is "/" (no WorkingDirectory in the plist), so a cwd-relative path 404s;
// fall back to the directory the binary lives in (the project root).
func resolveDashboardDir() string {
	const rel = "dashboard/dist"
	if _, err := os.Stat(rel); err == nil {
		return rel
	}
	if exe, err := os.Executable(); err == nil {
		exe, _ = filepath.EvalSymlinks(exe)
		dir := filepath.Join(filepath.Dir(exe), rel)
		if _, err := os.Stat(dir); err == nil {
			return dir
		}
	}
	log.Printf("gateway: dashboard assets not found (%s) — dashboard disabled", rel)
	return ""
}

func runGateway(args []string) {
	fs := flag.NewFlagSet("gateway", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "Path to config file")
	envPath := fs.String("env", ".env", "Path to .env file")
	bind := fs.String("bind", "127.0.0.1", "Bind address")
	port := fs.Int("port", 18789, "Gateway port")
	fs.Parse(args)

	loadEnv(*envPath)

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("shutting down...")
		cancel()
	}()

	// Every CLI subprocess this gateway spawns inherits this, so when the agent
	// runs `bomclaw task`/`note`/`msg` from its shell it is identified as
	// itself. Without it the CLI fell back to a default and agent 2's work was
	// silently attributed to agent 1.
	if err := os.Setenv("BOMCLAW_AGENT_ID", cfg.Agent.ID); err != nil {
		log.Printf("gateway: could not export BOMCLAW_AGENT_ID: %v", err)
	}

	// Create model provider: codex CLI, claude CLI (OAuth), or direct API key.
	provider := buildProvider(cfg)

	// Recorder for the gateway's own command path (dashboard, `bomclaw send`,
	// a peer agent). The bot keeps its own for the Telegram path; both write
	// to the same shared database.
	var gwTrace *trace.Recorder

	// Model resolver
	resolver := models.NewResolver(cfg.Models.Default, cfg.Models.Custom)

	// Storage — SQLite database
	db, err := storage.Open(filepath.Join(cfg.Session.DataDir, "goterm.db"))
	if err != nil {
		log.Fatalf("storage: %v", err)
	}
	defer db.Close()

	// Session manager (SQLite-backed)
	sessions := session.NewManager(storage.NewSessionStore(db))

	// Shared coordination database — traces, tasks and inter-agent messages,
	// written by every agent on this machine into one file. A failure here is
	// not fatal: the agent still answers, it just becomes invisible to the
	// admin page and cannot hand work to its peer.
	var coordDB *coord.DB
	if cfg.Coord.IsEnabled() {
		coordDB, err = coord.Open(cfg.Coord.Path)
		if err != nil {
			log.Printf("coord: disabled — %v", err)
			coordDB = nil
		} else {
			defer coordDB.Close()
			log.Printf("coord: shared database at %s (agent=%s)", coordDB.Path(), cfg.Agent.ID)
			startCoordUpkeep(ctx, coordDB, cfg, *bind, *port, resolver.Default())
			gwTrace = trace.New(coordDB, cfg.Agent.ID)
			defer gwTrace.Close()
		}
	}

	// Dashboard auth (username/password). When enabled, /ws and /api/* need
	// a login session — required before exposing the gateway via a tunnel.
	authMgr := auth.NewManager(auth.Config{
		Enabled:    cfg.Gateway.Auth.Enabled,
		PublicHost: cfg.Gateway.Auth.PublicHost,
		SessionTTL: time.Duration(cfg.Gateway.Auth.SessionTTLHours) * time.Hour,
	}, storage.NewUserStore(db))
	if cfg.Gateway.Auth.Enabled {
		log.Printf("gateway: dashboard auth enabled (public_host=%s)", cfg.Gateway.Auth.PublicHost)
	} else {
		log.Printf("gateway: dashboard auth DISABLED — do not expose this port publicly")
	}

	// Gateway RPC server
	addr := fmt.Sprintf("%s:%d", *bind, *port)
	startTime := time.Now()

	// Create the Telegram bot first so the gateway status RPC can report its
	// live run state (menu bar tray polls /api/status).
	var tgBot *bot.Bot
	if cfg.Telegram.Token != "" {
		// Share db + session manager with the gateway so session renames and
		// turn counters show up in the dashboard without a reload.
		tgBot, err = bot.New(cfg, db, coordDB, sessions)
		if err != nil {
			log.Printf("gateway: telegram bot init failed: %v", err)
			tgBot = nil
		}
	}

	// Task runner — claims work a peer queued and executes it with the same
	// backend that answers chat. Off unless the config asks for it: a claimed
	// task runs with full machine access and nobody is watching.
	var runner *taskrunner.Runner
	if coordDB != nil && cfg.Tasks.AutoClaim {
		runner = taskrunner.New(coordDB, bot.NewChatClient(cfg, nil), gwTrace, taskrunner.Config{
			AgentID:  cfg.Agent.ID,
			Model:    resolver.Default(),
			Interval: time.Duration(cfg.Tasks.PollIntervalSeconds) * time.Second,
			Timeout:  time.Duration(cfg.Tasks.TimeoutMinutes) * time.Minute,
		})
		runner.Start(ctx)
	} else if coordDB != nil {
		log.Printf("taskrunner: disabled (tasks.auto_claim=false) — queued work waits for `bomclaw task claim`")
	}

	deps := gateway.Deps{
		Sessions:     sessions,
		Resolver:     resolver,
		Provider:     provider,
		System:       cfg.Claude.SystemPrompt,
		DataDir:      cfg.Session.DataDir,
		Uptime:       func() time.Duration { return time.Since(startTime) },
		Coord:        coordDB,
		AgentID:      cfg.Agent.ID,
		ProviderName: cfg.Provider,
		Trace:        gwTrace,
		PokeTasks:    runner.Poke,
		NotesFile:    cfg.Coord.NotesFile,
	}
	if tgBot != nil {
		deps.Runs = func() []gateway.RunInfo {
			var out []gateway.RunInfo
			for _, s := range tgBot.Sessions().List() {
				info := s.RunInfo()
				if !info.Running {
					continue
				}
				out = append(out, gateway.RunInfo{
					ChatID:    s.ChatID,
					SessionID: s.ID,
					Label:     s.GetLabel(),
					Task:      info.CurrentTask,
					LastTool:  info.LastTool,
					ToolCount: info.ToolCount,
					StartedAt: info.StartedAt.Format(time.RFC3339),
				})
			}
			return out
		}
	}

	srv := gateway.NewServer(addr, gateway.NewMethodHandler(deps), gateway.NewStreamSendHandler(deps), resolveDashboardDir(), authMgr)

	// Start Telegram bot polling in background
	if tgBot != nil {
		go func() {
			log.Println("gateway: starting Telegram bot")
			tgBot.Run()
		}()
		defer tgBot.Shutdown()
	}

	// Kill any stale process holding our port (prevents "address already in use"
	// when launchd/systemd auto-restarts after a crash).
	if err := daemon.KillStaleListeners(*port); err != nil {
		log.Printf("warning: stale PID cleanup: %v", err)
	}

	log.Printf("bomclaw gateway starting on %s", addr)
	if err := srv.Start(ctx); err != nil {
		log.Fatalf("gateway: %v", err)
	}

	sessions.SaveNow()
	log.Println("bomclaw: shutdown complete")
}

// --- send command ---

func runSend(args []string) {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	addr := fs.String("addr", "ws://127.0.0.1:18789/ws", "Gateway WebSocket address")
	modelID := fs.String("model", "", "Model override")
	fs.Parse(args)

	message := strings.Join(fs.Args(), " ")
	if message == "" {
		fmt.Fprintln(os.Stderr, "Usage: bomclaw send [--model <model>] <message>")
		os.Exit(1)
	}

	ws, _, err := websocket.DefaultDialer.Dial(*addr, nil)
	if err != nil {
		log.Fatalf("connect: %v (is the gateway running?)", err)
	}
	defer ws.Close()

	params := gateway.SendParams{Message: message, ModelID: *modelID}
	paramsJSON, _ := json.Marshal(params)

	req := gateway.Request{ID: "1", Method: "send", Params: paramsJSON}
	if err := ws.WriteJSON(req); err != nil {
		log.Fatalf("send: %v", err)
	}

	var resp gateway.Response
	if err := ws.ReadJSON(&resp); err != nil {
		log.Fatalf("receive: %v", err)
	}

	if resp.Error != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", resp.Error.Message)
		os.Exit(1)
	}

	var result map[string]any
	json.Unmarshal(resp.Result, &result)
	if text, ok := result["text"].(string); ok {
		fmt.Println(text)
	}
}

// --- status command ---

func runStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	addr := fs.String("addr", "ws://127.0.0.1:18789/ws", "Gateway WebSocket address")
	fs.Parse(args)

	ws, _, err := websocket.DefaultDialer.Dial(*addr, nil)
	if err != nil {
		fmt.Println("Gateway: offline")
		os.Exit(1)
	}
	defer ws.Close()

	req := gateway.Request{ID: "1", Method: "status"}
	ws.WriteJSON(req)

	var resp gateway.Response
	ws.ReadJSON(&resp)

	if resp.Error != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", resp.Error.Message)
		os.Exit(1)
	}

	var status gateway.StatusResult
	json.Unmarshal(resp.Result, &status)
	fmt.Printf("Gateway: running\nUptime: %s\nDefault model: %s\nSessions: %d\nChannels: %s\n",
		status.Uptime, status.DefaultModel, status.ActiveSessions,
		strings.Join(status.Channels, ", "))
}

// --- models command ---

func runModels(_ []string) {
	resolver := models.NewResolver("claude-sonnet-4-6", nil)
	for _, m := range resolver.List() {
		fmt.Println(models.FormatModelInfo(&m, m.ID == resolver.Default()))
	}
}

// heartbeatInterval must be well under coord.StaleAfter so a healthy agent is
// never briefly shown as offline.
const heartbeatInterval = 30 * time.Second

// startCoordUpkeep registers this agent in the shared database and keeps it
// current: a heartbeat so peers and the admin page can tell it is alive, and a
// daily purge so trace rows do not grow without bound.
func startCoordUpkeep(ctx context.Context, cdb *coord.DB, cfg *config.Config, bind string, port int, model string) {
	wsAddr := cfg.Agent.WSAddr
	if wsAddr == "" {
		wsAddr = fmt.Sprintf("ws://%s:%d/ws", bind, port)
	}
	// Two gateways sharing an agent id would overwrite each other's row and
	// mis-address every task and message. It cannot be prevented from here —
	// the id is config — but a live peer already holding it is worth shouting
	// about, because the symptom (work vanishing) is otherwise baffling.
	if existing, err := cdb.ListAgents(); err == nil {
		for _, a := range existing {
			if a.ID == cfg.Agent.ID && a.Online && a.WSAddr != "" && a.WSAddr != wsAddr {
				log.Printf("coord: WARNING agent id %q is already held by a live gateway at %s — "+
					"set a distinct agent.id in this config or the two will overwrite each other",
					cfg.Agent.ID, a.WSAddr)
			}
		}
	}

	if err := cdb.RegisterAgent(coord.Agent{
		ID:          cfg.Agent.ID,
		DisplayName: cfg.Agent.Name,
		Provider:    cfg.Provider,
		Model:       model,
		WSAddr:      wsAddr,
		Workspace:   cfg.Claude.Workspace,
	}); err != nil {
		log.Printf("coord: register agent: %v", err)
	}

	go func() {
		ticker := time.NewTicker(heartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := cdb.Heartbeat(cfg.Agent.ID); err != nil {
					log.Printf("coord: heartbeat: %v", err)
				}
			}
		}
	}()

	if days := cfg.Coord.TraceRetentionDays; days > 0 {
		go func() {
			purge := func() {
				n, err := cdb.PurgeRunsBefore(time.Now().AddDate(0, 0, -days))
				if err != nil {
					log.Printf("coord: purge traces: %v", err)
				} else if n > 0 {
					log.Printf("coord: purged %d trace rows older than %d days", n, days)
				}
			}
			purge()
			ticker := time.NewTicker(6 * time.Hour)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					purge()
				}
			}
		}()
	}
}

// buildProvider picks the model backend for one-shot text calls, following
// cfg.Provider first and then the shape of the Anthropic credential.
func buildProvider(cfg *config.Config) agent.ModelProvider {
	if cfg.Provider == config.ProviderCodex {
		log.Println("gateway: using Codex CLI provider")
		return codex.NewCLIProvider(cfg.Claude.Workspace)
	}
	if strings.HasPrefix(cfg.Claude.APIKey, "sk-ant-oat") {
		// OAuth subscription token — must use claude CLI subprocess
		log.Println("gateway: using Claude CLI provider (OAuth token detected)")
		return claude.NewCLIProvider(cfg.Claude.Workspace)
	}
	// Direct API key (sk-ant-api03-...)
	log.Println("gateway: using direct Anthropic API provider")
	return anthropicClient.New(cfg.Claude.APIKey)
}

// --- chat command (direct, no gateway) ---

func runChat(args []string) {
	fs := flag.NewFlagSet("chat", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "Path to config file")
	envPath := fs.String("env", ".env", "Path to .env file")
	modelOverride := fs.String("model", "", "Model override")
	fs.Parse(args)

	loadEnv(*envPath)

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	chatProvider := buildProvider(cfg)
	resolver := models.NewResolver(cfg.Models.Default, cfg.Models.Custom)

	modelID := resolver.Default()
	if *modelOverride != "" {
		if m := resolver.Lookup(*modelOverride); m != nil {
			modelID = m.ID
		}
	}

	m := resolver.Lookup(modelID)
	maxTokens := 8192
	contextWindow := 200000
	if m != nil {
		maxTokens = m.MaxTokens
		contextWindow = m.ContextWindow
	}

	executor := tools.New(tools.ExecutorConfig{
		ShellTimeout:   cfg.Tools.ShellTimeout,
		MaxOutputBytes: cfg.Tools.MaxOutputBytes,
	})

	// Build tool definitions from executor
	toolDefs := buildToolDefs()

	ctxEngine := agentctx.NewEngine(contextWindow)

	fmt.Printf("BomClaw Chat — model: %s\nType /quit to exit\n\n", modelID)

	cli := channel.NewCLI()
	cli.Start(context.Background(), func(ctx context.Context, msg channel.InboundMessage) *channel.OutboundMessage {
		if msg.Command == "quit" || msg.Command == "exit" {
			os.Exit(0)
		}
		if msg.Command == "reset" {
			ctxEngine.Clear()
			return &channel.OutboundMessage{Text: "Session cleared."}
		}

		userText := msg.Text
		if userText == "" {
			userText = "/" + msg.Command + " " + msg.Args
		}

		result, err := agent.RunAgent(ctx, agent.RunParams{
			Provider:     chatProvider,
			ToolExecutor: &toolAdapter{executor: executor},
			ModelID:      modelID,
			SystemPrompt: cfg.Claude.SystemPrompt,
			UserMessage:  userText,
			Messages:     ctxEngine.Messages(),
			Tools:        toolDefs,
			MaxTokens:    maxTokens,
			OnText:       func(text string) { fmt.Print(text) },
			OnToolCall:   func(name, input string) { fmt.Printf("\n🔧 %s\n", name) },
			OnToolResult: func(name, output string, isErr bool) {
				if isErr {
					fmt.Printf("❌ %s error\n", name)
				}
			},
		})

		if err != nil {
			return &channel.OutboundMessage{Text: fmt.Sprintf("Error: %v", err)}
		}

		// Update context engine with new messages
		ctxEngine.SetMessages(result.Messages)

		fmt.Println() // newline after streaming
		return nil    // already printed via OnText
	})
}

// toolAdapter bridges tools.Executor to agent.ToolExecutor interface.
type toolAdapter struct {
	executor *tools.Executor
}

func (a *toolAdapter) Execute(ctx context.Context, name string, input json.RawMessage) agent.ToolResult {
	r := a.executor.Run(ctx, name, input)
	return agent.ToolResult{Content: r.Output, IsError: r.IsError}
}

// buildToolDefs creates agent.ToolDef from the tool names we support.
func buildToolDefs() []agent.ToolDef {
	names := []struct{ name, desc string }{
		{"run_shell", "Execute a shell command"},
		{"read_file", "Read file contents"},
		{"write_file", "Write file contents"},
		{"list_dir", "List directory"},
		{"search_files", "Search files by name or content"},
		{"take_screenshot", "Take screenshot"},
		{"get_clipboard", "Get clipboard contents"},
		{"set_clipboard", "Set clipboard contents"},
		{"run_applescript", "Run AppleScript"},
		{"open_app", "Open application"},
		{"get_system_info", "Get system information"},
		{"list_processes", "List running processes"},
		{"kill_process", "Kill a process"},
		{"browse_url", "Open URL in browser"},
		// Browser automation (native CDP)
		{"browser_navigate", "Navigate browser to a URL. Launches Chrome with CDP if needed."},
		{"browser_snapshot", "Get a DOM snapshot of the current page with element refs (n1, n2, etc). Always snapshot first to get refs."},
		{"browser_click", "Click an element by its ref (e.g. n3). Always snapshot first to get refs."},
		{"browser_fill", "Clear and type text into an input field by ref (e.g. n5). Use for form fields."},
		{"browser_type", "Append text to an input field by ref (does not clear first)."},
		{"browser_select", "Select a dropdown option by ref (e.g. n7) and value."},
		{"browser_scroll", "Scroll the page in a direction (up/down/left/right)."},
		{"browser_screenshot", "Take a screenshot of the current browser page."},
		{"browser_get_text", "Get text, HTML, value, title, or URL from an element or page."},
		{"browser_eval", "Execute JavaScript code in the browser and return the result."},
		{"browser_wait", "Wait for an element ref to appear, text to be visible, or a number of milliseconds."},
		{"browser_back", "Navigate back in browser history."},
	}

	// Import tool schemas from claude package tools
	var defs []agent.ToolDef
	for _, n := range names {
		schema := findToolSchema(n.name)
		defs = append(defs, agent.ToolDef{
			Name:        n.name,
			Description: n.desc,
			InputSchema: schema,
		})
	}
	return defs
}

// --- gateway service commands ---

func resolveAbsPath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}

func resolveBinaryPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve executable: %w", err)
	}
	real, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return exe, nil
	}
	// Warn if binary is in a temp directory (e.g. go run)
	if strings.Contains(real, "/tmp/") || strings.Contains(real, "/temp/") {
		fmt.Fprintln(os.Stderr, "Warning: binary is in a temp directory — install from a stable path")
	}
	return real, nil
}

func buildInstallEnv(configPath, envPath string) map[string]string {
	env := map[string]string{}

	home, err := os.UserHomeDir()
	if err == nil {
		env["HOME"] = home
	}

	// Load .env to capture API keys
	if envPath != "" {
		loadEnv(envPath)
	}
	// Load config to capture API keys
	if configPath != "" {
		cfg, err := config.Load(configPath)
		if err == nil {
			if cfg.Claude.APIKey != "" {
				env["ANTHROPIC_API_KEY"] = cfg.Claude.APIKey
			}
			if cfg.Telegram.Token != "" {
				env["TELEGRAM_TOKEN"] = cfg.Telegram.Token
			}
		}
	}

	// Also pick up from current env (env overrides config)
	if v := os.Getenv("ANTHROPIC_API_KEY"); v != "" {
		env["ANTHROPIC_API_KEY"] = v
	}
	if v := os.Getenv("TELEGRAM_TOKEN"); v != "" {
		env["TELEGRAM_TOKEN"] = v
	}

	// Inherit PATH so LaunchAgent can find executables like "claude", "node"
	if v := os.Getenv("PATH"); v != "" {
		env["PATH"] = v
	}

	return env
}

func runGatewayInstall(args []string) {
	fs := flag.NewFlagSet("gateway install", flag.ExitOnError)
	port := fs.Int("port", 18789, "Gateway port")
	bind := fs.String("bind", "127.0.0.1", "Bind address")
	configPath := fs.String("config", "config.yaml", "Path to config file")
	envPath := fs.String("env", ".env", "Path to .env file")
	force := fs.Bool("force", false, "Force reinstall even if already installed")
	fs.Parse(args)

	svc, err := daemon.Resolve()
	if err != nil {
		log.Fatalf("daemon: %v", err)
	}

	// Check if already installed
	if !*force {
		installed, _ := svc.IsInstalled()
		if installed {
			fmt.Printf("Service already installed (%s). Use --force to reinstall.\n", svc.Label())
			fmt.Printf("Unit: %s\n", svc.UnitPath())
			return
		}
	}

	binPath, err := resolveBinaryPath()
	if err != nil {
		log.Fatalf("daemon: %v", err)
	}

	absConfig := resolveAbsPath(*configPath)
	absEnv := resolveAbsPath(*envPath)

	installArgs := daemon.InstallArgs{
		BinaryPath:  binPath,
		Port:        *port,
		Bind:        *bind,
		ConfigPath:  absConfig,
		EnvFile:     absEnv,
		Environment: buildInstallEnv(absConfig, absEnv),
		Description: "BomClaw Gateway",
		Force:       *force,
	}

	ctx := context.Background()
	if err := svc.Install(ctx, installArgs); err != nil {
		log.Fatalf("install failed: %v", err)
	}

	fmt.Printf("Service installed via %s\n", svc.Label())
	fmt.Printf("Unit: %s\n", svc.UnitPath())

	// Health check
	fmt.Print("Waiting for gateway to become healthy...")
	result, err := daemon.WaitForHealthy(ctx, *port, *bind, daemon.HealthOpts{})
	if err != nil {
		fmt.Printf(" timeout\n")
		fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
		fmt.Fprintf(os.Stderr, "Check logs: journalctl --user -u %s\n", "bomclaw-gateway")
	} else {
		fmt.Printf(" ok (%s, %d attempts)\n", result.Elapsed.Round(time.Millisecond), result.Attempts)
	}

	// Linger check (Linux only)
	if runtime.GOOS == "linux" {
		daemon.PromptLinger()
	}

	fmt.Println()
	fmt.Println("Manage with:")
	fmt.Println("  bomclaw gateway status   — check health")
	fmt.Println("  bomclaw gateway restart   — restart service")
	fmt.Println("  bomclaw gateway stop      — stop service")
	fmt.Println("  bomclaw gateway uninstall — remove service")
}

func runGatewayUninstall(_ []string) {
	svc, err := daemon.Resolve()
	if err != nil {
		log.Fatalf("daemon: %v", err)
	}

	if err := svc.Uninstall(context.Background()); err != nil {
		log.Fatalf("uninstall failed: %v", err)
	}
	fmt.Println("Service uninstalled.")
}

func runGatewayStart(_ []string) {
	svc, err := daemon.Resolve()
	if err != nil {
		log.Fatalf("daemon: %v", err)
	}

	installed, _ := svc.IsInstalled()
	if !installed {
		fmt.Fprintln(os.Stderr, "Service not installed. Run: bomclaw gateway install")
		os.Exit(1)
	}

	if err := svc.Start(context.Background()); err != nil {
		log.Fatalf("start failed: %v", err)
	}
	fmt.Println("Service started.")
}

func runGatewayStop(_ []string) {
	svc, err := daemon.Resolve()
	if err != nil {
		log.Fatalf("daemon: %v", err)
	}

	if err := svc.Stop(context.Background()); err != nil {
		log.Fatalf("stop failed: %v", err)
	}
	fmt.Println("Service stopped.")
}

func runGatewayRestart(args []string) {
	fs := flag.NewFlagSet("gateway restart", flag.ExitOnError)
	port := fs.Int("port", 18789, "Gateway port (for health check)")
	bind := fs.String("bind", "127.0.0.1", "Bind address (for health check)")
	fs.Parse(args)

	svc, err := daemon.Resolve()
	if err != nil {
		log.Fatalf("daemon: %v", err)
	}

	installed, _ := svc.IsInstalled()
	if !installed {
		fmt.Fprintln(os.Stderr, "Service not installed. Run: bomclaw gateway install")
		os.Exit(1)
	}

	if err := svc.Restart(context.Background()); err != nil {
		log.Fatalf("restart failed: %v", err)
	}

	fmt.Print("Waiting for gateway to become healthy...")
	ctx := context.Background()
	result, err := daemon.WaitForHealthy(ctx, *port, *bind, daemon.HealthOpts{})
	if err != nil {
		fmt.Printf(" timeout\n")
		fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
	} else {
		fmt.Printf(" ok (%s)\n", result.Elapsed.Round(time.Millisecond))
	}
}

func runGatewayServiceStatus(args []string) {
	fs := flag.NewFlagSet("gateway status", flag.ExitOnError)
	port := fs.Int("port", 18789, "Gateway port (for health probe)")
	bind := fs.String("bind", "127.0.0.1", "Bind address")
	fs.Parse(args)

	svc, err := daemon.Resolve()
	if err != nil {
		log.Fatalf("daemon: %v", err)
	}

	// Service installation check
	installed, _ := svc.IsInstalled()
	if !installed {
		fmt.Printf("Service:   not installed\n")
		fmt.Printf("Backend:   %s\n", svc.Label())
		return
	}

	fmt.Printf("Service:   installed (%s)\n", svc.Label())
	fmt.Printf("Unit:      %s\n", svc.UnitPath())

	// Runtime status
	rt, err := svc.ReadRuntime()
	if err != nil {
		fmt.Printf("Status:    error (%v)\n", err)
	} else {
		fmt.Printf("Status:    %s", rt.Status)
		if rt.SubState != "" && rt.SubState != rt.Status {
			fmt.Printf(" (%s)", rt.SubState)
		}
		fmt.Println()
		if rt.PID > 0 {
			fmt.Printf("PID:       %d\n", rt.PID)
		}
		if rt.Status == "failed" {
			fmt.Printf("Exit:      %d (%s)\n", rt.ExitCode, rt.ExitReason)
		}
	}

	// HTTP health probe
	addr := net.JoinHostPort(*bind, fmt.Sprintf("%d", *port))
	fmt.Printf("Endpoint:  http://%s/health\n", addr)

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		fmt.Printf("Health:    unreachable\n")
	} else {
		conn.Close()
		fmt.Printf("Health:    port open\n")
	}
}

func findToolSchema(name string) map[string]any {
	// Hardcoded minimal schemas — the real schemas are in claude/tools.go
	// but those use SDK types. Here we use plain maps for the direct API.
	schemas := map[string]map[string]any{
		"run_shell": {"type": "object", "properties": map[string]any{
			"command":     map[string]any{"type": "string", "description": "Shell command to execute"},
			"working_dir": map[string]any{"type": "string", "description": "Working directory"},
			"timeout":     map[string]any{"type": "integer", "description": "Timeout in seconds"},
		}, "required": []string{"command"}},
		"read_file": {"type": "object", "properties": map[string]any{
			"path":   map[string]any{"type": "string", "description": "File path"},
			"offset": map[string]any{"type": "integer", "description": "Start line"},
			"limit":  map[string]any{"type": "integer", "description": "Number of lines"},
		}, "required": []string{"path"}},
		"write_file": {"type": "object", "properties": map[string]any{
			"path":    map[string]any{"type": "string", "description": "File path"},
			"content": map[string]any{"type": "string", "description": "Content"},
			"append":  map[string]any{"type": "boolean", "description": "Append mode"},
		}, "required": []string{"path", "content"}},
		"list_dir": {"type": "object", "properties": map[string]any{
			"path":        map[string]any{"type": "string", "description": "Directory path"},
			"recursive":   map[string]any{"type": "boolean", "description": "Recursive listing"},
			"show_hidden": map[string]any{"type": "boolean", "description": "Show hidden files"},
		}},
		"search_files": {"type": "object", "properties": map[string]any{
			"path":    map[string]any{"type": "string", "description": "Search directory"},
			"pattern": map[string]any{"type": "string", "description": "Search pattern"},
		}, "required": []string{"pattern"}},
		"take_screenshot": {"type": "object", "properties": map[string]any{}},
		"get_clipboard":   {"type": "object", "properties": map[string]any{}},
		"set_clipboard": {"type": "object", "properties": map[string]any{
			"text": map[string]any{"type": "string", "description": "Text to copy"},
		}, "required": []string{"text"}},
		"run_applescript": {"type": "object", "properties": map[string]any{
			"script": map[string]any{"type": "string", "description": "AppleScript code"},
		}, "required": []string{"script"}},
		"open_app": {"type": "object", "properties": map[string]any{
			"name": map[string]any{"type": "string", "description": "App name or path"},
		}, "required": []string{"name"}},
		"get_system_info": {"type": "object", "properties": map[string]any{}},
		"list_processes": {"type": "object", "properties": map[string]any{
			"filter":  map[string]any{"type": "string", "description": "Filter by name"},
			"sort_by": map[string]any{"type": "string", "description": "Sort by: cpu, memory, pid"},
		}},
		"kill_process": {"type": "object", "properties": map[string]any{
			"pid":    map[string]any{"type": "integer", "description": "Process ID"},
			"name":   map[string]any{"type": "string", "description": "Process name"},
			"signal": map[string]any{"type": "string", "description": "TERM or KILL"},
		}},
		"browse_url": {"type": "object", "properties": map[string]any{
			"url": map[string]any{"type": "string", "description": "URL to open"},
		}, "required": []string{"url"}},

		// Browser automation tools (native CDP)
		"browser_navigate": {"type": "object", "properties": map[string]any{
			"url": map[string]any{"type": "string", "description": "URL to navigate to"},
		}, "required": []string{"url"}},
		"browser_snapshot": {"type": "object", "properties": map[string]any{
			"selector":    map[string]any{"type": "string", "description": "CSS selector to scope the snapshot"},
			"interactive": map[string]any{"type": "boolean", "description": "Show only interactive elements (default true)"},
		}},
		"browser_click": {"type": "object", "properties": map[string]any{
			"ref":     map[string]any{"type": "string", "description": "Element ref from snapshot (e.g. n3)"},
			"new_tab": map[string]any{"type": "boolean", "description": "Open in new tab"},
		}, "required": []string{"ref"}},
		"browser_fill": {"type": "object", "properties": map[string]any{
			"ref":  map[string]any{"type": "string", "description": "Element ref for the input field"},
			"text": map[string]any{"type": "string", "description": "Text to fill (clears field first)"},
		}, "required": []string{"ref", "text"}},
		"browser_type": {"type": "object", "properties": map[string]any{
			"ref":  map[string]any{"type": "string", "description": "Element ref for the input field"},
			"text": map[string]any{"type": "string", "description": "Text to type (appends, does not clear)"},
		}, "required": []string{"ref", "text"}},
		"browser_select": {"type": "object", "properties": map[string]any{
			"ref":   map[string]any{"type": "string", "description": "Element ref for the dropdown"},
			"value": map[string]any{"type": "string", "description": "Value to select"},
		}, "required": []string{"ref", "value"}},
		"browser_scroll": {"type": "object", "properties": map[string]any{
			"direction": map[string]any{"type": "string", "description": "Scroll direction: up, down, left, right (default: down)"},
			"pixels":    map[string]any{"type": "integer", "description": "Pixels to scroll (default: 300)"},
		}},
		"browser_screenshot": {"type": "object", "properties": map[string]any{
			"path": map[string]any{"type": "string", "description": "Output file path (default: /tmp/browser-screenshot.png)"},
		}},
		"browser_get_text": {"type": "object", "properties": map[string]any{
			"ref":      map[string]any{"type": "string", "description": "Element ref to get text from (omit for full page)"},
			"property": map[string]any{"type": "string", "description": "Property: text, html, value, title, url (default: text)"},
		}},
		"browser_eval": {"type": "object", "properties": map[string]any{
			"expression": map[string]any{"type": "string", "description": "JavaScript expression to evaluate"},
		}, "required": []string{"expression"}},
		"browser_wait": {"type": "object", "properties": map[string]any{
			"ref":  map[string]any{"type": "string", "description": "Element ref to wait for"},
			"text": map[string]any{"type": "string", "description": "Text to wait for on page"},
			"ms":   map[string]any{"type": "integer", "description": "Milliseconds to wait"},
		}},
		"browser_back": {"type": "object", "properties": map[string]any{}},
	}
	if s, ok := schemas[name]; ok {
		return s
	}
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
