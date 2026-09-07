//go:build windows

package tools

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// hostInfo reports the same fields as the macOS implementation, in the same
// layout, so a Telegram reply looks the same whichever machine answers it.
//
// The values come back from PowerShell as key=value lines rather than
// pre-formatted text: CIM property names are stable, whereas the console
// output of systeminfo is localised.
func hostInfo(ctx context.Context) (string, error) {
	script := strings.Join([]string{
		"$os = Get-CimInstance Win32_OperatingSystem",
		"$cs = Get-CimInstance Win32_ComputerSystem",
		"$cpu = @(Get-CimInstance Win32_Processor)[0]",
		`"model=$($cs.Manufacturer) $($cs.Model)"`,
		`"osname=$($os.Caption)"`,
		`"osversion=$($os.Version)"`,
		`"osbuild=$($os.BuildNumber)"`,
		`"cpu=$($cpu.Name)"`,
		`"cores=$($cpu.NumberOfCores)"`,
		`"threads=$($cpu.NumberOfLogicalProcessors)"`,
		`"membytes=$($cs.TotalPhysicalMemory)"`,
		`"uptimesec=$([int]((Get-Date) - $os.LastBootUpTime).TotalSeconds)"`,
		`Get-CimInstance Win32_LogicalDisk -Filter "DriveType=3" | ForEach-Object { ` +
			`"disk=$($_.DeviceID) $([math]::Round($_.Size/1GB,1))G total, ` +
			`$([math]::Round($_.FreeSpace/1GB,1))G free" }`,
	}, "; ")

	out, err := psRun(ctx, script)
	if err != nil {
		return "", fmt.Errorf("system info: %w\n%s", err, out)
	}

	f := map[string]string{}
	var disks []string
	for _, line := range strings.Split(out, "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		if key == "disk" {
			disks = append(disks, val)
			continue
		}
		f[key] = val
	}

	memGB := ""
	if b, err := strconv.ParseInt(f["membytes"], 10, 64); err == nil {
		memGB = fmt.Sprintf("%.1f GB", float64(b)/1024/1024/1024)
	}

	uptime := ""
	if secs, err := strconv.Atoi(f["uptimesec"]); err == nil {
		uptime = (time.Duration(secs) * time.Second).Round(time.Minute).String()
	}

	diskText := "(no fixed disks reported)"
	if len(disks) > 0 {
		diskText = strings.Join(disks, "\n")
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "🖥 System Information\n")
	fmt.Fprintf(&sb, "═══════════════════\n")
	fmt.Fprintf(&sb, "Model:      %s\n", strings.TrimSpace(f["model"]))
	fmt.Fprintf(&sb, "OS:         %s %s (build %s)\n", f["osname"], f["osversion"], f["osbuild"])
	fmt.Fprintf(&sb, "CPU:        %s\n", f["cpu"])
	fmt.Fprintf(&sb, "Cores:      %s physical / %s logical\n", f["cores"], f["threads"])
	fmt.Fprintf(&sb, "Memory:     %s\n", memGB)
	fmt.Fprintf(&sb, "Uptime:     %s\n\n", uptime)
	fmt.Fprintf(&sb, "💾 Disk Usage:\n%s", diskText)

	return sb.String(), nil
}

// listProcesses prints a fixed-width table. Format-Table is deliberately not
// used: it prefixes a blank line and a dashed rule, and filterProcessTable
// treats the first line as the header.
func listProcesses(ctx context.Context, filter, sortBy string) (string, error) {
	sortExpr := "Sort-Object CPU -Descending"
	switch sortBy {
	case "memory":
		sortExpr = "Sort-Object WorkingSet64 -Descending"
	case "pid":
		sortExpr = "Sort-Object Id"
	}

	script := strings.Join([]string{
		`"{0,8}  {1,10}  {2,9}  {3}" -f 'PID','CPU(s)','MEM(MB)','NAME'`,
		"Get-Process | " + sortExpr + " | ForEach-Object { " +
			`"{0,8}  {1,10:N1}  {2,9:N1}  {3}" -f ` +
			"$_.Id, $_.CPU, ($_.WorkingSet64/1MB), $_.ProcessName }",
	}, "; ")

	out, err := psRun(ctx, script)
	if err != nil {
		return "", fmt.Errorf("Get-Process: %w\n%s", err, out)
	}

	return filterProcessTable(out, filter), nil
}

func killByPID(ctx context.Context, pid int, force bool) (string, error) {
	args := []string{"/PID", strconv.Itoa(pid), "/T"}
	if force {
		args = append(args, "/F")
	}
	out, err := exec.CommandContext(ctx, "taskkill", args...).CombinedOutput()
	if err != nil {
		return fmt.Sprintf("taskkill %d failed: %v\n%s", pid, err, out), nil
	}
	return fmt.Sprintf("Asked PID %d to exit (%s)", pid, killMode(force)), nil
}

func killByName(ctx context.Context, name string, force bool) (string, error) {
	candidates := []string{name}
	// taskkill /IM matches on the image name, which includes the extension. A
	// caller who said "notepad" means notepad.exe, so try that too rather than
	// reporting a process that is plainly running as not found.
	if !strings.Contains(name, ".") {
		candidates = append(candidates, name+".exe")
	}

	var lastOut string
	for _, image := range candidates {
		out, err := taskkillImage(ctx, image, force)
		if err == nil {
			return fmt.Sprintf("Asked all %q processes to exit (%s)", image, killMode(force)), nil
		}
		lastOut = out
	}
	return fmt.Sprintf("taskkill %q failed\n%s", name, lastOut), nil
}

func taskkillImage(ctx context.Context, image string, force bool) (string, error) {
	args := []string{"/IM", image, "/T"}
	if force {
		args = append(args, "/F")
	}
	out, err := exec.CommandContext(ctx, "taskkill", args...).CombinedOutput()
	return strings.TrimRight(string(out), "\r\n"), err
}

// killMode names what actually happened. Windows has no signals: taskkill
// without /F posts WM_CLOSE, which a console process with no message loop
// simply ignores, so "TERM" would overstate it.
func killMode(force bool) string {
	if force {
		return "forced"
	}
	return "close requested"
}
