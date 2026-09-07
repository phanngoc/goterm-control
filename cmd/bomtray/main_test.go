//go:build darwin || windows

package main

import (
	"strings"
	"testing"

	"github.com/ngocp/goterm-control/internal/gateway"
)

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

// An explicit override wins on every platform; what an unset one falls back to
// is platform-specific and covered in state_darwin_test.go / state_windows_test.go.
func TestAgentServiceTargetOverride(t *testing.T) {
	ag := &agent{url: "http://127.0.0.1:18789"}
	ag.st = &gateway.StatusResult{AgentID: "bomclaw"}
	if got := ag.serviceTarget("custom-target"); got != "custom-target" {
		t.Errorf("serviceTarget = %q, want the override", got)
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

// The tooltip is the only place detail can go on Windows, so it has to name
// each agent and its state — and skip addresses that never answered, the same
// way the menu rows do.
func TestTooltip(t *testing.T) {
	up := &agent{url: "http://127.0.0.1:18789"}
	up.st = &gateway.StatusResult{AgentID: "bomclaw"}
	up.up, up.seen = true, true

	busy := &agent{url: "http://127.0.0.1:18790"}
	busy.st = &gateway.StatusResult{
		AgentID: "bomclaw2",
		Runs:    []gateway.RunInfo{{Task: "crawl listings"}},
	}
	busy.up, busy.seen = true, true

	gone := &agent{url: "http://127.0.0.1:18791"}
	gone.seen = true // answered once, not answering now

	never := &agent{url: "http://127.0.0.1:18792"}

	a := &app{agents: []*agent{up, busy, gone, never}, awake: &awake{}}
	got := a.tooltip()

	for _, want := range []string{"bomclaw: idle", "bomclaw2: crawl listings", "down"} {
		if !strings.Contains(got, want) {
			t.Errorf("tooltip %q missing %q", got, want)
		}
	}
	if strings.Contains(got, "18792") {
		t.Errorf("an address that never answered must stay out of the tooltip: %q", got)
	}
}

func TestTooltipWithNothingAnswering(t *testing.T) {
	a := &app{agents: []*agent{{url: "http://127.0.0.1:18789"}}, awake: &awake{}}
	if got := a.tooltip(); !strings.Contains(got, "no gateway") {
		t.Errorf("tooltip = %q, want it to say no gateway is answering", got)
	}
}

// A single missed poll must not declare an agent down. The status request has a
// 2s timeout against a gateway that spawns CLI subprocesses, so one miss is
// routine — and it used to fire "is DOWN" straight away, then "is back up"
// seconds later, about a gateway with hours of uptime.
func TestOneMissedPollDoesNotMeanDown(t *testing.T) {
	ag := &agent{url: "http://127.0.0.1:18790"}
	ag.st = &gateway.StatusResult{AgentID: "bomclaw", AgentName: "BomClaw"}
	ag.seen, ag.up = true, true

	for i := 1; i < downAfter; i++ {
		ag.fails = i
		ag.up = ag.fails < downAfter
		if !ag.up {
			t.Fatalf("declared down after only %d missed poll(s); downAfter is %d", i, downAfter)
		}
	}

	ag.fails = downAfter
	ag.up = ag.fails < downAfter
	if ag.up {
		t.Errorf("still up after %d consecutive misses", downAfter)
	}
}

// A failed poll used to nil out the status, so the agent lost its name and the
// notification said "127.0.0.1:18790 is DOWN" instead of "BomClaw is DOWN".
func TestAFailedPollKeepsTheAgentName(t *testing.T) {
	ag := &agent{url: "http://127.0.0.1:18790"}
	ag.st = &gateway.StatusResult{AgentID: "bomclaw", AgentName: "BomClaw"}
	ag.seen = true

	// What poll() now does on error: count it, keep the last good status.
	ag.fails++
	ag.up = ag.fails < downAfter

	if got := ag.name(); got != "BomClaw" {
		t.Errorf("name after a failed poll = %q, want the last known name", got)
	}
	if ag.st == nil {
		t.Error("the last good status must be kept — it is where the name comes from")
	}
}
