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

	anthropicClient "github.com/ngocp/goterm-control/internal/anthropic"
	"github.com/ngocp/goterm-control/internal/agent"
	"github.com/ngocp/goterm-control/internal/channel"
	"github.com/ngocp/goterm-control/internal/claude"
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

	// Create model provider: use CLI for OAuth tokens, direct API for API keys
	var provider agent.ModelProvider
	if strings.HasPrefix(cfg.Claude.APIKey, "sk-ant-oat") {
		// OAuth subscription token — must use claude CLI subprocess
		log.Println("gateway: using Claude CLI provider (OAuth token detected)")
		provider = claude.NewCLIProvider()
	} else {
		// Direct API key (sk-ant-api03-...)
		log.Println("gateway: using direct Anthropic API provider")
		provider = anthropicClient.New(cfg.Claude.APIKey)
	}

	// Model resolver
	resolver := models.NewResolver(cfg.Models.Default, cfg.Models.Custom)

	// Session manager
	store := session.NewStore(cfg.Session.DataDir + "/sessions.json")
	sessions := session.NewManager(store, 0)

	// Gateway RPC server
	addr := fmt.Sprintf("%s:%d", *bind, *port)
	startTime := time.Now()
	deps := gateway.Deps{
		Sessions: sessions,
		Resolver: resolver,
		Provider: provider,
		System:   cfg.Claude.SystemPrompt,
		DataDir:  cfg.Session.DataDir,
		Uptime:   func() time.Duration { return time.Since(startTime) },
	}

	srv := gateway.NewServer(addr, gateway.NewMethodHandler(deps), gateway.NewStreamSendHandler(deps), "dashboard/dist")

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

	var chatProvider agent.ModelProvider
	if strings.HasPrefix(cfg.Claude.APIKey, "sk-ant-oat") {
		chatProvider = claude.NewCLIProvider()
	} else {
		chatProvider = anthropicClient.New(cfg.Claude.APIKey)
	}
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
		// Browser automation (agent-browser)
		{"browser_navigate", "Navigate browser to a URL. Opens the page in a headless Chrome browser."},
		{"browser_snapshot", "Get an accessibility snapshot of the current page with element refs (@e1, @e2, etc). Use -i for interactive elements only."},
		{"browser_click", "Click an element by its ref (e.g. @e3). Always snapshot first to get refs."},
		{"browser_fill", "Clear and type text into an input field by ref. Use for form fields."},
		{"browser_type", "Append text to an input field by ref (does not clear first)."},
		{"browser_select", "Select a dropdown option by ref and value."},
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

		// Browser automation tools (agent-browser)
		"browser_navigate": {"type": "object", "properties": map[string]any{
			"url": map[string]any{"type": "string", "description": "URL to navigate to"},
		}, "required": []string{"url"}},
		"browser_snapshot": {"type": "object", "properties": map[string]any{
			"selector":    map[string]any{"type": "string", "description": "CSS selector to scope the snapshot"},
			"interactive": map[string]any{"type": "boolean", "description": "Show only interactive elements (default true)"},
		}},
		"browser_click": {"type": "object", "properties": map[string]any{
			"ref":     map[string]any{"type": "string", "description": "Element ref from snapshot (e.g. @e3)"},
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
