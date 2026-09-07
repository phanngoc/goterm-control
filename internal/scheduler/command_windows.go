//go:build windows

package scheduler

import (
	"context"
	"os/exec"
	"strconv"
	"syscall"
)

// newShellCommand builds a schedule's command so that a timeout kills the whole
// tree rather than leaving the shell's children behind.
//
// PowerShell, not /bin/sh, for the same reason run_shell uses it: it is the
// shell a Windows user means, and assuming bash would make a schedule depend on
// whether Git Bash happens to be installed. Stdout is forced to UTF-8 because
// PowerShell otherwise writes the console's OEM code page, and a schedule's
// output is stored and shown back.
//
// CREATE_NEW_PROCESS_GROUP is the counterpart of Setpgid, and taskkill /T is
// the counterpart of signalling a negated pid: Windows has no way to signal a
// process group directly, so the tree is walked by pid instead.
func newShellCommand(ctx context.Context, script string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, psExe(),
		"-NoProfile", "-NonInteractive", "-Command",
		"[Console]::OutputEncoding=[System.Text.Encoding]::UTF8;"+script,
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
		HideWindow:    true,
	}
	cmd.Cancel = func() error {
		// /F because a console process with no message loop ignores the
		// polite close, and a timed-out schedule is past being asked nicely.
		return exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F").Run()
	}
	return cmd
}

// psExe prefers PowerShell 7 when installed, falling back to the powershell.exe
// that ships with Windows.
func psExe() string {
	if p, err := exec.LookPath("pwsh.exe"); err == nil {
		return p
	}
	return "powershell.exe"
}
