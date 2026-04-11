package daemon

import (
	"net"
	"testing"
)

func TestKillStaleListeners_NoStale(t *testing.T) {
	// Pick a random free port
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close() // port is now free

	// Should return nil immediately (fast path)
	if err := KillStaleListeners(port); err != nil {
		t.Fatalf("expected nil error for free port, got: %v", err)
	}
}

func TestIsPortOccupied(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	port := ln.Addr().(*net.TCPAddr).Port
	if !isPortOccupied(port) {
		t.Fatal("expected port to be occupied")
	}

	ln.Close()
	if isPortOccupied(port) {
		t.Fatal("expected port to be free after close")
	}
}
