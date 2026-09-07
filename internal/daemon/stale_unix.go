//go:build !windows

package daemon

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"syscall"
	"time"
)

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

// terminatePID asks the process to shut down cleanly.
func terminatePID(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) }

// killPID kills the process outright.
func killPID(pid int) error { return syscall.Kill(pid, syscall.SIGKILL) }
