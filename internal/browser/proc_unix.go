//go:build !windows

package browser

import (
	"os"
	"os/exec"
	"syscall"
)

// detach puts Chrome in its own process group, so it survives if the gateway
// dies mid-launch and a signal sent to our group does not take it with us.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// requestStop asks Chrome to shut down cleanly.
func requestStop(p *os.Process) error {
	return p.Signal(syscall.SIGTERM)
}
