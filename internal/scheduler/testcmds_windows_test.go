//go:build windows

package scheduler

import "time"

// PowerShell counterparts of the snippets in testcmds_unix_test.go. None of the
// POSIX forms work here: PowerShell reads an environment variable as
// $env:NAME rather than $NAME, has no `>&2` redirection and no `true`.
const (
	// Prints "hello <schedule name>", exercising env expansion and stdout.
	cmdEchoScheduleName = `Write-Output "hello $env:BOMCLAW_SCHEDULE"`
	// Writes "boom" to stderr and exits 3. [Console]::Error is used rather
	// than Write-Error, which wraps the message in an ErrorRecord dump.
	cmdFailWithStderr = `[Console]::Error.WriteLine('boom'); exit 3`
	// Outlives a 1s timeout, so cancellation has something to kill. Longer than
	// the Unix five seconds because the budget below has to be wider, and the
	// sleep must still comfortably outlast it.
	cmdSleep5 = "Start-Sleep -Seconds 20"
	// Wider than Unix's 3s on purpose, and measured rather than guessed: a
	// 1s-timeout run costs a PowerShell start (~0.5-1s) plus a taskkill spawn
	// on the cancel path, since Windows cannot signal a process group. That
	// lands at 3.1-3.5s here, so the original 3s budget failed for the one
	// reason the assertion does not care about — process startup, not a
	// deadline that went unenforced. 8s still proves enforcement against a
	// 20 second sleep.
	cancelBudget = 8 * time.Second
	// Succeeds, printing nothing.
	cmdSucceed = "exit 0"
	// Prints a marker a test can look for.
	cmdEchoRAN = "Write-Output RAN"
)
