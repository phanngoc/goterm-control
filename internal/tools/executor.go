package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Executor dispatches tool calls by name.
type Executor struct {
	shell   *ShellTool
	fs      *FileSystemTool
	mac     *MacTool
	system  *SystemTool
	browser *BrowserTool
}

// ToolResult carries the result of a tool call.
type ToolResult struct {
	Output      string
	IsError     bool
	IsImage     bool   // true when Output is a file path to an image
	ImagePath   string // path to image file when IsImage=true
}

func New(cfg ExecutorConfig) *Executor {
	return &Executor{
		shell: &ShellTool{
			DefaultTimeout: cfg.ShellTimeout,
			MaxOutputBytes: cfg.MaxOutputBytes,
		},
		fs:      &FileSystemTool{AllowedPaths: cfg.AllowedPaths},
		mac:     &MacTool{},
		system:  &SystemTool{},
		browser: &BrowserTool{},
	}
}

type ExecutorConfig struct {
	ShellTimeout   int
	MaxOutputBytes int
	AllowedPaths   []string
}

func (e *Executor) Run(ctx context.Context, name string, input json.RawMessage) ToolResult {
	var (
		out string
		err error
	)

	switch name {
	case "run_shell":
		out, err = e.shell.Run(ctx, input)
	case "read_file":
		out, err = e.fs.ReadFile(ctx, input)
	case "write_file":
		out, err = e.fs.WriteFile(ctx, input)
	case "list_dir":
		out, err = e.fs.ListDir(ctx, input)
	case "search_files":
		out, err = e.fs.SearchFiles(ctx, input)
	case "take_screenshot":
		out, err = e.mac.Screenshot(ctx, input)
		if err == nil && len(out) > 11 && out[:11] == "SCREENSHOT:" {
			return ToolResult{
				IsImage:   true,
				ImagePath: out[11:],
				Output:    "Screenshot taken",
			}
		}
	case "get_clipboard":
		out, err = e.mac.GetClipboard(ctx, input)
	case "set_clipboard":
		out, err = e.mac.SetClipboard(ctx, input)
	case "run_applescript":
		out, err = e.mac.RunAppleScript(ctx, input)
	case "open_app":
		out, err = e.mac.OpenApp(ctx, input)
	case "get_system_info":
		out, err = e.system.Info(ctx, input)
	case "list_processes":
		out, err = e.system.Processes(ctx, input)
	case "kill_process":
		out, err = e.system.Kill(ctx, input)
	case "browse_url":
		out, err = e.mac.BrowseURL(ctx, input)

	// Browser automation (agent-browser CLI)
	case "browser_navigate":
		out, err = e.browser.Navigate(ctx, input)
	case "browser_snapshot":
		out, err = e.browser.Snapshot(ctx, input)
	case "browser_click":
		out, err = e.browser.Click(ctx, input)
	case "browser_fill":
		out, err = e.browser.Fill(ctx, input)
	case "browser_type":
		out, err = e.browser.Type(ctx, input)
	case "browser_select":
		out, err = e.browser.Select(ctx, input)
	case "browser_scroll":
		out, err = e.browser.Scroll(ctx, input)
	case "browser_screenshot":
		out, err = e.browser.Screenshot(ctx, input)
		if err == nil && strings.HasPrefix(out, "SCREENSHOT:") {
			return ToolResult{
				IsImage:   true,
				ImagePath: out[11:],
				Output:    "Browser screenshot taken",
			}
		}
	case "browser_get_text":
		out, err = e.browser.GetText(ctx, input)
	case "browser_eval":
		out, err = e.browser.Eval(ctx, input)
	case "browser_wait":
		out, err = e.browser.Wait(ctx, input)
	case "browser_back":
		out, err = e.browser.Back(ctx, input)

	default:
		return ToolResult{
			Output:  fmt.Sprintf("Unknown tool: %s", name),
			IsError: true,
		}
	}

	if err != nil {
		return ToolResult{
			Output:  fmt.Sprintf("Tool %s error: %v", name, err),
			IsError: true,
		}
	}
	return ToolResult{Output: out}
}
