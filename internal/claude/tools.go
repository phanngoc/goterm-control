package claude

import (
	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
)

// MacTools defines all tools available to Claude for Mac control.
var MacTools = []anthropic.ToolParam{
	{
		Name:        "run_shell",
		Description: param.Opt[string]{Value: "Execute a shell command on the Mac. Returns combined stdout and stderr. Use bash -c for complex commands."},
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"command":     map[string]any{"type": "string", "description": "The shell command to execute"},
				"working_dir": map[string]any{"type": "string", "description": "Working directory (optional, defaults to $HOME)"},
				"timeout":     map[string]any{"type": "integer", "description": "Timeout in seconds (optional, default 60, max 300)"},
			},
			Required: []string{"command"},
		},
	},
	{
		Name:        "read_file",
		Description: param.Opt[string]{Value: "Read the contents of a file. Returns file content as text. Limit 100KB."},
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"path":   map[string]any{"type": "string", "description": "Absolute or relative (from $HOME) path to the file"},
				"offset": map[string]any{"type": "integer", "description": "Line number to start reading from (optional, 1-indexed)"},
				"limit":  map[string]any{"type": "integer", "description": "Number of lines to read (optional)"},
			},
			Required: []string{"path"},
		},
	},
	{
		Name:        "write_file",
		Description: param.Opt[string]{Value: "Write or overwrite a file with given content. Creates parent directories if needed."},
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"path":    map[string]any{"type": "string", "description": "Absolute or relative (from $HOME) path to write"},
				"content": map[string]any{"type": "string", "description": "Content to write to the file"},
				"append":  map[string]any{"type": "boolean", "description": "If true, append to existing file (default false)"},
			},
			Required: []string{"path", "content"},
		},
	},
	{
		Name:        "list_dir",
		Description: param.Opt[string]{Value: "List files and directories. Shows names, sizes, and modification times."},
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"path":        map[string]any{"type": "string", "description": "Directory path to list (defaults to $HOME)"},
				"recursive":   map[string]any{"type": "boolean", "description": "List recursively (default false)"},
				"show_hidden": map[string]any{"type": "boolean", "description": "Show hidden files starting with . (default false)"},
			},
		},
	},
	{
		Name:        "search_files",
		Description: param.Opt[string]{Value: "Search for files by name pattern or search file contents. Returns matching paths and lines."},
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"path":           map[string]any{"type": "string", "description": "Directory to search in (defaults to $HOME)"},
				"pattern":        map[string]any{"type": "string", "description": "Search pattern (regex supported)"},
				"search_content": map[string]any{"type": "boolean", "description": "If true, search file contents instead of filenames (default false)"},
				"file_pattern":   map[string]any{"type": "string", "description": "Filter files by glob pattern when search_content=true (e.g. '*.go')"},
			},
			Required: []string{"pattern"},
		},
	},
	{
		Name:        "take_screenshot",
		Description: param.Opt[string]{Value: "Take a screenshot of the screen. The image will be sent to Telegram."},
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{},
		},
	},
	{
		Name:        "get_clipboard",
		Description: param.Opt[string]{Value: "Get the current contents of the clipboard."},
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{},
		},
	},
	{
		Name:        "set_clipboard",
		Description: param.Opt[string]{Value: "Set the clipboard to the given text."},
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"text": map[string]any{"type": "string", "description": "Text to put on the clipboard"},
			},
			Required: []string{"text"},
		},
	},
	{
		Name:        "run_applescript",
		Description: param.Opt[string]{Value: "Run an AppleScript to control Mac applications (Finder, Safari, Music, Messages, etc.)."},
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"script": map[string]any{"type": "string", "description": "AppleScript code to execute"},
			},
			Required: []string{"script"},
		},
	},
	{
		Name:        "open_app",
		Description: param.Opt[string]{Value: "Open a Mac application or file with its default application."},
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"name": map[string]any{"type": "string", "description": "Application name (e.g. 'Safari', 'Finder', 'Terminal') or file path"},
			},
			Required: []string{"name"},
		},
	},
	{
		Name:        "get_system_info",
		Description: param.Opt[string]{Value: "Get Mac system information: hardware model, OS version, CPU, memory, disk usage."},
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{},
		},
	},
	{
		Name:        "list_processes",
		Description: param.Opt[string]{Value: "List running processes with PID, CPU%, memory, and command name."},
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"filter":  map[string]any{"type": "string", "description": "Filter processes by name substring (optional)"},
				"sort_by": map[string]any{"type": "string", "description": "Sort by: cpu, memory, pid (default: cpu)", "enum": []string{"cpu", "memory", "pid"}},
			},
		},
	},
	{
		Name:        "kill_process",
		Description: param.Opt[string]{Value: "Kill a process by PID or name. Sends SIGTERM (graceful) or SIGKILL (force)."},
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"pid":    map[string]any{"type": "integer", "description": "Process ID to kill"},
				"name":   map[string]any{"type": "string", "description": "Process name to kill (kills all matching)"},
				"signal": map[string]any{"type": "string", "description": "Signal to send: TERM (graceful) or KILL (force). Default: TERM", "enum": []string{"TERM", "KILL"}},
			},
		},
	},
	{
		Name:        "browse_url",
		Description: param.Opt[string]{Value: "Open a URL in the default browser."},
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"url": map[string]any{"type": "string", "description": "URL to open"},
			},
			Required: []string{"url"},
		},
	},
}
