package memory

import (
	"fmt"
	"strings"
	"time"
)

// NoReplySentinel is what the agent replies with after a successful flush
// turn when it has nothing to say to the user (openclaw NO_REPLY pattern).
const NoReplySentinel = "NO_REPLY"

// policyHeader is the memory usage policy injected at the top of the memory
// block. All paths are ABSOLUTE: the Claude CLI has its own built-in memory
// feature pointing at ~/.claude/projects/<cwd>/memory/, and relative paths
// like "memory/..." get resolved against that store instead of the workspace.
// Placeholders: %[1]s = MEMORY.md path, %[2]s = notes dir, %[3]s = today's note.
const policyHeader = `

## Persistent Memory

You have persistent memory stored as markdown files:
- %[1]s — curated long-term memory (stable facts, preferences, active projects, key decisions). Keep it lean (< ~200 lines).
- %[2]s/YYYY-MM-DD.md — append-only daily notes. Today's file: %[3]s

Rules:
1. MANDATORY: before answering questions about prior work, decisions, dates, people, preferences, or TODOs not visible in this conversation, search memory first: grep -ri "<keyword>" %[2]s %[1]s
2. When an important fact emerges (a decision, preference, project state change, follow-up), silently append a short bullet to %[3]s. Do not announce it or ask permission.
3. Periodically synthesize durable facts from daily notes into %[1]s; remove stale entries.
4. Never store secrets (tokens, passwords, keys) in memory files.
5. These EXACT absolute paths are the bot's memory store. Do NOT write these notes into any ~/.claude/projects/.../memory/ directory or other memory location.
`

// defaultFlushPrompt is sent as a user turn on the resumed session right
// before it is reset. Placeholders: %[1]s = today's note (absolute),
// %[2]s = MEMORY.md (absolute).
const defaultFlushPrompt = `[Memory flush — session is about to be reset; its context will be lost]
Review this conversation and append anything worth remembering to %[1]s (create it if missing):
decisions made, current task state and next steps, new facts about the user or projects, open TODOs.
Use short markdown bullets under a "## HH:MM" heading. Update %[2]s only if a durable long-term fact emerged.
Write ONLY to those exact absolute paths — not to any ~/.claude/projects/.../memory/ directory.
If nothing is worth saving, write nothing. Do not address the user. When done, reply with exactly: ` + NoReplySentinel + "\n"

// memorySeed is the initial MEMORY.md created by Bootstrap.
const memorySeed = `# MEMORY.md — Long-Term Memory

<!--
Curated long-term memory for the assistant. Keep it lean (< ~200 lines):
synthesize, don't transcribe. Detailed day-to-day observations belong in
memory/YYYY-MM-DD.md; promote only durable facts here and prune stale ones.
Sections you may want: User, Preferences, Active Projects, Decisions.
-->
`

// FlushPrompt returns the prompt for a silent memory-flush turn. A custom
// prompt from config may embed {today} which expands to the ABSOLUTE path of
// today's daily note.
func (m *Manager) FlushPrompt(now time.Time) string {
	if p := strings.TrimSpace(m.cfg.FlushPrompt); p != "" {
		return strings.ReplaceAll(p, "{today}", m.TodayNotePath(now))
	}
	return fmt.Sprintf(defaultFlushPrompt, m.TodayNotePath(now), m.MemoryFilePath())
}

// IsNoReply reports whether a flush reply is the bare NO_REPLY sentinel.
func (m *Manager) IsNoReply(reply string) bool {
	return strings.TrimSpace(reply) == NoReplySentinel
}
