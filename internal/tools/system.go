package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type SystemTool struct{}

func (s *SystemTool) Info(_ context.Context, _ json.RawMessage) (string, error) {
	var sb strings.Builder

	run := func(name string, args ...string) string {
		out, err := exec.Command(name, args...).Output()
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

	// Disk usage
	disk := run("df", "-h", "/")

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

type listProcessInput struct {
	Filter string `json:"filter"`
	SortBy string `json:"sort_by"`
}

func (s *SystemTool) Processes(_ context.Context, raw json.RawMessage) (string, error) {
	var inp listProcessInput
	if err := json.Unmarshal(raw, &inp); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	// ps with cpu and memory sort
	sortFlag := "-r" // by CPU by default (ps -r sorts by CPU)
	if inp.SortBy == "memory" {
		sortFlag = "-m"
	} else if inp.SortBy == "pid" {
		sortFlag = ""
	}

	var cmd *exec.Cmd
	if sortFlag != "" {
		cmd = exec.Command("ps", "aux", sortFlag)
	} else {
		cmd = exec.Command("ps", "aux")
	}

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("ps: %w", err)
	}

	lines := strings.Split(string(out), "\n")
	if len(lines) == 0 {
		return "No processes found", nil
	}

	// Header line
	result := lines[0] + "\n"
	count := 0
	for _, line := range lines[1:] {
		if line == "" {
			continue
		}
		if inp.Filter != "" && !strings.Contains(strings.ToLower(line), strings.ToLower(inp.Filter)) {
			continue
		}
		result += line + "\n"
		count++
		if count >= 30 && inp.Filter == "" {
			result += fmt.Sprintf("... (showing top 30 of %d processes)", len(lines)-1)
			break
		}
	}

	return strings.TrimRight(result, "\n"), nil
}

type killInput struct {
	PID    int    `json:"pid"`
	Name   string `json:"name"`
	Signal string `json:"signal"`
}

func (s *SystemTool) Kill(ctx context.Context, raw json.RawMessage) (string, error) {
	var inp killInput
	if err := json.Unmarshal(raw, &inp); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	sig := "TERM"
	if inp.Signal == "KILL" {
		sig = "KILL"
	}

	if inp.PID > 0 {
		cmd := exec.CommandContext(ctx, "kill", "-"+sig, strconv.Itoa(inp.PID))
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Sprintf("kill %d failed: %v\n%s", inp.PID, err, out), nil
		}
		return fmt.Sprintf("Sent %s to PID %d", sig, inp.PID), nil
	}

	if inp.Name != "" {
		cmd := exec.CommandContext(ctx, "pkill", "-"+sig, inp.Name)
		out, err := cmd.CombinedOutput()
		result := strings.TrimRight(string(out), "\n")
		if err != nil {
			return fmt.Sprintf("pkill %q failed: %v\n%s", inp.Name, err, result), nil
		}
		return fmt.Sprintf("Sent %s to all processes named %q", sig, inp.Name), nil
	}

	return "", fmt.Errorf("provide either pid or name")
}
