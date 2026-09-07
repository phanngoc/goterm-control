//go:build windows

package tools

import (
	"context"
	"encoding/base64"
	"fmt"
	"os/exec"
	"strings"
)

// psExe returns the PowerShell to invoke: PowerShell 7 when it is installed,
// otherwise the powershell.exe that ships with Windows 11.
func psExe() string {
	if p, err := exec.LookPath("pwsh.exe"); err == nil {
		return p
	}
	return "powershell.exe"
}

// psRun runs a PowerShell snippet and returns its combined output. See
// shellCommand in shell_windows.go for why the flags and encoding line.
func psRun(ctx context.Context, script string) (string, error) {
	cmd := exec.CommandContext(ctx, psExe(),
		"-NoProfile", "-NonInteractive", "-Command",
		"[Console]::OutputEncoding=[System.Text.Encoding]::UTF8;"+script,
	)
	out, err := cmd.CombinedOutput()
	return strings.TrimRight(string(out), "\r\n"), err
}

// psLiteral renders a Go string as a PowerShell expression that evaluates back
// to it. The value travels as base64 so that no quote, newline, backtick or
// $-expansion in the caller's text can change how the script parses — the text
// here comes from the model, and clipboard contents are arbitrary.
//
// The wrapping parentheses are required, not cosmetic. PowerShell parses a
// cmdlet's arguments in command mode, where a bare `[Type]::Method(...)` is not
// an expression it will bind to a parameter — `Set-Clipboard -Value [Encoding]…`
// fails with "A positional parameter cannot be found that accepts argument
// 'System.Byte[]'". Parenthesising forces expression mode, and is harmless in
// the places this is already interpolated into an argument list.
func psLiteral(s string) string {
	b64 := base64.StdEncoding.EncodeToString([]byte(s))
	return fmt.Sprintf(
		"([System.Text.Encoding]::UTF8.GetString([System.Convert]::FromBase64String('%s')))", b64)
}

// captureScreen writes a PNG of the whole desktop to path.
//
// VirtualScreen, not PrimaryScreen: this is the counterpart of macOS
// `screencapture -x`, which captures every attached display.
//
// This needs a real interactive session to draw from. The gateway's Scheduled
// Task runs with an InteractiveToken for exactly this reason — a Windows
// service in session 0 would capture a black frame.
func captureScreen(ctx context.Context, path string) error {
	script := strings.Join([]string{
		"Add-Type -AssemblyName System.Windows.Forms,System.Drawing",
		"$r = [System.Windows.Forms.SystemInformation]::VirtualScreen",
		"$bmp = New-Object System.Drawing.Bitmap $r.Width, $r.Height",
		"$g = [System.Drawing.Graphics]::FromImage($bmp)",
		"$g.CopyFromScreen($r.Location, [System.Drawing.Point]::Empty, $r.Size)",
		"$bmp.Save(" + psLiteral(path) + ", [System.Drawing.Imaging.ImageFormat]::Png)",
		"$g.Dispose()",
		"$bmp.Dispose()",
	}, "; ")

	if out, err := psRun(ctx, script); err != nil {
		return fmt.Errorf("screen capture failed: %w\n%s", err, out)
	}
	return nil
}

func clipboardGet(ctx context.Context) (string, error) {
	out, err := psRun(ctx, "Get-Clipboard -Raw")
	if err != nil {
		return "", fmt.Errorf("Get-Clipboard: %w\n%s", err, out)
	}
	return out, nil
}

func clipboardSet(ctx context.Context, text string) error {
	if out, err := psRun(ctx, "Set-Clipboard -Value "+psLiteral(text)); err != nil {
		return fmt.Errorf("Set-Clipboard: %w\n%s", err, out)
	}
	return nil
}

// runAutomationScript has no Windows counterpart. AppleScript's job here is
// driving other applications; on Windows that is PowerShell, which the agent
// already reaches through run_shell.
func runAutomationScript(_ context.Context, _ string) (string, error) {
	return "", fmt.Errorf(
		"run_applescript is macOS-only — on Windows use run_shell, which runs PowerShell")
}

// openApp launches an application by name, or opens a file with its default
// application when given a path. Start-Process covers both: a bare name
// resolves through PATH and the App Paths registry key, a document goes
// through its shell association.
func openApp(ctx context.Context, name string) error {
	target := name
	if strings.HasPrefix(target, "~/") || strings.HasPrefix(target, `~\`) {
		target = resolvePath(target)
	}
	if out, err := psRun(ctx, "Start-Process -FilePath "+psLiteral(target)); err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	return nil
}

func openURL(ctx context.Context, url string) error {
	if out, err := psRun(ctx, "Start-Process -FilePath "+psLiteral(url)); err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	return nil
}
