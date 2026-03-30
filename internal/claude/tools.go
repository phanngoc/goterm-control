package claude

import "github.com/anthropics/anthropic-sdk-go"

// MacTools defines all tools available to Claude for Mac control.
var MacTools = []anthropic.ToolParam{
	{
		Name:        anthropic.F("run_shell"),
		Description: anthropic.F("Execute a shell command on the Mac. Returns combined stdout and stderr. Use bash -c for complex commands."),
		InputSchema: anthropic.F[interface{}](map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"command": map[string]interface{}{
					"type":        "string",
					"description": "The shell command to execute",
				},
				"working_dir": map[string]interface{}{
					"type":        "string",
					"description": "Working directory (optional, defaults to $HOME)",
				},
				"timeout": map[string]interface{}{
					"type":        "integer",
					"description": "Timeout in seconds (optional, default 60, max 300)",
				},
			},
			"required": []string{"command"},
		}),
	},
	{
		Name:        anthropic.F("read_file"),
		Description: anthropic.F("Read the contents of a file. Returns file content as text. Limit 100KB."),
		InputSchema: anthropic.F[interface{}](map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "Absolute or relative (from $HOME) path to the file",
				},
				"offset": map[string]interface{}{
					"type":        "integer",
					"description": "Line number to start reading from (optional, 1-indexed)",
				},
				"limit": map[string]interface{}{
					"type":        "integer",
					"description": "Number of lines to read (optional)",
				},
			},
			"required": []string{"path"},
		}),
	},
	{
		Name:        anthropic.F("write_file"),
		Description: anthropic.F("Write or overwrite a file with given content. Creates parent directories if needed."),
		InputSchema: anthropic.F[interface{}](map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "Absolute or relative (from $HOME) path to write",
				},
				"content": map[string]interface{}{
					"type":        "string",
					"description": "Content to write to the file",
				},
				"append": map[string]interface{}{
					"type":        "boolean",
					"description": "If true, append to existing file (default false)",
				},
			},
			"required": []string{"path", "content"},
		}),
	},
	{
		Name:        anthropic.F("list_dir"),
		Description: anthropic.F("List files and directories. Shows names, sizes, and modification times."),
		InputSchema: anthropic.F[interface{}](map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "Directory path to list (defaults to $HOME)",
				},
				"recursive": map[string]interface{}{
					"type":        "boolean",
					"description": "List recursively (default false)",
				},
				"show_hidden": map[string]interface{}{
					"type":        "boolean",
					"description": "Show hidden files starting with . (default false)",
				},
			},
		}),
	},
	{
		Name:        anthropic.F("search_files"),
		Description: anthropic.F("Search for files by name pattern or search file contents. Returns matching paths and lines."),
		InputSchema: anthropic.F[interface{}](map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "Directory to search in (defaults to $HOME)",
				},
				"pattern": map[string]interface{}{
					"type":        "string",
					"description": "Search pattern (regex supported)",
				},
				"search_content": map[string]interface{}{
					"type":        "boolean",
					"description": "If true, search file contents instead of filenames (default false)",
				},
				"file_pattern": map[string]interface{}{
					"type":        "string",
					"description": "Filter files by glob pattern when search_content=true (e.g. '*.go')",
				},
			},
			"required": []string{"pattern"},
		}),
	},
	{
		Name:        anthropic.F("take_screenshot"),
		Description: anthropic.F("Take a screenshot of the screen. The image will be sent to Telegram."),
		InputSchema: anthropic.F[interface{}](map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		}),
	},
	{
		Name:        anthropic.F("get_clipboard"),
		Description: anthropic.F("Get the current contents of the clipboard."),
		InputSchema: anthropic.F[interface{}](map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		}),
	},
	{
		Name:        anthropic.F("set_clipboard"),
		Description: anthropic.F("Set the clipboard to the given text."),
		InputSchema: anthropic.F[interface{}](map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"text": map[string]interface{}{
					"type":        "string",
					"description": "Text to put on the clipboard",
				},
			},
			"required": []string{"text"},
		}),
	},
	{
		Name:        anthropic.F("run_applescript"),
		Description: anthropic.F("Run an AppleScript to control Mac applications (Finder, Safari, Music, Messages, etc.)."),
		InputSchema: anthropic.F[interface{}](map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"script": map[string]interface{}{
					"type":        "string",
					"description": "AppleScript code to execute",
				},
			},
			"required": []string{"script"},
		}),
	},
	{
		Name:        anthropic.F("open_app"),
		Description: anthropic.F("Open a Mac application or file with its default application."),
		InputSchema: anthropic.F[interface{}](map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"name": map[string]interface{}{
					"type":        "string",
					"description": "Application name (e.g. 'Safari', 'Finder', 'Terminal') or file path",
				},
			},
			"required": []string{"name"},
		}),
	},
	{
		Name:        anthropic.F("get_system_info"),
		Description: anthropic.F("Get Mac system information: hardware model, OS version, CPU, memory, disk usage."),
		InputSchema: anthropic.F[interface{}](map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		}),
	},
	{
		Name:        anthropic.F("list_processes"),
		Description: anthropic.F("List running processes with PID, CPU%, memory, and command name."),
		InputSchema: anthropic.F[interface{}](map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"filter": map[string]interface{}{
					"type":        "string",
					"description": "Filter processes by name substring (optional)",
				},
				"sort_by": map[string]interface{}{
					"type":        "string",
					"description": "Sort by: cpu, memory, pid (default: cpu)",
					"enum":        []string{"cpu", "memory", "pid"},
				},
			},
		}),
	},
	{
		Name:        anthropic.F("kill_process"),
		Description: anthropic.F("Kill a process by PID or name. Sends SIGTERM (graceful) or SIGKILL (force)."),
		InputSchema: anthropic.F[interface{}](map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"pid": map[string]interface{}{
					"type":        "integer",
					"description": "Process ID to kill",
				},
				"name": map[string]interface{}{
					"type":        "string",
					"description": "Process name to kill (kills all matching)",
				},
				"signal": map[string]interface{}{
					"type":        "string",
					"description": "Signal to send: TERM (graceful) or KILL (force). Default: TERM",
					"enum":        []string{"TERM", "KILL"},
				},
			},
		}),
	},
	{
		Name:        anthropic.F("browse_url"),
		Description: anthropic.F("Open a URL in the default browser."),
		InputSchema: anthropic.F[interface{}](map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"url": map[string]interface{}{
					"type":        "string",
					"description": "URL to open",
				},
			},
			"required": []string{"url"},
		}),
	},
}
