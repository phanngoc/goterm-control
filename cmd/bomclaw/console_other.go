//go:build !windows

package main

// hideConsoleWindow does nothing outside Windows. launchd and systemd both
// start a service with no terminal attached, so there is no window to hide.
func hideConsoleWindow() {}
