//go:build !windows

package gateway

import (
	"os"
	"os/exec"
	"syscall"
)

// setProcessGroup puts a preview in its own process group, so stopping it
// reaches what it started — npm spawns node, node spawns esbuild.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killGroup(p *os.Process, hard bool) {
	sig := syscall.SIGTERM
	if hard {
		sig = syscall.SIGKILL
	}
	_ = syscall.Kill(-p.Pid, sig)
}
