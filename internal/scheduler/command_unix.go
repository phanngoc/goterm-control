//go:build !windows

package scheduler

import (
	"context"
	"os/exec"
	"syscall"
)

// newShellCommand builds a schedule's command in its own process group, so the
// timeout kills the whole tree rather than leaving the shell's children behind.
//
// Setpgid puts the shell in a new group; killing the negated pid signals every
// process in it.
func newShellCommand(ctx context.Context, script string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	return cmd
}
