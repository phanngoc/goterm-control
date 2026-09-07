//go:build !windows

package tools

import (
	"context"
	"os/exec"
)

// shellCommand builds the command that runs a shell snippet.
func shellCommand(ctx context.Context, script string) *exec.Cmd {
	return exec.CommandContext(ctx, "bash", "-c", script)
}
