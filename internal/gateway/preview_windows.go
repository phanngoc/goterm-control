//go:build windows

package gateway

import (
	"os"
	"os/exec"
)

func setProcessGroup(cmd *exec.Cmd) {}

// Windows has no process groups to signal; the direct child is what can be
// stopped here.
func killGroup(p *os.Process, hard bool) { _ = p.Kill() }
