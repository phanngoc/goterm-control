package browser

import (
	"encoding/json"
	"fmt"
	"strings"
)

// SnapshotNode is one element of a DOM snapshot. Both backends produce it:
// the in-page script run over CDP in the managed Chrome, and the Browser
// Bridge extension in the user's own browser. Ref is what the agent quotes
// back ("click n12"), so it must be the DFS position of the element and
// nothing else.
type SnapshotNode struct {
	Ref   string `json:"ref"`
	Depth int    `json:"depth"`
	Tag   string `json:"tag"`
	ID    string `json:"id,omitempty"`
	Role  string `json:"role,omitempty"`
	Name  string `json:"name,omitempty"`
	Text  string `json:"text,omitempty"`
	Href  string `json:"href,omitempty"`
	Type  string `json:"type,omitempty"`
	Value string `json:"value,omitempty"`
}

// FormatSnapshot renders nodes as the indented text tree the agent reads.
func FormatSnapshot(nodes []SnapshotNode) string {
	var b strings.Builder
	for _, n := range nodes {
		indent := strings.Repeat("  ", n.Depth)
		fmt.Fprintf(&b, "%s[%s] <%s>", indent, n.Ref, n.Tag)
		if n.Role != "" {
			fmt.Fprintf(&b, " role=%s", n.Role)
		}
		if n.Name != "" {
			fmt.Fprintf(&b, " %q", n.Name)
		}
		if n.ID != "" {
			fmt.Fprintf(&b, " #%s", n.ID)
		}
		if n.Type != "" {
			fmt.Fprintf(&b, " type=%s", n.Type)
		}
		if n.Value != "" {
			fmt.Fprintf(&b, " value=%q", n.Value)
		}
		if n.Href != "" {
			fmt.Fprintf(&b, " href=%s", n.Href)
		}
		if n.Text != "" && len(n.Text) < 80 {
			fmt.Fprintf(&b, " %q", n.Text)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// FormatSnapshotJSON formats a snapshot given as {"nodes":[…]} or a bare
// array of nodes.
func FormatSnapshotJSON(raw []byte) (string, error) {
	var wrapped struct {
		Nodes []SnapshotNode `json:"nodes"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil && wrapped.Nodes != nil {
		return FormatSnapshot(wrapped.Nodes), nil
	}
	var nodes []SnapshotNode
	if err := json.Unmarshal(raw, &nodes); err != nil {
		return "", fmt.Errorf("snapshot is not a node list: %w", err)
	}
	return FormatSnapshot(nodes), nil
}
