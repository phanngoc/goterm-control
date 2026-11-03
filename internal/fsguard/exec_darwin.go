package fsguard

import (
	"context"
	"os"
	"os/exec"
)

// sandboxExec ships with every macOS release; Apple marks the CLI deprecated
// but the sandbox it drives is the one the OS itself runs on.
const sandboxExec = "/usr/bin/sandbox-exec"

func supported() bool {
	_, err := os.Stat(sandboxExec)
	return err == nil
}

// CommandContext is exec.CommandContext, started inside the guard when it is
// on. sandbox-exec applies the profile and then execs the CLI in place, so the
// pid, the process group and every signal sent to it are the CLI's own.
//
// The binary is resolved first so the sandboxed exec finds the same file Go
// would have run; if it cannot be resolved the call falls through unwrapped,
// and fails exactly as it always did.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	s := current.Load()
	if s == nil {
		return exec.CommandContext(ctx, name, args...)
	}
	bin, err := exec.LookPath(name)
	if err != nil {
		return exec.CommandContext(ctx, name, args...)
	}
	wrapped := append([]string{"-p", s.profile, bin}, args...)
	return exec.CommandContext(ctx, sandboxExec, wrapped...)
}
