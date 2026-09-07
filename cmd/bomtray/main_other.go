//go:build !darwin && !windows

// bomtray currently supports macOS and Windows. This file is the stub every
// other platform gets, and the checklist for adding one — Ubuntu 22 is the
// intended next target.
//
// The shared logic in main.go is already platform-neutral: polling, the menu,
// state transitions, notifications on up/down/finish, awake modes and their
// persistence are all common. A new platform supplies only the following, in
// files tagged for it. Nothing else has to change.
//
// Presentation (see state_darwin.go, state_windows.go):
//
//	setTrayPresentation(s trayState, tooltip string)
//	awakeMenuLabel() string
//	serviceLabelFor(agentID string) string
//
// Sleep inhibition (see awake_darwin.go, awake_windows.go) — a type named
// awake with:
//
//	Hold() error   // idempotent
//	Release()      // idempotent
//	Held() bool
//
// Desktop actions (see actions_darwin.go, actions_windows.go):
//
//	openPath(target string)          // file, directory, or URL
//	openTerminalTail(logPath string) // a visible window following the log
//	restartGateway(target string)    // whatever serviceLabelFor named
//	notify(title, message string)
//
// Autostart (see install_darwin.go, install_windows.go):
//
//	runInstall()
//	runUninstall()
//
// For Ubuntu 22 specifically, the tray itself is close to free: fyne.io/systray
// already speaks the freedesktop StatusNotifierItem protocol, which GNOME needs
// an AppIndicator extension for and KDE supports natively — so the icon may or
// may not appear depending on the desktop, which is worth detecting rather than
// assuming. The real work is the other three groups: sleep inhibition through
// a logind Inhibit or a portal call rather than a one-line syscall, xdg-open
// for actions, and a systemd --user unit for autostart. That is why this is a
// stub instead of a half-working port.
package main

import (
	"fmt"
	"os"
	"runtime"
)

func main() {
	fmt.Fprintf(os.Stderr, "bomtray is not supported on %s yet (macOS and Windows only)\n", runtime.GOOS)
	os.Exit(1)
}
