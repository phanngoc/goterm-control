package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ngocp/goterm-control/internal/auth"
	"github.com/ngocp/goterm-control/internal/config"
	"github.com/ngocp/goterm-control/internal/coord"
	"github.com/ngocp/goterm-control/internal/daemon"
	"github.com/ngocp/goterm-control/internal/gateway"
	"github.com/ngocp/goterm-control/internal/storage"
)

// --- dashboard command ---
//
// The web dashboard, served by a process that is not an agent. It used to be
// part of agent 1's gateway, so restarting agent 1 — a deploy, a model change,
// the restart button on the dashboard itself — took the page down with it.
// See internal/gateway/relay.go for what it answers itself and what it hands
// to an agent.

const defaultDashboardPort = 18780

func runDashboardCommand(args []string) {
	if len(args) > 0 {
		switch args[0] {
		case "install":
			runDashboardInstall(args[1:])
			return
		case "uninstall":
			runDashboardService(args[1:], "uninstall")
			return
		case "restart":
			runDashboardService(args[1:], "restart")
			return
		case "status":
			runDashboardService(args[1:], "status")
			return
		}
	}
	runDashboard(args)
}

func runDashboard(args []string) {
	fs := flag.NewFlagSet("dashboard", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "Config of the agent the dashboard acts as (agent 1): coord path, login, identity")
	envPath := fs.String("env", ".env", "Path to .env file")
	bind := fs.String("bind", "127.0.0.1", "Bind address")
	port := fs.Int("port", defaultDashboardPort, "Dashboard port")
	agentURL := fs.String("agent-url", "", "WebSocket address of the agent that answers chat (default: the one --config names, as registered in coord)")
	fs.Parse(args)

	loadEnv(*envPath)

	// Load, not Validate: Validate asks for the agent's model credentials,
	// which this process never uses.
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("dashboard: shutting down...")
		cancel()
	}()

	// The login lives in the agent's own database. Opening the same file
	// means the same accounts and the same sessions: nobody logs in again,
	// and the cookie the browser carries is one the agent accepts too, which
	// is what lets the relay forward it upstream.
	db, err := storage.Open(filepath.Join(cfg.Session.DataDir, "goterm.db"))
	if err != nil {
		log.Fatalf("storage: %v", err)
	}
	defer db.Close()

	if !cfg.Coord.IsEnabled() {
		log.Fatalf("dashboard: coord is disabled in %s — the dashboard reads everything but chat from it", *configPath)
	}
	coordDB, err := openCoordDB(cfg)
	if err != nil {
		log.Fatalf("coord: %v", err)
	}
	defer coordDB.Close()

	authMgr := auth.NewManager(auth.Config{
		Enabled:    cfg.Gateway.Auth.Enabled,
		PublicHost: cfg.Gateway.Auth.PublicHost,
		SessionTTL: time.Duration(cfg.Gateway.Auth.SessionTTLHours) * time.Hour,
	}, storage.NewUserStore(db))
	if !cfg.Gateway.Auth.Enabled {
		log.Printf("dashboard: auth DISABLED — do not expose this port publicly")
	}

	upstream := func() (string, error) {
		return homeAgentAddr(coordDB, cfg.Agent.ID, *agentURL), nil
	}

	previews := gateway.NewPreviewManager(coordDB)
	defer previews.StopAll()

	deps := gateway.Deps{
		Preview:      previews,
		Coord:        coordDB,
		AgentID:      cfg.Agent.ID,
		AgentName:    cfg.Agent.Name,
		OwnerChatID:  ownerChat(cfg),
		Workspace:    cfg.Claude.Workspace,
		ProviderName: cfg.Provider,
		NotesFile:    cfg.Coord.NotesFile,
		ProjectsDir:  cfg.Coord.ProjectsDir,
		SchedulesRun: cfg.Schedules.Enabled,
		ReviveAgent:  reviveAgent,
		Detached:     true,
		// The task page shows a run in flight, and runs live in the agent.
		Runs: func() []gateway.RunInfo {
			addr, _ := upstream()
			return agentRuns(addr)
		},
	}

	addr := fmt.Sprintf("%s:%d", *bind, *port)
	srv := gateway.NewServer(addr, gateway.NewMethodHandler(deps), nil, resolveDashboardDir(), authMgr)
	srv.SetRelay(&gateway.Relay{Local: gateway.NewMethodHandler(deps), Upstream: upstream})
	srv.Handle(gateway.ProjectPrefix, authMgr.RequireAuthExceptLocal(gateway.ProjectHandler(deps)))
	// The editor's terminal. Its own auth check (a login, never the loopback
	// exemption) lives in the handler: it hands out a shell.
	srv.Handle(gateway.TerminalPath, gateway.TerminalHandler(coordDB, authMgr))
	// Project dev servers, proxied — see internal/gateway/preview.go.
	srv.Handle(gateway.PreviewPrefix, previews.Handler(authMgr))

	if err := daemon.KillStaleListeners(*port); err != nil {
		log.Printf("warning: stale PID cleanup: %v", err)
	}
	up, _ := upstream()
	log.Printf("bomclaw dashboard starting on %s (chat relayed to %s)", addr, up)
	if err := srv.Start(ctx); err != nil {
		log.Printf("dashboard: %v", err)
	}
}

// homeAgentAddr is the agent chat is relayed to: the flag when given, else
// what that agent registered in coord, else agent 1's default address.
func homeAgentAddr(cdb *coord.DB, agentID, override string) string {
	if override != "" {
		return override
	}
	if agents, err := cdb.ListAgents(); err == nil {
		for _, a := range agents {
			if a.ID == agentID && a.WSAddr != "" {
				return a.WSAddr
			}
		}
	}
	return "ws://127.0.0.1:18789/ws"
}

// agentRuns asks the agent what it is running. Nil when it is not answering —
// the page then shows no live run, which is true from where it stands.
func agentRuns(wsAddr string) []gateway.RunInfo {
	url, err := gateway.PeerURL(wsAddr, "/api/status")
	if err != nil {
		return nil
	}
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Get(url)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var st gateway.StatusResult
	if json.NewDecoder(resp.Body).Decode(&st) != nil {
		return nil
	}
	return st.Runs
}

func runDashboardInstall(args []string) {
	fs := flag.NewFlagSet("dashboard install", flag.ExitOnError)
	port := fs.Int("port", defaultDashboardPort, "Dashboard port")
	bind := fs.String("bind", "127.0.0.1", "Bind address")
	configPath := fs.String("config", "config.yaml", "Config of the agent the dashboard acts as (agent 1)")
	envPath := fs.String("env", ".env", "Path to .env file")
	force := fs.Bool("force", false, "Force reinstall even if already installed")
	fs.Parse(args)

	svc, err := daemon.ResolveDashboard()
	if err != nil {
		log.Fatalf("daemon: %v", err)
	}
	if !*force {
		if installed, _ := svc.IsInstalled(); installed {
			fmt.Printf("Dashboard service already installed (%s). Use --force to reinstall.\n", svc.Label())
			fmt.Printf("Unit: %s\n", svc.UnitPath())
			return
		}
	}
	binPath, err := resolveBinaryPath()
	if err != nil {
		log.Fatalf("daemon: %v", err)
	}

	// Only HOME: the dashboard runs no model and no bot, so the API key and
	// the Telegram token the gateway's unit carries have no business here.
	env := map[string]string{}
	if home, err := os.UserHomeDir(); err == nil {
		env["HOME"] = home
	}

	ctx := context.Background()
	if err := svc.Install(ctx, daemon.InstallArgs{
		BinaryPath:  binPath,
		Command:     "dashboard",
		Port:        *port,
		Bind:        *bind,
		ConfigPath:  resolveAbsPath(*configPath),
		EnvFile:     resolveAbsPath(*envPath),
		Environment: env,
		Description: "BomClaw Dashboard",
		Force:       *force,
	}); err != nil {
		log.Fatalf("install failed: %v", err)
	}
	fmt.Printf("Dashboard service installed via %s\nUnit: %s\n", svc.Label(), svc.UnitPath())

	fmt.Print("Waiting for the dashboard to become healthy...")
	if res, err := daemon.WaitForHealthy(ctx, *port, *bind, daemon.HealthOpts{}); err != nil {
		fmt.Printf(" timeout\n")
		fmt.Fprintf(os.Stderr, "Warning: %v\nCheck logs: ~/.goterm/logs/dashboard.err.log\n", err)
	} else {
		fmt.Printf(" ok (%s)\n", res.Elapsed.Round(time.Millisecond))
	}
	fmt.Printf("\nPoint the tunnel at http://%s:%d to serve it publicly.\n", *bind, *port)
}

func runDashboardService(args []string, action string) {
	fs := flag.NewFlagSet("dashboard "+action, flag.ExitOnError)
	fs.Parse(args)

	svc, err := daemon.ResolveDashboard()
	if err != nil {
		log.Fatalf("daemon: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	switch action {
	case "uninstall":
		err = svc.Uninstall(ctx)
	case "restart":
		err = svc.Restart(ctx)
	case "status":
		rt, rerr := svc.ReadRuntime()
		if rerr != nil {
			log.Fatalf("status: %v", rerr)
		}
		fmt.Printf("Dashboard (%s): %s", svc.UnitPath(), rt.Status)
		if rt.PID > 0 {
			fmt.Printf(", pid %d", rt.PID)
		}
		fmt.Println()
		return
	}
	if err != nil {
		log.Fatalf("%s: %v", action, err)
	}
	fmt.Printf("Dashboard service: %s done\n", action)
}
