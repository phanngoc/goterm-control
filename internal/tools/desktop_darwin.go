//go:build darwin

package tools

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// captureScreen writes a PNG of the screen to path. -x silences the shutter.
func captureScreen(ctx context.Context, path string) error {
	out, err := exec.CommandContext(ctx, "screencapture", "-x", path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("screencapture failed: %w\n%s", err, out)
	}
	return nil
}

func clipboardGet(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "pbpaste").Output()
	if err != nil {
		return "", fmt.Errorf("pbpaste: %w", err)
	}
	return strings.TrimRight(string(out), "\n"), nil
}

func clipboardSet(ctx context.Context, text string) error {
	cmd := exec.CommandContext(ctx, "pbcopy")
	cmd.Stdin = strings.NewReader(text)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pbcopy: %w", err)
	}
	return nil
}

func runAutomationScript(ctx context.Context, script string) (string, error) {
	out, err := exec.CommandContext(ctx, "osascript", "-e", script).CombinedOutput()
	result := strings.TrimRight(string(out), "\n")
	if err != nil {
		return fmt.Sprintf("AppleScript error: %v\n%s", err, result), nil
	}
	if result == "" {
		return "AppleScript executed successfully (no output)", nil
	}
	return result, nil
}

// openApp launches an application by name, or opens a file with its default
// application when given a path.
func openApp(ctx context.Context, name string) error {
	var cmd *exec.Cmd
	if strings.HasPrefix(name, "/") || strings.HasPrefix(name, "~/") {
		cmd = exec.CommandContext(ctx, "open", resolvePath(name))
	} else {
		cmd = exec.CommandContext(ctx, "open", "-a", name)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	return nil
}

func openURL(ctx context.Context, url string) error {
	return exec.CommandContext(ctx, "open", url).Run()
}
