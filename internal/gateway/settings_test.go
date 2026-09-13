package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

// A peer that is down must appear with the reason, not vanish: an agent
// missing from the settings list reads as an agent that does not exist, and
// the person goes looking for a bug that is not there.
func TestAnUnreachablePeerIsListedNotDropped(t *testing.T) {
	s := peerSettings(nil, "bomclaw9", "Agent 9", "ws://127.0.0.1:1/ws")
	if s.AgentID != "bomclaw9" || s.AgentName != "Agent 9" {
		t.Fatalf("identity lost: %+v", s)
	}
	if s.Reachable {
		t.Fatal("a dead peer reported itself reachable")
	}
	if s.Error == "" {
		t.Fatal("unreachable with no reason given")
	}
}

// A peer with a malformed address fails the same way — described, not dropped.
func TestABadPeerAddressIsDescribed(t *testing.T) {
	s := peerSettings(nil, "bomclaw9", "Agent 9", "not-a-url")
	if s.Reachable || !strings.Contains(s.Error, "not-a-url") {
		t.Fatalf("expected the bad address in the reason: %+v", s)
	}
}

// Routing: a change for this agent stays here rather than going out over the
// network to itself.
func TestSetModelForSelfDoesNotGoOverTheWire(t *testing.T) {
	deps := Deps{AgentID: "bomclaw"} // no ConfigPath: the local handler must be the one to complain
	_, err := handleAdminSetModelOn(deps, json.RawMessage(`{"agent_id":"bomclaw","model":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "config path") {
		t.Fatalf("expected the local handler's error, got %v", err)
	}
}
