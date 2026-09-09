//go:build !windows

package scheduler

import "time"

// Shell snippets the command-payload tests need, written for this platform's
// shell. A schedule's cmd runs in the host's native shell (/bin/sh here,
// PowerShell on Windows), so the tests cannot share one literal — see
// testcmds_windows_test.go for the counterparts, and command_unix.go /
// command_windows.go for what actually runs them.
const (
	// Prints "hello <schedule name>", exercising env expansion and stdout.
	cmdEchoScheduleName = "echo hello $BOMCLAW_SCHEDULE"
	// Writes "boom" to stderr and exits 3.
	cmdFailWithStderr = "echo boom >&2; exit 3"
	// Outlives a 1s timeout, so cancellation has something to kill.
	cmdSleep5 = "sleep 5"
	// How long a 1s-timeout run may take end to end and still prove the
	// deadline fired rather than the command finishing. /bin/sh starts and
	// dies in milliseconds, so the margin here is generous already.
	cancelBudget = 3 * time.Second
	// Succeeds, printing nothing.
	cmdSucceed = "true"
	// Prints a marker a test can look for.
	cmdEchoRAN = "echo RAN"
)
