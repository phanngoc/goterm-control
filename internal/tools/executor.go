package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// Executor dispatches tool calls by name.
type Executor struct {
	shell  *ShellTool
	fs     *FileSystemTool
	mac    *MacTool
	system *SystemTool
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
		fs:     &FileSystemTool{AllowedPaths: cfg.AllowedPaths},
		mac:    &MacTool{},
		system: &SystemTool{},
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
