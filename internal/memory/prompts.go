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
// block. The %s placeholder is today's workspace-relative note path.
const policyHeader = `

## Persistent Memory

You have persistent memory stored as markdown in your workspace:
- MEMORY.md — curated long-term memory (stable facts, preferences, active projects, key decisions). Keep it lean (< ~200 lines).
- memory/YYYY-MM-DD.md — append-only daily notes. Today's file: %s

Rules:
1. MANDATORY: before answering questions about prior work, decisions, dates, people, preferences, or TODOs not visible in this conversation, search memory first: grep -ri "<keyword>" memory/ MEMORY.md
2. When an important fact emerges (a decision, preference, project state change, follow-up), silently append a short bullet to today's note. Do not announce it or ask permission.
3. Periodically synthesize durable facts from daily notes into MEMORY.md; remove stale entries.
4. Never store secrets (tokens, passwords, keys) in memory files.
`

// defaultFlushPrompt is sent as a user turn on the resumed session right
// before it is reset. The %s placeholders are today's relative note path.
const defaultFlushPrompt = `[Memory flush — session is about to be reset; its context will be lost]
Review this conversation and append anything worth remembering to %s:
decisions made, current task state and next steps, new facts about the user or projects, open TODOs.
Use short markdown bullets under a "## HH:MM" heading. Update MEMORY.md only if a durable long-term fact emerged.
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
// prompt from config may embed {today} which expands to the relative path of
// today's daily note.
func (m *Manager) FlushPrompt(now time.Time) string {
	if p := strings.TrimSpace(m.cfg.FlushPrompt); p != "" {
		return strings.ReplaceAll(p, "{today}", m.relNote(now))
	}
	return fmt.Sprintf(defaultFlushPrompt, m.relNote(now))
}

// IsNoReply reports whether a flush reply is the bare NO_REPLY sentinel.
func (m *Manager) IsNoReply(reply string) bool {
	return strings.TrimSpace(reply) == NoReplySentinel
}
