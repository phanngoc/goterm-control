package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

const (
	defaultCdpPort       = 9222
	httpTimeout          = 1500 * time.Millisecond
	reachabilityTimeout  = 500 * time.Millisecond
	launchReadyWindow    = 15 * time.Second
	launchPollInterval   = 200 * time.Millisecond
	stopTimeout          = 2500 * time.Millisecond
	stopPollInterval     = 100 * time.Millisecond
)

// Chrome candidates on Linux, ordered by preference.
var chromePaths = []string{
	"/usr/bin/google-chrome",
	"/usr/bin/google-chrome-stable",
	"/usr/bin/brave-browser",
	"/usr/bin/brave-browser-stable",
	"/usr/bin/microsoft-edge",
	"/usr/bin/microsoft-edge-stable",
	"/usr/bin/chromium",
	"/usr/bin/chromium-browser",
	"/snap/bin/chromium",
}

// Chrome manages a local Chrome process with CDP enabled.
type Chrome struct {
	cmd         *exec.Cmd
	cdpPort     int
	userDataDir string
	mu          sync.Mutex
}

// FindChrome returns the path to the first available Chrome executable.
func FindChrome() (string, error) {
	for _, p := range chromePaths {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	// Fallback: check PATH.
	for _, name := range []string{"google-chrome", "chromium", "chromium-browser"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("no Chrome/Chromium found; install google-chrome or chromium")
}

// Launch starts Chrome with --remote-debugging-port and waits for CDP ready.
func Launch(ctx context.Context) (*Chrome, error) {
	exe, err := FindChrome()
	if err != nil {
		return nil, err
	}

	home, _ := os.UserHomeDir()
	dataDir := filepath.Join(home, ".goterm", "browser", "user-data")
	os.MkdirAll(dataDir, 0o755)

	c := &Chrome{
		cdpPort:     defaultCdpPort,
		userDataDir: dataDir,
	}

	args := []string{
		fmt.Sprintf("--remote-debugging-port=%d", c.cdpPort),
		fmt.Sprintf("--user-data-dir=%s", dataDir),
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-sync",
		"--disable-background-networking",
		"--disable-component-update",
		"--disable-features=Translate,MediaRouter",
		"--disable-session-crashed-bubble",
		"--hide-crash-restore-bubble",
		"--password-store=basic",
		"--disable-dev-shm-usage",
	}

	c.cmd = exec.CommandContext(ctx, exe, args...)
	c.cmd.Stdout = nil
	c.cmd.Stderr = nil
	// Detach from parent process group so Chrome survives if we crash mid-launch.
	c.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := c.cmd.Start(); err != nil {
		return nil, fmt.Errorf("chrome launch: %w", err)
	}

	// Poll for CDP readiness.
	deadline := time.Now().Add(launchReadyWindow)
	for time.Now().Before(deadline) {
		if c.IsReachable() {
			return c, nil
		}
		time.Sleep(launchPollInterval)
	}

	// Timed out — kill and report.
	c.cmd.Process.Kill()
	return nil, fmt.Errorf("chrome CDP not ready after %s on port %d", launchReadyWindow, c.cdpPort)
}

// Stop terminates Chrome gracefully (SIGTERM), falling back to SIGKILL.
func (c *Chrome) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cmd == nil || c.cmd.Process == nil {
		return nil
	}

	c.cmd.Process.Signal(syscall.SIGTERM)

	deadline := time.Now().Add(stopTimeout)
	for time.Now().Before(deadline) {
		if !c.IsReachable() {
			c.cmd.Wait()
			return nil
		}
		time.Sleep(stopPollInterval)
	}

	c.cmd.Process.Kill()
	c.cmd.Wait()
	return nil
}

// IsReachable returns true if Chrome's CDP HTTP endpoint responds.
func (c *Chrome) IsReachable() bool {
	client := http.Client{Timeout: reachabilityTimeout}
	resp, err := client.Get(c.cdpBaseURL() + "/json/version")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

// PageWsURL returns the WebSocket debugger URL for the active page tab.
func (c *Chrome) PageWsURL() (string, error) {
	client := http.Client{Timeout: httpTimeout}
	resp, err := client.Get(c.cdpBaseURL() + "/json/list")
	if err != nil {
		return "", fmt.Errorf("cdp /json/list: %w", err)
	}
	defer resp.Body.Close()

	var targets []struct {
		Type                 string `json:"type"`
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&targets); err != nil {
		return "", fmt.Errorf("cdp /json/list decode: %w", err)
	}

	// Prefer "page" type targets.
	for _, t := range targets {
		if t.Type == "page" && t.WebSocketDebuggerURL != "" {
			return t.WebSocketDebuggerURL, nil
		}
	}
	if len(targets) > 0 && targets[0].WebSocketDebuggerURL != "" {
		return targets[0].WebSocketDebuggerURL, nil
	}
	return "", fmt.Errorf("no page target found")
}

func (c *Chrome) cdpBaseURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", c.cdpPort)
}
