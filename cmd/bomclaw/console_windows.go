//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

var (
	modKernel32 = syscall.NewLazyDLL("kernel32.dll")
	modUser32   = syscall.NewLazyDLL("user32.dll")

	procGetConsoleWindow      = modKernel32.NewProc("GetConsoleWindow")
	procGetConsoleProcessList = modKernel32.NewProc("GetConsoleProcessList")
	procShowWindow            = modUser32.NewProc("ShowWindow")
)

const swHide = 0

// hideConsoleWindow hides the console window Windows allocated for this
// process — but only when this process is the sole owner of that console.
//
// Why it is needed: bomclaw is a console binary, so a Scheduled Task running it
// with an InteractiveToken gets a console window on the user's desktop, and it
// sits there for as long as the gateway runs. Nothing in a task definition can
// pass CREATE_NO_WINDOW, so the window has to be dismissed from the inside.
//
// The alternative, and what openclaw does, is to point the task at a .vbs so
// wscript.exe (a GUI-subsystem host) launches the real process hidden. That
// works, but its `Run cmd, 0, False` returns immediately: the task completes,
// the gateway is orphaned, and the task can no longer report or stop it. Since
// that is precisely what the cmd.exe wrapper got wrong here, the window is
// hidden in-process instead and the task keeps owning the gateway.
//
// The process-count check is what makes this safe. Run from a terminal the
// gateway *inherits* that terminal's console, and hiding it would hide the
// user's own window. GetConsoleProcessList reports every process attached to
// the console, so a count of one means the console exists solely for us.
func hideConsoleWindow() {
	hwnd, _, _ := procGetConsoleWindow.Call()
	if hwnd == 0 {
		return // no console: built -H windowsgui, or detached
	}

	// Room for more than one pid on purpose: the call reports the true count
	// even when the buffer is too small, but asking for two lets "exactly one"
	// be distinguished from "at least two" without a second call.
	var pids [2]uint32
	n, _, _ := procGetConsoleProcessList.Call(
		uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)))
	if n != 1 {
		return // sharing a console with a shell — not ours to hide
	}

	procShowWindow.Call(hwnd, swHide)
}
