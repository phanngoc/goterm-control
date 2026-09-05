//go:build darwin

package main

import (
	"testing"

	"github.com/ngocp/goterm-control/internal/gateway"
)

// The tray used to carry a single hardcoded launchd label that matched no
// installed service, so "Restart Gateway" silently did nothing. The label now
// comes from the agent id the gateway reports.
func TestAgentLaunchdLabel(t *testing.T) {
	cases := []struct {
		name     string
		id       string
		override string
		want     string
	}{
		{"derived from the agent id", "bomclaw", "", "com.bomclaw.gateway"},
		{"second agent gets its own label", "bomclaw2", "", "com.bomclaw2.gateway"},
		{"an explicit override wins", "bomclaw", "com.custom.gateway", "com.custom.gateway"},
		// Empty rather than a guess: restarting the wrong service is worse
		// than telling the user the label is unknown.
		{"unknown id yields no label", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ag := &agent{url: "http://127.0.0.1:18789"}
			if c.id != "" {
				ag.st = &gateway.StatusResult{AgentID: c.id}
			}
			if got := ag.launchdLabel(c.override); got != c.want {
				t.Errorf("launchdLabel = %q, want %q", got, c.want)
			}
		})
	}
}

// With two agents in one menu, every row has to say which agent it is.
func TestAgentName(t *testing.T) {
	ag := &agent{url: "http://127.0.0.1:18790"}
	if got := ag.name(); got != "127.0.0.1:18790" {
		t.Errorf("before the gateway answers, fall back to host:port; got %q", got)
	}

	ag.st = &gateway.StatusResult{AgentID: "bomclaw2"}
	if got := ag.name(); got != "bomclaw2" {
		t.Errorf("id is used when there is no display name; got %q", got)
	}

	ag.st.AgentName = "BomClaw (agent 2)"
	if got := ag.name(); got != "BomClaw (agent 2)" {
		t.Errorf("display name wins; got %q", got)
	}
}

func TestShortModel(t *testing.T) {
	cases := map[string]string{
		"claude-opus-5": "opus-5",
		"gpt-6-astra":   "gpt-6-astra",
		"":              "",
	}
	for in, want := range cases {
		if got := shortModel(in); got != want {
			t.Errorf("shortModel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncateIsRuneSafe(t *testing.T) {
	// Vietnamese task labels are common here; byte slicing would split them.
	got := truncate("Đánh giá đất đầu tư Hòa Hiệp Nam", 10)
	if []rune(got)[len([]rune(got))-1] != '…' {
		t.Errorf("expected an ellipsis, got %q", got)
	}
	if n := len([]rune(got)); n != 10 {
		t.Errorf("expected 10 runes, got %d (%q)", n, got)
	}
	if truncate("short", 10) != "short" {
		t.Error("a short string must be returned unchanged")
	}
}

func TestShortURL(t *testing.T) {
	for in, want := range map[string]string{
		"http://127.0.0.1:18789":  "127.0.0.1:18789",
		"https://example.com/":    "example.com",
		"http://127.0.0.1:18790/": "127.0.0.1:18790",
	} {
		if got := shortURL(in); got != want {
			t.Errorf("shortURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestXMLEscape(t *testing.T) {
	if got := xmlEscape(`a&b<c>"d"`); got != "a&amp;b&lt;c&gt;&quot;d&quot;" {
		t.Errorf("xmlEscape = %q", got)
	}
}
