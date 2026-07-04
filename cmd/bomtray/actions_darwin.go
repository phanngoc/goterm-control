//go:build darwin

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
)

// openPath opens a file, directory, or URL with the default handler.
func openPath(target string) {
	if err := exec.Command("open", target).Run(); err != nil {
		log.Printf("bomtray: open %s: %v", target, err)
	}
}

// openTerminalTail opens Terminal.app tailing the given log file.
func openTerminalTail(logPath string) {
	script := fmt.Sprintf(`tell application "Terminal"
	activate
	do script "tail -f %s"
end tell`, logPath)
	if err := exec.Command("osascript", "-e", script).Run(); err != nil {
		log.Printf("bomtray: tail logs: %v", err)
	}
}

// restartGateway restarts the launchd service (KeepAlive-safe).
func restartGateway(label string) {
	target := fmt.Sprintf("gui/%d/%s", os.Getuid(), label)
	if err := exec.Command("launchctl", "kickstart", "-k", target).Run(); err != nil {
		log.Printf("bomtray: restart gateway: %v", err)
		notify("Bomclaw", "Restart failed — check logs")
		return
	}
	notify("Bomclaw", "Gateway restarting...")
}

// notify shows a macOS notification via osascript.
func notify(title, message string) {
	esc := func(s string) string { return strings.ReplaceAll(s, `"`, `\"`) }
	script := fmt.Sprintf(`display notification "%s" with title "%s"`, esc(message), esc(title))
	if err := exec.Command("osascript", "-e", script).Run(); err != nil {
		log.Printf("bomtray: notify: %v", err)
	}
}
