//go:build !windows

package browser

// chromeCandidates returns absolute paths to try, in preference order.
func chromeCandidates() []string {
	return []string{
		"/usr/bin/google-chrome",
		"/usr/bin/google-chrome-stable",
		"/usr/bin/brave-browser",
		"/usr/bin/brave-browser-stable",
		"/usr/bin/microsoft-edge",
		"/usr/bin/microsoft-edge-stable",
		"/usr/bin/chromium",
		"/usr/bin/chromium-browser",
		"/snap/bin/chromium",
	}
}

// chromeExeNames returns the names to look for on PATH as a fallback.
func chromeExeNames() []string {
	return []string{"google-chrome", "chromium", "chromium-browser"}
}
