package chat

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Naming a tool call so a person can tell what the agent is doing.
//
// This lived in internal/bot, where only the chat lane could reach it — so the
// task board showed "Bash" while a thread watching the same agent showed
// "Bash(cd ../goterm-workspace)". Same agent, same second, two different
// answers to "what is it doing", and the less useful one on the screen built
// for watching work.

// ToolLabel creates a short label like Bash(cd stock_d) or Read(bot/handler.go)
func ToolLabel(name, inputJSON string) string {
	var m map[string]any
	if json.Unmarshal([]byte(inputJSON), &m) != nil {
		return name
	}

	// Path keys get tail-truncated (show meaningful end); others get head-truncated.
	pathKeys := map[string]bool{"path": true, "file_path": true}

	for _, key := range []string{"command", "path", "file_path", "url", "query", "pattern", "script", "expression", "name", "ref", "text", "glob", "regex"} {
		if v, ok := m[key]; ok {
			s := fmt.Sprintf("%v", v)
			if s == "" {
				continue
			}
			if pathKeys[key] {
				s = shortenPath(s, 25)
			} else if key == "command" {
				s = shortenBashCommand(s, 25)
			} else {
				r := []rune(s)
				if len(r) > 20 {
					s = string(r[:20])
				}
			}
			return name + "(" + s + ")"
		}
	}
	return name
}

// shortenBashCommand extracts the first segment of a shell command (before
// &&, ||, |, ;) and shortens any path-like argument while keeping the
// command prefix (cd, ls, grep, etc.).
//
//	"cd /Users/ngocp/Documents/projects/meClaw/goterm-control" → "cd ../goterm-control"
//	"ls -la /very/long/path/to/dir"                            → "ls ../dir"
//	"echo hello world"                                         → "echo hello world"
func shortenBashCommand(s string, maxRunes int) string {
	if len([]rune(s)) <= maxRunes {
		return s
	}

	// Take the first command segment (before &&, ||, |, ;).
	seg := s
	for _, sep := range []string{" && ", " || ", " | ", "; "} {
		if idx := strings.Index(seg, sep); idx >= 0 {
			seg = seg[:idx]
		}
	}

	// Split into tokens; find the command prefix and the first path argument.
	tokens := strings.Fields(seg)
	if len(tokens) == 0 {
		return headTruncate(s, maxRunes)
	}

	cmd := tokens[0] // e.g. "cd", "ls", "grep"
	var pathIdx int  // index of the first path-like token
	var foundPath bool
	for i := 1; i < len(tokens); i++ {
		t := tokens[i]
		if strings.HasPrefix(t, "/") || strings.HasPrefix(t, "./") ||
			strings.HasPrefix(t, "~/") || strings.HasPrefix(t, "../") {
			pathIdx = i
			foundPath = true
			break
		}
	}

	if !foundPath {
		return headTruncate(s, maxRunes)
	}

	// Budget for the path: maxRunes minus "cmd " prefix.
	prefix := cmd
	pathBudget := maxRunes - len([]rune(prefix)) - 1 // -1 for space
	if pathBudget < 6 {
		return headTruncate(s, maxRunes)
	}

	shortened := shortenPath(tokens[pathIdx], pathBudget)
	return prefix + " " + shortened
}

// headTruncate keeps the first maxRunes runes of s.
func headTruncate(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes])
}

// shortenPath keeps the last path components that fit within maxRunes,
// so "/Users/ngocp/Documents/projects/meClaw/goterm-control/internal/bot/handler.go"
// becomes "../bot/handler.go" instead of the useless "/Users/ngocp/Do".
func shortenPath(s string, maxRunes int) string {
	if len([]rune(s)) <= maxRunes {
		return s
	}
	parts := strings.Split(s, "/")
	// Build from the tail, accumulating components.
	var tail string
	for i := len(parts) - 1; i >= 0; i-- {
		candidate := parts[i]
		if tail != "" {
			candidate = parts[i] + "/" + tail
		}
		if len([]rune(candidate))+3 > maxRunes { // +3 for "../"
			break
		}
		tail = candidate
	}
	if tail == "" {
		// Filename alone exceeds budget — truncate the filename.
		r := []rune(parts[len(parts)-1])
		if len(r) > maxRunes-3 {
			tail = string(r[:maxRunes-3])
		} else {
			tail = string(r)
		}
	}
	if tail == s {
		return s
	}
	return "../" + tail
}

// sendText converts markdown to Telegram HTML and sends the message.
