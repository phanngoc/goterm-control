//go:build unix

package execution

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// detach gives the child its own process group, so one signal reaches it and
// everything it went on to spawn.
func detach(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// KillGroup kills a process and its descendants. The negative pid is the
// group — the whole point of Setpgid above. If the group is gone the process
// may still be there (Setpgid can fail, or the caller never detached), so fall
// back to killing it alone rather than leaving it running.
func KillGroup(p *os.Process) error {
	if p == nil {
		return nil
	}
	if err := syscall.Kill(-p.Pid, syscall.SIGKILL); err == nil || isGone(err) {
		return err
	}
	return p.Kill()
}

func isGone(err error) bool {
	return errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrProcessDone)
}
