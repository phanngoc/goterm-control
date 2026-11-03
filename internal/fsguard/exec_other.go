//go:build !darwin

package fsguard

import (
	"context"
	"os/exec"
)

// Only macOS has TCC, and only macOS has sandbox-exec.
func supported() bool { return false }

// CommandContext is exec.CommandContext: there is nothing to guard here.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}
