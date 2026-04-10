package daemon

import (
	"context"
	"fmt"
	"runtime"
)

// InstallArgs holds everything needed to write and activate a service.
type InstallArgs struct {
	BinaryPath  string            // absolute path to the nanoclaw binary
	Port        int               // gateway port
	Bind        string            // bind address
	ConfigPath  string            // absolute path to config.yaml
	EnvFile     string            // absolute path to .env (used as EnvironmentFile=)
	Environment map[string]string // extra env vars (HOME, API keys)
	Description string            // service description
	Force       bool              // force reinstall
}

// ServiceRuntime holds the live state of a managed service.
type ServiceRuntime struct {
	Status     string // "running", "stopped", "failed", "unknown"
	SubState   string // platform-specific detail (e.g. "running", "dead")
	PID        int    // main process PID (0 if not running)
	ExitCode   int    // last exit code
	ExitReason string // last exit reason
}

// Service is the platform-agnostic interface for daemon lifecycle management.
type Service interface {
	// Label returns the human-readable backend name (e.g. "systemd", "LaunchAgent").
	Label() string

	// Install writes the service configuration and activates it.
	Install(ctx context.Context, args InstallArgs) error

	// Uninstall stops and removes the service.
	Uninstall(ctx context.Context) error

	// Start starts the service.
	Start(ctx context.Context) error

	// Stop stops the service.
	Stop(ctx context.Context) error

	// Restart restarts the service.
	Restart(ctx context.Context) error

	// IsInstalled returns true if the service is registered and enabled.
	IsInstalled() (bool, error)

	// ReadRuntime reads the live runtime state.
	ReadRuntime() (*ServiceRuntime, error)

	// UnitPath returns the path to the service definition file.
	UnitPath() string
}

// Resolve returns the appropriate Service for the current platform.
func Resolve() (Service, error) {
	switch runtime.GOOS {
	case "linux":
		return newSystemdService()
	case "darwin":
		return newLaunchdService()
	default:
		return nil, fmt.Errorf("daemon service not supported on %s", runtime.GOOS)
	}
}
