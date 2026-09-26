//go:build windows

package gateway

import "errors"

// The dashboard's terminal needs a PTY; this build has none on Windows yet.
func startShell(dir, channel string, cols, rows int) (shell, error) {
	return nil, errors.New("the project terminal is not available on Windows")
}
