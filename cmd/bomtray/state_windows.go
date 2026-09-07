//go:build windows

package main

import "fyne.io/systray"

// tooltipMax is the length Shell_NotifyIcon accepts in NOTIFYICONDATA.szTip,
// counted in UTF-16 units and including the terminator. A longer string is not
// truncated for us — the call fails and the tooltip silently keeps its old
// value, which is worse than a trimmed one.
const tooltipMax = 127

// setTrayPresentation renders the state as an icon plus a tooltip.
//
// There is no title to set: systray's SetTitle is an explicit no-op on Windows
// ("do nothing" in systray_windows.go), because the notification area has no
// text slot. So the colour of the icon carries the state and the tooltip
// carries the detail.
func setTrayPresentation(s trayState, tooltip string) {
	systray.SetIcon(stateIcon(s))
	systray.SetTooltip(trimToUTF16(tooltip, tooltipMax))
}

// trimToUTF16 shortens s to at most n UTF-16 units without splitting a
// surrogate pair — agent names and task text may hold astral-plane runes, and
// half a pair is an invalid string, not a shorter one.
func trimToUTF16(s string, n int) string {
	if len(s) <= n { // ASCII fast path: bytes >= UTF-16 units
		return s
	}
	units := 0
	for i, r := range s {
		w := 1
		if r > 0xFFFF {
			w = 2
		}
		if units+w > n {
			return s[:i]
		}
		units += w
	}
	return s
}

func awakeMenuLabel() string { return "Keep PC Awake" }

// serviceLabelFor maps an agent id to the Scheduled Task the gateway installs
// itself as.
//
// The agent id is deliberately ignored: unlike launchd, where the label is
// com.<id>.gateway and each agent gets its own, the Windows backend registers a
// single fixed task path (see internal/daemon/schtasks.go). A machine running
// two gateways therefore has only one installed service, and restarting either
// menu entry restarts that one. Naming tasks per agent is the fix, and it
// belongs in the daemon package rather than here.
func serviceLabelFor(string) string { return `\BomClaw\bomclaw-gateway` }
