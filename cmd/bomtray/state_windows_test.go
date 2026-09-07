//go:build windows

package main

import (
	"testing"
	"unicode/utf16"

	"github.com/ngocp/goterm-control/internal/gateway"
)

// Unlike launchd, the Windows backend registers one fixed task path, so the
// agent id does not select anything. This test exists to pin that down: if
// per-agent task names are ever added in internal/daemon, this is what has to
// change with them.
func TestServiceLabelForIsTheFixedTaskPath(t *testing.T) {
	const want = `\BomClaw\bomclaw-gateway`
	for _, id := range []string{"bomclaw", "bomclaw2", ""} {
		if got := serviceLabelFor(id); got != want {
			t.Errorf("serviceLabelFor(%q) = %q, want %q", id, got, want)
		}
	}

	// Reached through an agent, an unknown id still yields a usable target —
	// the Windows path has nothing to be unsure about.
	ag := &agent{url: "http://127.0.0.1:18789"}
	ag.st = &gateway.StatusResult{}
	if got := ag.serviceTarget(""); got != want {
		t.Errorf("serviceTarget = %q, want %q", got, want)
	}
}

func TestTrimToUTF16(t *testing.T) {
	cases := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"short ascii is untouched", "bomclaw", 127, "bomclaw"},
		{"exact fit", "abcd", 4, "abcd"},
		{"ascii is cut", "abcdef", 3, "abc"},
		// One rune, two UTF-16 units: with room for only one it must be
		// dropped whole, because half a surrogate pair is invalid text.
		{"a surrogate pair is not split", "a\U0001F980", 2, "a"},
		{"a surrogate pair that fits is kept", "a\U0001F980", 3, "a\U0001F980"},
		// Vietnamese is one UTF-16 unit per rune but multi-byte in UTF-8, so a
		// byte-based limit would cut it far too short.
		{"multi-byte runes count as one unit", "Đánh giá", 8, "Đánh giá"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := trimToUTF16(c.in, c.n)
			if got != c.want {
				t.Errorf("trimToUTF16(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
			}
			if n := len(utf16.Encode([]rune(got))); n > c.n {
				t.Errorf("result is %d UTF-16 units, over the %d cap", n, c.n)
			}
		})
	}
}

// Shell_NotifyIcon rejects an over-long tooltip outright rather than trimming
// it, leaving the old value in place — so the cap has to be enforced here.
func TestSetTrayPresentationCapsTheTooltip(t *testing.T) {
	long := ""
	for range 200 {
		long += "x"
	}
	if got := trimToUTF16(long, tooltipMax); len(got) != tooltipMax {
		t.Errorf("tooltip trimmed to %d, want %d", len(got), tooltipMax)
	}
}
