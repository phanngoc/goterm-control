//go:build !darwin && !windows

package tools

import (
	"context"
	"fmt"
	"runtime"
)

// The desktop tools have no implementation outside macOS and Windows. Before
// the platform split these called screencapture, pbpaste, pbcopy and open
// unconditionally, so on Linux they already failed — with "executable file not
// found in $PATH", which reads like a broken install rather than a tool that
// was never wired up here. Saying so plainly is the whole change.
//
// Wiring Linux up (xdg-open, xclip/wl-copy, grim/spectacle) is a separate
// piece of work: unlike macOS and Windows there is no single answer, it
// depends on X11 vs Wayland and on which helpers are installed.

func unsupported(tool string) error {
	return fmt.Errorf("%s is not implemented on %s (macOS and Windows only)", tool, runtime.GOOS)
}

func captureScreen(_ context.Context, _ string) error {
	return unsupported("take_screenshot")
}

func clipboardGet(_ context.Context) (string, error) {
	return "", unsupported("get_clipboard")
}

func clipboardSet(_ context.Context, _ string) error {
	return unsupported("set_clipboard")
}

func runAutomationScript(_ context.Context, _ string) (string, error) {
	return "", unsupported("run_applescript")
}

func openApp(_ context.Context, _ string) error {
	return unsupported("open_app")
}

func openURL(_ context.Context, _ string) error {
	return unsupported("browse_url")
}
