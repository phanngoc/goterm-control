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

	"golang.org/x/net/websocket"

	anthropicClient "github.com/ngocp/goterm-control/internal/anthropic"
	"github.com/ngocp/goterm-control/internal/agent"
	"github.com/ngocp/goterm-control/internal/channel"
	"github.com/ngocp/goterm-control/internal/config"
	agentctx "github.com/ngocp/goterm-control/internal/context"
	"github.com/ngocp/goterm-control/internal/gateway"
	"github.com/ngocp/goterm-control/internal/models"
	"github.com/ngocp/goterm-control/internal/session"
	"github.com/ngocp/goterm-control/internal/tools"
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
		runGateway(os.Args[2:])
	case "send":
		runSend(os.Args[2:])
	case "status":
		runStatus(os.Args[2:])
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
	fmt.Println(`NanoClaw — lean AI agent for remote host control

Usage:
  nanoclaw <command> [flags]

Commands:
  gateway   Start the gateway (Telegram + WebSocket RPC server)
  send      Send a message to the agent via gateway
  status    Show gateway status
  models    List available models
  chat      Interactive CLI chat with the agent (no gateway needed)
  help      Show this help`)
}

// --- gateway command ---

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

	// Create the Anthropic provider
	provider := anthropicClient.New(cfg.Claude.APIKey)

	// Model resolver
	resolver := models.NewResolver(cfg.Models.Default, cfg.Models.Custom)

	// Session manager
	store := session.NewStore(cfg.Session.DataDir + "/sessions.json")
	sessions := session.NewManager(store, 0)

	// Gateway RPC server
	addr := fmt.Sprintf("%s:%d", *bind, *port)
	deps := gateway.Deps{
		Sessions: sessions,
		Resolver: resolver,
		Provider: provider,
		System:   cfg.Claude.SystemPrompt,
		Uptime:   nil, // set after server creation
	}

	srv := gateway.NewServer(addr, gateway.NewMethodHandler(deps))
	deps.Uptime = srv.Uptime

	// Also start Telegram bot in background if configured
	if cfg.Telegram.Token != "" {
		go func() {
			// Import the old bot package for Telegram support
			// For now, the gateway runs standalone — Telegram integration
			// will be wired through the channel interface
			log.Printf("gateway: Telegram channel available (use existing goterm binary for now)")
		}()
	}

	log.Printf("nanoclaw gateway starting on %s", addr)
	if err := srv.Start(ctx); err != nil {
		log.Fatalf("gateway: %v", err)
	}

	sessions.SaveNow()
	log.Println("nanoclaw: shutdown complete")
}

// --- send command ---

func runSend(args []string) {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	addr := fs.String("addr", "ws://127.0.0.1:18789/ws", "Gateway WebSocket address")
	modelID := fs.String("model", "", "Model override")
	fs.Parse(args)

	message := strings.Join(fs.Args(), " ")
	if message == "" {
		fmt.Fprintln(os.Stderr, "Usage: nanoclaw send [--model <model>] <message>")
		os.Exit(1)
	}

	ws, err := websocket.Dial(*addr, "", "http://localhost/")
	if err != nil {
		log.Fatalf("connect: %v (is the gateway running?)", err)
	}
	defer ws.Close()

	params := gateway.SendParams{Message: message, ModelID: *modelID}
	paramsJSON, _ := json.Marshal(params)

	req := gateway.Request{ID: "1", Method: "send", Params: paramsJSON}
	if err := websocket.JSON.Send(ws, req); err != nil {
		log.Fatalf("send: %v", err)
	}

	var resp gateway.Response
	if err := websocket.JSON.Receive(ws, &resp); err != nil {
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

	ws, err := websocket.Dial(*addr, "", "http://localhost/")
	if err != nil {
		fmt.Println("Gateway: offline")
		os.Exit(1)
	}
	defer ws.Close()

	req := gateway.Request{ID: "1", Method: "status"}
	websocket.JSON.Send(ws, req)

	var resp gateway.Response
	websocket.JSON.Receive(ws, &resp)

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

	provider := anthropicClient.New(cfg.Claude.APIKey)
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

	fmt.Printf("NanoClaw Chat — model: %s\nType /quit to exit\n\n", modelID)

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
			Provider:     provider,
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
		return nil     // already printed via OnText
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
		"get_system_info":  {"type": "object", "properties": map[string]any{}},
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
	}
	if s, ok := schemas[name]; ok {
		return s
	}
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
