//go:build windows

package main

import (
	"fmt"
	"log"
	"os/exec"
	"strings"
	"syscall"
)

// hidden runs a helper without flashing a console window.
//
// This matters more here than anywhere else in the codebase: bomtray is a
// GUI-only process, so every one of these would otherwise pop a black console
// on the user's desktop for a fraction of a second, several times a session.
func hidden(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd
}

// openPath opens a file, directory, or URL with its default handler.
//
// `cmd /c start` is used rather than explorer.exe, which returns a non-zero
// exit code on success often enough to make error handling meaningless. The
// empty "" is start's window-title argument: without it, start treats a quoted
// target as the title and opens nothing.
func openPath(target string) {
	if err := hidden("cmd", "/c", "start", "", target).Run(); err != nil {
		log.Printf("bomtray: open %s: %v", target, err)
	}
}

// openTerminalTail opens a visible PowerShell window following the log — the
// counterpart of Terminal.app running `tail -f`.
//
// Get-Content -Wait is the tail; -Tail 50 gives the same "what just happened"
// context tail's default does. This window is meant to be seen, so only the
// launching cmd is hidden.
func openTerminalTail(logPath string) {
	ps := fmt.Sprintf("Get-Content -LiteralPath %s -Wait -Tail 50", psQuote(logPath))
	err := hidden("cmd", "/c", "start", "bomclaw logs",
		"powershell", "-NoExit", "-NoProfile", "-Command", ps).Run()
	if err != nil {
		log.Printf("bomtray: tail logs: %v", err)
	}
}

// psQuote wraps a value in a PowerShell single-quoted string, where the only
// escape is a doubled quote and nothing else is interpreted.
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// restartGateway restarts the gateway's Scheduled Task.
//
// There is no schtasks equivalent of `launchctl kickstart -k`, so it is /End
// then /Run. /End on a task that is not running reports an error, which is not
// a reason to skip the start — the gateway clears any stale listener on its
// port at startup.
func restartGateway(taskPath string) {
	_ = hidden("schtasks", "/End", "/TN", taskPath).Run()
	if err := hidden("schtasks", "/Run", "/TN", taskPath).Run(); err != nil {
		log.Printf("bomtray: restart gateway: %v", err)
		notify("Bomclaw", "Restart failed — check the gateway log")
		return
	}
	notify("Bomclaw", "Gateway restarting...")
}

// notify shows a toast in the Windows notification centre.
//
// This goes through WinRT rather than a Shell_NotifyIcon balloon: balloons are
// suppressed on Windows 11 in favour of toasts, and creating a second
// NotifyIcon purely to raise one would briefly add a second tray icon.
//
// The AppID has to name an application with a Start menu entry or the toast is
// dropped without error, which is why it borrows PowerShell's own well-known
// AppID instead of registering one. The consequence is cosmetic: toasts are
// attributed to "Windows PowerShell".
func notify(title, message string) {
	const appID = `{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe`

	script := strings.Join([]string{
		`[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType=WindowsRuntime] > $null`,
		`[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom, ContentType=WindowsRuntime] > $null`,
		`$t = [Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent(` +
			`[Windows.UI.Notifications.ToastTemplateType]::ToastText02)`,
		`$n = $t.GetElementsByTagName('text')`,
		`$n.Item(0).AppendChild($t.CreateTextNode(` + psQuote(title) + `)) > $null`,
		`$n.Item(1).AppendChild($t.CreateTextNode(` + psQuote(message) + `)) > $null`,
		`[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier(` +
			psQuote(appID) + `).Show(` +
			`[Windows.UI.Notifications.ToastNotification]::new($t))`,
	}, "; ")

	if err := hidden("powershell", "-NoProfile", "-NonInteractive", "-Command", script).Run(); err != nil {
		log.Printf("bomtray: notify: %v", err)
	}
}
