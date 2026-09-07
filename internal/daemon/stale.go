package daemon

import (
	"fmt"
	"net"
	"os"
	"time"
)

// KillStaleListeners detects and kills any process holding the given TCP port.
// This prevents "address already in use" errors when the service manager
// auto-restarts the gateway after a crash while the old socket is still bound.
//
// Finding and signalling the holder is platform-specific (see stale_unix.go
// and stale_windows.go); the escalation policy below is shared.
func KillStaleListeners(port int) error {
	if !isPortOccupied(port) {
		return nil
	}

	pids, err := findListenerPIDs(port)
	if err != nil {
		return fmt.Errorf("find listeners on port %d: %w", port, err)
	}
	if len(pids) == 0 {
		return nil
	}

	self := os.Getpid()
	for _, pid := range pids {
		if pid == self {
			continue
		}
		_ = terminatePID(pid)
	}

	// Wait for port to free up (graceful shutdown grace period)
	if waitPortFree(port, 3*time.Second) {
		return nil
	}

	// Escalate to a forced kill
	for _, pid := range pids {
		if pid == self {
			continue
		}
		_ = killPID(pid)
	}

	if waitPortFree(port, 2*time.Second) {
		return nil
	}

	return fmt.Errorf("port %d still occupied after killing stale processes", port)
}

// isPortOccupied returns true if something is listening on the port.
func isPortOccupied(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// waitPortFree polls until the port is free or the timeout elapses.
func waitPortFree(port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !isPortOccupied(port) {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}
