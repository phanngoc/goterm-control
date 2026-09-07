//go:build !darwin && !windows

package tools

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// hostInfo reports what a Linux box can answer without extra packages.
// sysctl/sw_vers, which the macOS implementation reads, do not exist here —
// before the platform split every field came back as "(error: ...)".
func hostInfo(ctx context.Context) (string, error) {
	run := func(name string, args ...string) string {
		out, err := exec.CommandContext(ctx, name, args...).Output()
		if err != nil {
			return fmt.Sprintf("(error: %v)", err)
		}
		return strings.TrimRight(string(out), "\n")
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "🖥 System Information\n")
	fmt.Fprintf(&sb, "═══════════════════\n")
	fmt.Fprintf(&sb, "Host:       %s\n", run("hostname"))
	fmt.Fprintf(&sb, "Kernel:     %s\n", run("uname", "-srm"))
	fmt.Fprintf(&sb, "Uptime:     %s\n\n", strings.TrimSpace(run("uptime", "-p")))
	fmt.Fprintf(&sb, "🧠 Memory:\n%s\n\n", run("free", "-h"))
	fmt.Fprintf(&sb, "💾 Disk Usage:\n%s", run("df", "-h", "/"))

	return sb.String(), nil
}

func listProcesses(ctx context.Context, filter, sortBy string) (string, error) {
	// procps ps has no -r/-m; --sort is the portable way to order it.
	sortKey := "-pcpu"
	switch sortBy {
	case "memory":
		sortKey = "-pmem"
	case "pid":
		sortKey = "pid"
	}

	out, err := exec.CommandContext(ctx, "ps", "aux", "--sort", sortKey).Output()
	if err != nil {
		return "", fmt.Errorf("ps: %w", err)
	}

	return filterProcessTable(string(out), filter), nil
}

func killByPID(ctx context.Context, pid int, force bool) (string, error) {
	sig := "TERM"
	if force {
		sig = "KILL"
	}
	out, err := exec.CommandContext(ctx, "kill", "-"+sig, strconv.Itoa(pid)).CombinedOutput()
	if err != nil {
		return fmt.Sprintf("kill %d failed: %v\n%s", pid, err, out), nil
	}
	return fmt.Sprintf("Sent %s to PID %d", sig, pid), nil
}

func killByName(ctx context.Context, name string, force bool) (string, error) {
	sig := "TERM"
	if force {
		sig = "KILL"
	}
	out, err := exec.CommandContext(ctx, "pkill", "-"+sig, name).CombinedOutput()
	result := strings.TrimRight(string(out), "\n")
	if err != nil {
		return fmt.Sprintf("pkill %q failed: %v\n%s", name, err, result), nil
	}
	return fmt.Sprintf("Sent %s to all processes named %q", sig, name), nil
}
