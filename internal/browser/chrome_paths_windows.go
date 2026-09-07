//go:build windows

package browser

import (
	"os"
	"path/filepath"
)

// chromeInstallDirs are the roots a Chromium browser installs itself under.
// Chrome and Edge land in Program Files for a machine-wide install and in
// LOCALAPPDATA for a per-user one, and the per-user copy is the one a
// non-admin install produces — so both have to be checked.
var chromeInstallDirs = []string{"PROGRAMFILES", "PROGRAMFILES(X86)", "LOCALAPPDATA"}

// chromeRelPaths are the executable paths under each install root, ordered by
// preference. Edge is included because it ships with Windows 11: on a machine
// with nothing else installed it is the browser that will actually be there.
var chromeRelPaths = []string{
	`Google\Chrome\Application\chrome.exe`,
	`Google\Chrome Beta\Application\chrome.exe`,
	`Chromium\Application\chrome.exe`,
	`BraveSoftware\Brave-Browser\Application\brave.exe`,
	`Microsoft\Edge\Application\msedge.exe`,
}

// chromeCandidates returns absolute paths to try, in preference order.
func chromeCandidates() []string {
	var out []string
	for _, env := range chromeInstallDirs {
		root := os.Getenv(env)
		if root == "" {
			continue
		}
		for _, rel := range chromeRelPaths {
			out = append(out, filepath.Join(root, rel))
		}
	}
	return out
}

// chromeExeNames returns the names to look for on PATH as a fallback.
func chromeExeNames() []string {
	return []string{"chrome.exe", "chromium.exe", "brave.exe", "msedge.exe"}
}
