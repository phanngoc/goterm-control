//go:build darwin

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
)

const trayAgentLabel = "com.bomclaw.tray"

const trayPlistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>LimitLoadToSessionType</key>
	<string>Aqua</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`

func trayPlistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library/LaunchAgents", trayAgentLabel+".plist")
}

// runInstall writes the LaunchAgent plist and loads it so the tray starts
// now and at every login.
func runInstall() {
	binPath, err := os.Executable()
	if err != nil {
		log.Fatalf("bomtray: resolve executable: %v", err)
	}
	binPath, _ = filepath.EvalSymlinks(binPath)

	home, _ := os.UserHomeDir()
	logPath := filepath.Join(home, ".goterm/logs/tray.err.log")
	_ = os.MkdirAll(filepath.Dir(logPath), 0755)

	plist := fmt.Sprintf(trayPlistTemplate, trayAgentLabel, binPath, logPath)
	path := trayPlistPath()
	if err := os.WriteFile(path, []byte(plist), 0644); err != nil {
		log.Fatalf("bomtray: write plist: %v", err)
	}

	// Reload: unload first in case an older version is loaded.
	_ = exec.Command("launchctl", "unload", path).Run()
	if err := exec.Command("launchctl", "load", path).Run(); err != nil {
		log.Fatalf("bomtray: launchctl load: %v", err)
	}
	fmt.Printf("bomtray installed: %s\n(the 🤖 icon should appear in your menu bar)\n", path)
}

// runUninstall unloads and removes the LaunchAgent.
func runUninstall() {
	path := trayPlistPath()
	_ = exec.Command("launchctl", "unload", path).Run()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Fatalf("bomtray: remove plist: %v", err)
	}
	fmt.Println("bomtray uninstalled")
}
