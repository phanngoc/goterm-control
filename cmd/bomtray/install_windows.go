//go:build windows

package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// The tray starts at logon through the per-user Run key.
//
// Not a Scheduled Task, unlike the gateway: a tray icon has no business being
// restarted when the user closes it, and Run is the mechanism Windows users
// already know to inspect (Task Manager's Startup tab lists it, and can disable
// it, which a task cannot). It is also HKCU only, so no elevation.
const (
	trayRunKey  = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`
	trayRunName = "BomClawTray"
)

// runInstall registers the tray to start at logon, and starts it now.
func runInstall() {
	binPath, err := os.Executable()
	if err != nil {
		log.Fatalf("bomtray: resolve executable: %v", err)
	}
	if resolved, err := filepath.EvalSymlinks(binPath); err == nil {
		binPath = resolved
	}

	// Spell the agent list out rather than relying on the binary's defaults:
	// the defaults can change, and a Run entry that says what it watches is one
	// you can read when the menu shows the wrong thing.
	parts := []string{quoteArg(binPath)}
	for _, u := range defaultAgents {
		parts = append(parts, "-agent", quoteArg(u))
	}
	value := strings.Join(parts, " ")

	// /f overwrites an existing value, which is what a reinstall wants.
	if err := hidden("reg", "add", trayRunKey, "/v", trayRunName,
		"/t", "REG_SZ", "/d", value, "/f").Run(); err != nil {
		log.Fatalf("bomtray: register Run entry: %v", err)
	}

	fmt.Printf("bomtray installed: %s\\%s\n", trayRunKey, trayRunName)
	fmt.Println("It will start at every logon. Starting it now...")

	// Detached, so the tray outlives this console.
	cmd := hidden(binPath)
	args := []string{binPath}
	for _, u := range defaultAgents {
		args = append(args, "-agent", u)
	}
	cmd.Args = args
	if err := cmd.Start(); err != nil {
		log.Printf("bomtray: could not start now (it will start at logon): %v", err)
		return
	}
	_ = cmd.Process.Release()
	fmt.Println("(the icon should appear in the notification area — check the ^ overflow if you do not see it)")
}

// runUninstall removes the Run entry. A tray already running is left alone;
// quitting it is what the menu's Quit item is for.
func runUninstall() {
	if err := hidden("reg", "delete", trayRunKey, "/v", trayRunName, "/f").Run(); err != nil {
		// reg delete fails when the value is not there, which is the same
		// end state the caller asked for.
		log.Printf("bomtray: remove Run entry (may not have been installed): %v", err)
	}
	fmt.Println("bomtray uninstalled (a running tray keeps running until you Quit it)")
}

// quoteArg wraps a value in double quotes when it needs them, so a path under
// "C:\Program Files\..." survives being stored as one Run string and split
// again by the shell at logon.
func quoteArg(s string) string {
	if !strings.ContainsAny(s, " \t\"") {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}
