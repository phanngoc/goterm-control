package memory

import (
	"fmt"
	"strings"
)

// BuildMemoryContext searches the memory store for entries relevant to the user's
// message and formats them as a section to inject into the system prompt or
// prepend to the user message.
func BuildMemoryContext(store MemoryBackend, userMessage string, maxEntries int) string {
	if store == nil || userMessage == "" {
		return ""
	}
	if maxEntries <= 0 {
		maxEntries = 5
	}

	entries, err := store.Search(userMessage, maxEntries)
	if err != nil || len(entries) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n\n## Relevant Memory from Past Conversations\n\n")

	for i, e := range entries {
		sb.WriteString(fmt.Sprintf("**Memory %d** (session: %s)\n", i+1, e.SessionID))
		if e.Summary != "" {
			sb.WriteString(fmt.Sprintf("- Summary: %s\n", e.Summary))
		}
		if len(e.Facts) > 0 {
			for _, f := range e.Facts {
				sb.WriteString(fmt.Sprintf("  - %s\n", f))
			}
		}
		sb.WriteString("\n")
	}

	return sb.String()
}
