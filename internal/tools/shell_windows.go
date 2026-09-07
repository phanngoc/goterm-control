//go:build windows

package tools

import (
	"context"
	"os/exec"
)

// shellCommand builds the command that runs a shell snippet.
//
// PowerShell is what a Windows user means by "run this command", and it is
// what the run_shell tool description advertises on this platform. bash may
// well be installed (Git Bash, WSL), but assuming it would make the agent's
// commands depend on which of them the machine happens to have.
//
// -NoProfile keeps a user's profile from changing what a command does, and
// -NonInteractive turns a credential prompt into an error rather than a turn
// that hangs until the tool timeout. Stdout is forced to UTF-8 because
// PowerShell otherwise emits the console's OEM code page, which reaches Go as
// mojibake for anything outside ASCII.
func shellCommand(ctx context.Context, script string) *exec.Cmd {
	return exec.CommandContext(ctx, psExe(),
		"-NoProfile", "-NonInteractive", "-Command",
		"[Console]::OutputEncoding=[System.Text.Encoding]::UTF8;"+script,
	)
}
