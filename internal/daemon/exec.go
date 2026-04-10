package daemon

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"syscall"
)

// ExecResult holds the output of a subprocess execution.
type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// execCommand runs a command and returns its output.
func execCommand(ctx context.Context, name string, args ...string) (*ExecResult, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()

	result := &ExecResult{
		Stdout: outBuf.String(),
		Stderr: errBuf.String(),
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok {
				result.ExitCode = ws.ExitStatus()
			} else {
				result.ExitCode = 1
			}
			return result, nil // non-zero exit is not a Go error
		}
		return result, fmt.Errorf("exec %s: %w", name, err)
	}

	return result, nil
}

// commandExists checks if a command is available in PATH.
func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
