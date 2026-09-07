//go:build darwin

package main

import (
	"testing"

	"github.com/ngocp/goterm-control/internal/gateway"
)

// The tray used to carry a single hardcoded launchd label that matched no
// installed service, so "Restart Gateway" silently did nothing. The label now
// comes from the agent id the gateway reports.
func TestServiceTargetIsTheLaunchdLabel(t *testing.T) {
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
			if got := ag.serviceTarget(c.override); got != c.want {
				t.Errorf("serviceTarget = %q, want %q", got, c.want)
			}
		})
	}
}

func TestXMLEscape(t *testing.T) {
	if got := xmlEscape(`a&b<c>"d"`); got != "a&amp;b&lt;c&gt;&quot;d&quot;" {
		t.Errorf("xmlEscape = %q", got)
	}
}
