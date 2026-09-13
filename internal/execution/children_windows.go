//go:build windows

package execution

import (
	"errors"
	"os"
	"os/exec"
)

// Windows has no fork/exec process group to put a child in; a job object would
// be the equivalent and nothing on this platform has needed it yet.
func detach(*exec.Cmd) {}

// KillGroup kills the process alone. Descendants survive, which is the
// pre-existing behaviour on this platform.
func KillGroup(p *os.Process) error {
	if p == nil {
		return nil
	}
	return p.Kill()
}

func isGone(err error) bool { return errors.Is(err, os.ErrProcessDone) }
