//go:build windows

package browser

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// detach puts Chrome in its own console process group, so a Ctrl-C or
// CTRL_CLOSE_EVENT delivered to the gateway's console is not broadcast to
// Chrome as well. This is the Windows counterpart of Setpgid.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}

// requestStop asks Chrome to shut down cleanly.
//
// Windows has no SIGTERM — os.Process.Signal only accepts Kill here. taskkill
// without /F posts WM_CLOSE to the target's windows, which is what Chrome
// treats as a user-initiated quit (sessions and profiles get flushed). The
// caller polls CDP afterwards and escalates to Process.Kill on timeout.
func requestStop(p *os.Process) error {
	ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	return exec.CommandContext(ctx, "taskkill", "/PID", strconv.Itoa(p.Pid), "/T").Run()
}
