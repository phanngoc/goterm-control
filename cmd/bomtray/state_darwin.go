//go:build darwin

package main

import "fyne.io/systray"

// setTrayPresentation renders the state as a menu bar title. macOS is the only
// platform where systray supports one, and it is the more informative option
// where it exists: the glyphs are visible without hovering.
func setTrayPresentation(s trayState, tooltip string) {
	title := "🤖"
	switch {
	case s.down:
		title = "🤖⛔"
	case s.running:
		title = "🤖⚡"
	}
	if s.awake {
		title += "☕"
	}
	systray.SetTitle(title)
	systray.SetTooltip(tooltip)
}

func awakeMenuLabel() string { return "Keep Mac Awake" }

// serviceLabelFor maps an agent id to its launchd label, which is how
// `bomclaw gateway install` names the LaunchAgent.
func serviceLabelFor(agentID string) string {
	if agentID == "" {
		return ""
	}
	return "com." + agentID + ".gateway"
}
