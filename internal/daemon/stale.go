package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// KillStaleListeners detects and kills any process holding the given TCP port.
// This prevents "address already in use" errors when launchd/systemd auto-restarts
// the gateway after a crash while the old socket is still in TIME_WAIT.
func KillStaleListeners(port int) error {
	if !isPortOccupied(port) {
		return nil
	}

	pids, err := findListenerPIDs(port)
	if err != nil {
		return fmt.Errorf("lsof: %w", err)
	}
	if len(pids) == 0 {
		return nil
	}

	self := os.Getpid()
	for _, pid := range pids {
		if pid == self {
			continue
		}
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}

	// Wait for port to free up (SIGTERM grace period)
	if waitPortFree(port, 3*time.Second) {
		return nil
	}

	// Escalate to SIGKILL
	for _, pid := range pids {
		if pid == self {
			continue
		}
		_ = syscall.Kill(pid, syscall.SIGKILL)
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

// findListenerPIDs returns PIDs of processes listening on the given TCP port.
func findListenerPIDs(port int) ([]int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := execCommand(ctx, "lsof", "-ti", fmt.Sprintf("tcp:%d", port))
	if err != nil {
		return nil, err
	}
	if result.ExitCode != 0 || strings.TrimSpace(result.Stdout) == "" {
		return nil, nil
	}

	var pids []int
	for line := range strings.SplitSeq(strings.TrimSpace(result.Stdout), "\n") {
		line = strings.TrimSpace(line)
		if pid, err := strconv.Atoi(line); err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids, nil
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
