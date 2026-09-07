//go:build windows

package daemon

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// findListenerPIDs returns PIDs of processes listening on the given TCP port.
//
// netstat is used rather than PowerShell's Get-NetTCPConnection so the lookup
// works without an interpreter startup, and without depending on PowerShell
// being on PATH in whatever context the gateway was launched from.
func findListenerPIDs(port int) ([]int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := execCommand(ctx, "netstat", "-a", "-n", "-o", "-p", "tcp")
	if err != nil {
		return nil, err
	}
	if result.ExitCode != 0 {
		return nil, fmt.Errorf("netstat exit %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
	}

	return parseNetstatListeners(result.Stdout, port), nil
}

// parseNetstatListeners pulls the PIDs listening on port out of netstat -ano
// output. Rows look like:
//
//	TCP    0.0.0.0:18789    0.0.0.0:0    LISTENING    12345
//	TCP    [::]:18789       [::]:0       LISTENING    12345
//
// Both IPv4 and IPv6 local addresses end in ":<port>", so a suffix match on
// the local-address column covers both without parsing the host part — and
// without matching a *remote* address on the same port, which is what makes
// the LISTENING check load-bearing rather than decorative.
func parseNetstatListeners(out string, port int) []int {
	suffix := ":" + strconv.Itoa(port)
	seen := make(map[int]bool)
	var pids []int

	for line := range strings.SplitSeq(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || !strings.EqualFold(fields[0], "TCP") {
			continue
		}
		if !strings.EqualFold(fields[3], "LISTENING") {
			continue
		}
		if !strings.HasSuffix(fields[1], suffix) {
			continue
		}
		pid, err := strconv.Atoi(fields[4])
		if err != nil || pid <= 0 || seen[pid] {
			continue
		}
		seen[pid] = true
		pids = append(pids, pid)
	}

	return pids
}

// terminatePID asks the process to close. Windows has no SIGTERM: taskkill
// without /F posts WM_CLOSE, which a console process with no message loop
// ignores. Those are picked up by the forced pass in KillStaleListeners.
func terminatePID(pid int) error { return taskkill(pid, false) }

// killPID kills the process and its children outright.
func killPID(pid int) error { return taskkill(pid, true) }

func taskkill(pid int, force bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// /T takes the process tree too: a stale gateway may still own CLI
	// subprocesses holding the socket open through handle inheritance.
	args := []string{"/PID", strconv.Itoa(pid), "/T"}
	if force {
		args = append(args, "/F")
	}
	res, err := execCommand(ctx, "taskkill", args...)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("taskkill %d (exit %d): %s", pid, res.ExitCode,
			strings.TrimSpace(res.Stdout+res.Stderr))
	}
	return nil
}
