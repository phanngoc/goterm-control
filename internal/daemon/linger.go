package daemon

import (
	"context"
	"fmt"
	"log"
	"os/user"
	"runtime"
	"strings"
)

// IsLingerEnabled checks if loginctl linger is enabled for the given user.
// Returns true if the user's services persist across logout.
func IsLingerEnabled(username string) (bool, error) {
	if runtime.GOOS != "linux" {
		return true, nil // linger is a Linux-only concept
	}

	if !commandExists("loginctl") {
		return false, fmt.Errorf("loginctl not found")
	}

	res, err := execCommand(context.Background(),
		"loginctl", "show-user", username, "-p", "Linger",
	)
	if err != nil {
		return false, err
	}

	// Output: "Linger=yes" or "Linger=no"
	stdout := strings.TrimSpace(res.Stdout)
	_, val, _ := strings.Cut(stdout, "=")
	return strings.TrimSpace(val) == "yes", nil
}

// EnableLinger enables loginctl linger for the given user.
// Tries without sudo first, then falls back to sudo.
func EnableLinger(username string) error {
	// Try without sudo first
	res, err := execCommand(context.Background(),
		"loginctl", "enable-linger", username,
	)
	if err == nil && res.ExitCode == 0 {
		return nil
	}

	// Fallback: try with sudo -n (non-interactive)
	res, err = execCommand(context.Background(),
		"sudo", "-n", "loginctl", "enable-linger", username,
	)
	if err == nil && res.ExitCode == 0 {
		return nil
	}

	return fmt.Errorf("could not enable linger — run manually:\n  sudo loginctl enable-linger %s", username)
}

// PromptLinger checks linger status and prints instructions if not enabled.
// This is called after service installation on Linux.
func PromptLinger() {
	if runtime.GOOS != "linux" {
		return
	}

	u, err := user.Current()
	if err != nil {
		return
	}

	enabled, err := IsLingerEnabled(u.Username)
	if err != nil {
		log.Printf("daemon: could not check linger status: %v", err)
		return
	}

	if enabled {
		log.Printf("daemon: linger is enabled for %s — service will persist across logout", u.Username)
		return
	}

	// Try to enable automatically
	if err := EnableLinger(u.Username); err != nil {
		fmt.Println()
		fmt.Println("⚠️  Linger is NOT enabled — the service will stop when you log out.")
		fmt.Println("   To keep it running permanently, run:")
		fmt.Printf("   sudo loginctl enable-linger %s\n", u.Username)
		fmt.Println()
	} else {
		log.Printf("daemon: linger enabled for %s", u.Username)
	}
}
