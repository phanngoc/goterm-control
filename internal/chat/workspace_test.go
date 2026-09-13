package chat

import (
	"testing"

	"github.com/ngocp/goterm-control/internal/session"
)

// A chat has no project and runs where the agent lives.
func TestAPlainSessionRunsInTheAgentsWorkspace(t *testing.T) {
	s := session.New(1)
	if got := WorkspaceFor(s, "/home/agent/work"); got != "/home/agent/work" {
		t.Fatalf("got %q", got)
	}
}

// A turn answering a project runs in that project's folder — otherwise the
// work lands in whichever agent took the turn, and the next person looking for
// it has three places to search and no way to know which.
func TestAProjectSessionRunsInTheProjectFolder(t *testing.T) {
	s := session.New(1)
	s.SetWorkspace("/home/projects/trading")
	if got := WorkspaceFor(s, "/home/agent/work"); got != "/home/projects/trading" {
		t.Fatalf("the project folder lost to the agent's own: %q", got)
	}
}

// Nil and empty are the same answer: the fallback. A client calls this before
// it has anything else to go on.
func TestNoSessionFallsBack(t *testing.T) {
	if got := WorkspaceFor(nil, "/home/agent/work"); got != "/home/agent/work" {
		t.Fatalf("got %q", got)
	}
	if got := WorkspaceFor(session.New(1), ""); got != "" {
		t.Fatalf("an unset workspace invented one: %q", got)
	}
}
