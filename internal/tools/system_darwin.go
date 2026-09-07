//go:build darwin

package tools

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

func hostInfo(ctx context.Context) (string, error) {
	run := func(name string, args ...string) string {
		out, err := exec.CommandContext(ctx, name, args...).Output()
		if err != nil {
			return fmt.Sprintf("(error: %v)", err)
		}
		return strings.TrimRight(string(out), "\n")
	}

	model := run("sysctl", "-n", "hw.model")
	osName := run("sw_vers", "-productName")
	osVersion := run("sw_vers", "-productVersion")
	osBuild := run("sw_vers", "-buildVersion")
	cpuBrand := run("sysctl", "-n", "machdep.cpu.brand_string")
	cpuCores := run("sysctl", "-n", "hw.physicalcpu")
	cpuThreads := run("sysctl", "-n", "hw.logicalcpu")
	memBytes := run("sysctl", "-n", "hw.memsize")
	uptime := run("uptime")

	memGB := ""
	if b, err := strconv.ParseInt(strings.TrimSpace(memBytes), 10, 64); err == nil {
		memGB = fmt.Sprintf("%.1f GB", float64(b)/1024/1024/1024)
	}

	disk := run("df", "-h", "/")

	var sb strings.Builder
	fmt.Fprintf(&sb, "🖥 System Information\n")
	fmt.Fprintf(&sb, "═══════════════════\n")
	fmt.Fprintf(&sb, "Model:      %s\n", model)
	fmt.Fprintf(&sb, "OS:         %s %s (%s)\n", osName, osVersion, osBuild)
	fmt.Fprintf(&sb, "CPU:        %s\n", cpuBrand)
	fmt.Fprintf(&sb, "Cores:      %s physical / %s logical\n", cpuCores, cpuThreads)
	fmt.Fprintf(&sb, "Memory:     %s\n", memGB)
	fmt.Fprintf(&sb, "Uptime:     %s\n\n", strings.TrimSpace(uptime))
	fmt.Fprintf(&sb, "💾 Disk Usage:\n%s", disk)

	return sb.String(), nil
}

func listProcesses(ctx context.Context, filter, sortBy string) (string, error) {
	// ps -r sorts by CPU, -m by memory; neither sorts by PID, which is the
	// order ps prints anyway.
	sortFlag := "-r"
	switch sortBy {
	case "memory":
		sortFlag = "-m"
	case "pid":
		sortFlag = ""
	}

	var cmd *exec.Cmd
	if sortFlag != "" {
		cmd = exec.CommandContext(ctx, "ps", "aux", sortFlag)
	} else {
		cmd = exec.CommandContext(ctx, "ps", "aux")
	}

	out, err := cmd.Output()
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
