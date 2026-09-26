package chat

import "strings"

// Showing an answer as it forms.
//
// The shape is openclaw's (src/config/types.base.ts, BlockStreamingChunkConfig):
// a preview updated in place, and the decision to update is a CHARACTER
// threshold with a break preference — not a timer. A timer fires mid-word as
// often as not, and a reader watching a sentence appear one fragment at a time
// learns less than one who sees a paragraph land whole.
type StreamPreview struct {
	// MinGrowth is how much new text must arrive before it is worth showing
	// again. Below this the reader gains nothing and the database takes a
	// write per token.
	MinGrowth int
	// MaxRunes bounds what is shown; the tail is kept, because the end is
	// where the answer currently is.
	MaxRunes int

	shownLen int
}

// DefaultPreview is tuned for a chat thread: roughly a sentence of growth
// before an update, and a paragraph or two on screen.
func DefaultPreview() *StreamPreview { return &StreamPreview{MinGrowth: 120, MaxRunes: 600} }

// Ready reports whether the reply has grown enough to redraw, and records that
// it was. Call it once per candidate update.
func (p *StreamPreview) Ready(reply string) bool {
	if p == nil {
		return false
	}
	n := len([]rune(reply))
	if n-p.shownLen < p.MinGrowth {
		return false
	}
	p.shownLen = n
	return true
}

// Cut returns the part of a forming reply worth showing: the tail, trimmed
// back to a boundary so it never begins or ends mid-word.
//
// Preference order is openclaw's — paragraph, then line, then sentence —
// because a cut at a paragraph reads as a pause and a cut mid-sentence reads
// as a bug.
func (p *StreamPreview) Cut(reply string) string {
	max := 600
	if p != nil && p.MaxRunes > 0 {
		max = p.MaxRunes
	}
	r := []rune(strings.TrimSpace(reply))
	if len(r) <= max {
		return string(r)
	}
	tail := string(r[len(r)-max:])
	// Start the visible part at a boundary rather than wherever the cut landed.
	for _, sep := range []string{"\n\n", "\n", ". "} {
		if i := strings.Index(tail, sep); i >= 0 && i < len(tail)/2 {
			return "…" + strings.TrimSpace(tail[i+len(sep):])
		}
	}
	return "…" + tail
}
