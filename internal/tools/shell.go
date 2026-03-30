package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

type ShellTool struct {
	DefaultTimeout int
	MaxOutputBytes int
}

type shellInput struct {
	Command    string `json:"command"`
	WorkingDir string `json:"working_dir"`
	Timeout    int    `json:"timeout"`
}

func (t *ShellTool) Run(ctx context.Context, raw json.RawMessage) (string, error) {
	var inp shellInput
	if err := json.Unmarshal(raw, &inp); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	timeout := t.DefaultTimeout
	if inp.Timeout > 0 {
		if inp.Timeout > 300 {
			inp.Timeout = 300
		}
		timeout = inp.Timeout
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", inp.Command)

	// Working directory
	workDir := inp.WorkingDir
	if workDir == "" {
		workDir = os.Getenv("HOME")
	}
	// Expand ~ manually
	if strings.HasPrefix(workDir, "~/") {
		workDir = os.Getenv("HOME") + workDir[1:]
	}
	cmd.Dir = workDir

	// Inherit environment
	cmd.Env = os.Environ()

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	startTime := time.Now()
	err := cmd.Run()
	elapsed := time.Since(startTime)

	output := out.String()
	if len(output) > t.MaxOutputBytes {
		output = output[:t.MaxOutputBytes] + fmt.Sprintf("\n\n[... output truncated at %d bytes ...]", t.MaxOutputBytes)
	}

	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Sprintf("Command timed out after %ds\n\nOutput so far:\n%s", timeout, output), nil
	}

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}

	result := fmt.Sprintf("$ %s\n(exit: %d, %.2fs)\n\n%s", inp.Command, exitCode, elapsed.Seconds(), output)
	return strings.TrimRight(result, "\n"), nil
}
