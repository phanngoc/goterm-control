//go:build windows

package scheduler

// PowerShell counterparts of the snippets in testcmds_unix_test.go. None of the
// POSIX forms work here: PowerShell reads an environment variable as
// $env:NAME rather than $NAME, has no `>&2` redirection and no `true`.
const (
	// Prints "hello <schedule name>", exercising env expansion and stdout.
	cmdEchoScheduleName = `Write-Output "hello $env:BOMCLAW_SCHEDULE"`
	// Writes "boom" to stderr and exits 3. [Console]::Error is used rather
	// than Write-Error, which wraps the message in an ErrorRecord dump.
	cmdFailWithStderr = `[Console]::Error.WriteLine('boom'); exit 3`
	// Outlives a 1s timeout, so cancellation has something to kill.
	cmdSleep5 = "Start-Sleep -Seconds 5"
	// Succeeds, printing nothing.
	cmdSucceed = "exit 0"
	// Prints a marker a test can look for.
	cmdEchoRAN = "Write-Output RAN"
)
