package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ngocp/goterm-control/internal/browser"
)

// The `bomclaw browser` command drives the user's own browser through the
// Browser Bridge. It talks to this machine's gateway over plain HTTP — the
// agent's shell is already local, so it hits /api/browser/* as a loopback
// caller and needs no login session. The gateway exports BOMCLAW_GATEWAY_ADDR
// into every subprocess it spawns, so a second agent reaches its own gateway.

// exitNoBrowser is the distinct code the agent is told to look for: the bridge
// is up but no extension is paired/connected.
const exitNoBrowser = 3

func gatewayHTTPAddr() string {
	if a := strings.TrimSpace(os.Getenv("BOMCLAW_GATEWAY_ADDR")); a != "" {
		return strings.TrimRight(a, "/")
	}
	return "http://127.0.0.1:18789"
}

type browserResult struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// browserCall posts one action to the gateway and returns the extension's
// result payload. A 503 (no browser) exits with exitNoBrowser so scripts and
// the agent can branch on it.
func browserCall(addr, action string, params map[string]any) json.RawMessage {
	body, _ := json.Marshal(map[string]any{"action": action, "params": params})
	req, err := http.NewRequest(http.MethodPost, addr+"/api/browser/call", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "browser: %v\n", err)
		os.Exit(1)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 3 * time.Minute}).Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "browser: gateway unreachable at %s (%v)\n", addr, err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	var out browserResult
	if err := json.Unmarshal(raw, &out); err != nil {
		fmt.Fprintf(os.Stderr, "browser: bad response from gateway: %s\n", strings.TrimSpace(string(raw)))
		os.Exit(1)
	}
	if !out.OK {
		fmt.Fprintf(os.Stderr, "browser: %s\n", out.Error)
		if resp.StatusCode == http.StatusServiceUnavailable {
			os.Exit(exitNoBrowser)
		}
		os.Exit(1)
	}
	return out.Result
}

func runBrowser(args []string) {
	if len(args) == 0 {
		browserUsage()
		os.Exit(1)
	}
	sub, rest := args[0], args[1:]
	addr := gatewayHTTPAddr()

	switch sub {
	case "status":
		printBrowserStatus(httpGet(addr + "/api/browser/status"))

	case "token":
		raw := httpGet(addr + "/api/browser/token")
		var t struct{ Token, URL string }
		_ = json.Unmarshal(raw, &t)
		fmt.Printf("Pairing token: %s\nExtension endpoint: %s\n\nOpen the BomClaw Browser Bridge extension, paste the token, and connect.\n", t.Token, t.URL)

	case "navigate", "open":
		url := firstArg(rest, "navigate <url>")
		printMessage(browserCall(addr, "navigate", map[string]any{"url": url}))

	case "tabs":
		runBrowserTabs(addr, rest)

	case "snapshot":
		fs := flag.NewFlagSet("browser snapshot", flag.ExitOnError)
		selector := fs.String("selector", "", "Restrict the snapshot to this CSS selector")
		fs.Parse(rest)
		res := browserCall(addr, "snapshot", map[string]any{"selector": *selector})
		tree, err := browser.FormatSnapshotJSON(res)
		if err != nil {
			fmt.Println(string(res))
			return
		}
		fmt.Print(tree)

	case "click":
		printMessage(browserCall(addr, "click", map[string]any{"ref": firstArg(rest, "click <ref>")}))

	case "fill", "type":
		ref, text := twoArgs(rest, sub+" <ref> <text>")
		printMessage(browserCall(addr, sub, map[string]any{"ref": ref, "text": text}))

	case "select":
		ref, value := twoArgs(rest, "select <ref> <value>")
		printMessage(browserCall(addr, "select", map[string]any{"ref": ref, "value": value}))

	case "text", "get-text":
		fs := flag.NewFlagSet("browser text", flag.ExitOnError)
		ref := fs.String("ref", "", "Element ref (omit for the whole page)")
		property := fs.String("property", "text", "text|html|value|title|url")
		fs.Parse(rest)
		res := browserCall(addr, "text", map[string]any{"ref": *ref, "property": *property})
		printJSONField(res, "text")

	case "scroll":
		fs := flag.NewFlagSet("browser scroll", flag.ExitOnError)
		pixels := fs.Int("pixels", 0, "How far to scroll (default 300)")
		fs.Parse(rest)
		dir := "down"
		if fs.NArg() > 0 {
			dir = fs.Arg(0)
		}
		printMessage(browserCall(addr, "scroll", map[string]any{"direction": dir, "pixels": *pixels}))

	case "wait":
		fs := flag.NewFlagSet("browser wait", flag.ExitOnError)
		ref := fs.String("ref", "", "Wait until this element ref appears")
		text := fs.String("text", "", "Wait until this text is visible")
		ms := fs.Int("ms", 0, "Wait this many milliseconds (or the timeout for ref/text)")
		fs.Parse(rest)
		printMessage(browserCall(addr, "wait", map[string]any{"ref": *ref, "text": *text, "ms": *ms}))

	case "back":
		printMessage(browserCall(addr, "back", map[string]any{}))

	case "screenshot":
		fs := flag.NewFlagSet("browser screenshot", flag.ExitOnError)
		out := fs.String("out", "", "Output PNG path (default: a temp file)")
		fs.Parse(rest)
		res := browserCall(addr, "screenshot", map[string]any{})
		var payload struct {
			Data string `json:"data"`
		}
		_ = json.Unmarshal(res, &payload)
		data, err := base64.StdEncoding.DecodeString(payload.Data)
		if err != nil || len(data) == 0 {
			fmt.Fprintln(os.Stderr, "browser: the browser returned no image")
			os.Exit(1)
		}
		path := *out
		if path == "" {
			path = filepath.Join(os.TempDir(), fmt.Sprintf("bomclaw-browser-%d.png", time.Now().UnixNano()))
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "browser: write screenshot: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Saved screenshot to %s\n", path)

	case "eval":
		if len(rest) == 0 {
			fmt.Fprintln(os.Stderr, "usage: bomclaw browser eval '<javascript>'")
			os.Exit(1)
		}
		res := browserCall(addr, "eval", map[string]any{"expression": strings.Join(rest, " ")})
		printJSONField(res, "result")

	case "help", "-h", "--help":
		browserUsage()

	default:
		fmt.Fprintf(os.Stderr, "unknown browser subcommand: %s\n\n", sub)
		browserUsage()
		os.Exit(1)
	}
}

func runBrowserTabs(addr string, rest []string) {
	action := "list"
	if len(rest) > 0 {
		action = rest[0]
	}
	params := map[string]any{"action": action}
	switch action {
	case "list":
		res := browserCall(addr, "tabs", params)
		var out struct {
			Tabs []struct {
				ID     string `json:"id"`
				Title  string `json:"title"`
				URL    string `json:"url"`
				Active bool   `json:"active"`
				Owner  string `json:"owner"`
			} `json:"tabs"`
		}
		if err := json.Unmarshal(res, &out); err != nil {
			fmt.Println(string(res))
			return
		}
		if len(out.Tabs) == 0 {
			fmt.Println("no open tabs")
			return
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "\tID\tTITLE\tURL")
		for _, t := range out.Tabs {
			marker := " "
			if t.Active {
				marker = "*" // the tab THIS agent acts on
			}
			title := truncate(t.Title, 40)
			// A tab another agent is driving is worth flagging: focusing it
			// would put two agents on one page.
			if t.Owner != "" && !t.Active {
				title += " [" + t.Owner + "]"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", marker, t.ID, title, t.URL)
		}
		w.Flush()
		fmt.Println("\n* = the tab this agent acts on. `tabs focus <id>` to switch.")
	case "open":
		params["url"] = firstArg(rest[1:], "tabs open <url>")
		printMessage(browserCall(addr, "tabs", params))
	case "focus", "close":
		params["tab_id"] = firstArg(rest[1:], "tabs "+action+" <tab_id>")
		printMessage(browserCall(addr, "tabs", params))
	default:
		fmt.Fprintf(os.Stderr, "usage: bomclaw browser tabs [list|open <url>|focus <id>|close <id>]\n")
		os.Exit(1)
	}
}

// --- helpers ---

func httpGet(url string) json.RawMessage {
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "browser: gateway unreachable (%v)\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return raw
}

func firstArg(args []string, usage string) string {
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		fmt.Fprintf(os.Stderr, "usage: bomclaw browser %s\n", usage)
		os.Exit(1)
	}
	return args[0]
}

func twoArgs(args []string, usage string) (string, string) {
	if len(args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: bomclaw browser %s\n", usage)
		os.Exit(1)
	}
	// Everything after the ref joins into the text, so an unquoted value works.
	return args[0], strings.Join(args[1:], " ")
}

// printBrowserStatus renders the bridge status as a few readable lines. The
// raw JSON was fine for one agent; with several on a machine the useful
// questions are which agent this is, whether its browser is attached, and
// whether it has actually done anything.
func printBrowserStatus(raw json.RawMessage) {
	var st struct {
		Connected    bool   `json:"connected"`
		AgentID      string `json:"agent_id"`
		AgentName    string `json:"agent_name"`
		BrowserName  string `json:"browser_name"`
		Browser      string `json:"browser"`
		Client       string `json:"client"`
		ConnectedAt  string `json:"connected_at"`
		Actions      int    `json:"actions"`
		LastAction   string `json:"last_action"`
		LastActionAt string `json:"last_action_at"`
		LastError    string `json:"last_error"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		fmt.Println(strings.TrimSpace(string(raw)))
		return
	}

	agent := orAny(st.AgentName, st.AgentID)
	if agent == "" {
		agent = "this agent"
	}
	if !st.Connected {
		fmt.Printf("%s: no browser connected\n\n"+
			"Pair the BomClaw Browser Bridge extension with the token from\n"+
			"`bomclaw browser token`, then check the extension popup.\n", agent)
		return
	}

	browser := orAny(st.BrowserName, st.Browser)
	fmt.Printf("%s: connected to %s\n", agent, browser)
	if st.ConnectedAt != "" {
		if t, err := time.Parse(time.RFC3339, st.ConnectedAt); err == nil {
			fmt.Printf("  since    %s\n", t.Local().Format("15:04:05"))
		}
	}
	fmt.Printf("  actions  %d\n", st.Actions)
	if st.LastAction != "" {
		line := "  last     " + st.LastAction
		if t, err := time.Parse(time.RFC3339, st.LastActionAt); err == nil {
			line += " at " + t.Local().Format("15:04:05")
		}
		fmt.Println(line)
	}
	if st.LastError != "" {
		fmt.Printf("  error    %s\n", st.LastError)
	}
}

// printMessage prints the {"message":…} an action returns, falling back to the
// raw JSON for shapes without one.
func printMessage(res json.RawMessage) {
	var m struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(res, &m); err == nil && m.Message != "" {
		fmt.Println(m.Message)
		return
	}
	fmt.Println(strings.TrimSpace(string(res)))
}

// printJSONField prints one string field of a result object; with field=="" or
// a value that is not a plain string, it prints the raw JSON.
func printJSONField(res json.RawMessage, field string) {
	if field == "" {
		fmt.Println(strings.TrimSpace(string(res)))
		return
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(res, &obj); err == nil {
		if v, ok := obj[field]; ok {
			var s string
			if json.Unmarshal(v, &s) == nil {
				fmt.Println(s)
				return
			}
			fmt.Println(strings.TrimSpace(string(v)))
			return
		}
	}
	fmt.Println(strings.TrimSpace(string(res)))
}

func browserUsage() {
	fmt.Println(`bomclaw browser — drive the user's own browser via the Browser Bridge extension

Usage:
  bomclaw browser <subcommand> [args]

Subcommands:
  status                       Is a browser connected?
  token                        Show the pairing token and endpoint for the extension
  tabs [list|open <url>|focus <id>|close <id>]
  navigate <url>               Open a URL in the agent's tab
  snapshot [--selector CSS]    Page as a tree of elements with refs (n1, n2, …)
  click <ref>                  Click an element by ref
  fill <ref> <text>            Replace an input's value
  type <ref> <text>            Append to an input
  select <ref> <value>         Choose a dropdown option
  text [--ref R] [--property text|html|value|title|url]
  scroll [up|down|left|right] [--pixels N]
  wait [--ref R] [--text T] [--ms N]
  back                         Go back in history
  screenshot [--out PATH]      Save a PNG of the page
  eval '<javascript>'          Run JavaScript in the page (may be disabled)

Exit code 3 means no browser is connected.`)
}
