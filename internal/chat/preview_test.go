package chat

import (
	"strings"
	"testing"
)

// The threshold is what stops a database write per token telling the reader
// something they cannot read that fast.
func TestPreviewWaitsForEnoughNewText(t *testing.T) {
	p := &StreamPreview{MinGrowth: 10, MaxRunes: 100}
	if p.Ready("short") {
		t.Error("redrew after five characters")
	}
	if !p.Ready(strings.Repeat("a", 10)) {
		t.Error("did not redraw after the threshold was passed")
	}
	// And having drawn it, it waits again rather than redrawing on every call.
	if p.Ready(strings.Repeat("a", 12)) {
		t.Error("redrew after two more characters")
	}
	if !p.Ready(strings.Repeat("a", 25)) {
		t.Error("did not redraw after another threshold of growth")
	}
}

// A cut mid-sentence reads as a bug; a cut at a paragraph reads as a pause.
func TestCutPrefersAParagraphBoundary(t *testing.T) {
	p := &StreamPreview{MaxRunes: 40}
	long := "phần mở đầu đã cũ và dài dòng lắm rồi\n\nĐoạn mới bắt đầu ở đây."
	got := p.Cut(long)
	if !strings.HasPrefix(got, "…") {
		t.Errorf("a trimmed preview should say so: %q", got)
	}
	if !strings.Contains(got, "Đoạn mới bắt đầu") {
		t.Errorf("the newest paragraph is what the reader wants: %q", got)
	}
	if strings.Contains(got, "phần mở đầu") {
		t.Errorf("the cut kept text it should have dropped: %q", got)
	}
}

// Short replies are shown whole, with no ellipsis pretending something is
// missing.
func TestShortRepliesAreNotCut(t *testing.T) {
	p := DefaultPreview()
	if got := p.Cut("xong rồi"); got != "xong rồi" {
		t.Errorf("got %q", got)
	}
}

// Multi-byte text must not be cut by bytes: Vietnamese would lose its
// diacritics at the boundary.
func TestCutCountsRunesNotBytes(t *testing.T) {
	p := &StreamPreview{MaxRunes: 10}
	got := p.Cut(strings.Repeat("ế", 40))
	if n := len([]rune(strings.TrimPrefix(got, "…"))); n > 10 {
		t.Fatalf("kept %d runes, cap is 10", n)
	}
	if strings.Contains(got, "�") {
		t.Fatal("the cut split a character")
	}
}
