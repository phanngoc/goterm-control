package daemon

import (
	"context"
	"strings"
	"testing"
)

// Revive reaches straight into the service manager, so it has to be sure the
// agent it is about to start is one this machine actually runs. The unit file
// is that proof. Without this check the call would fail somewhere inside
// launchctl or systemctl, naming a service label nobody has ever seen, for an
// agent that is simply registered from another host.
func TestRevivingAnAgentThisMachineDoesNotRunSaysSo(t *testing.T) {
	err := Revive(context.Background(), "bomclaw-no-such-agent")
	if err == nil {
		t.Fatal("claimed to start an agent with no service on this machine")
	}
	if !strings.Contains(err.Error(), "bomclaw-no-such-agent") {
		t.Fatalf("the agent was not named: %v", err)
	}
	if !strings.Contains(err.Error(), "this machine") {
		t.Fatalf("the reason was not the real one: %v", err)
	}
}
