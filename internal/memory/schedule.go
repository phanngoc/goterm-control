package memory

import (
	"strconv"
	"strings"
	"time"
)

// ShouldRotate reports whether a session last active at lastActivity should
// be rotated (memory flush + reset) at now, and why ("daily" or "idle").
//
// Daily: the session was last active before the most recent dailyAt boundary
// (e.g. "04:00" local). Idle: more than idle has passed since last activity
// (0 disables). Rotation is checked lazily on the next inbound message — the
// caller only invokes this for sessions with a live CLI session ID.
func ShouldRotate(lastActivity, now time.Time, dailyAt string, idle time.Duration) (bool, string) {
	if lastActivity.IsZero() {
		return false, ""
	}
	if idle > 0 && now.Sub(lastActivity) > idle {
		return true, "idle"
	}
	if h, m, ok := parseDailyAt(dailyAt); ok {
		boundary := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
		if boundary.After(now) {
			boundary = boundary.AddDate(0, 0, -1)
		}
		if lastActivity.Before(boundary) {
			return true, "daily"
		}
	}
	return false, ""
}

// parseDailyAt parses "HH:MM". Empty or "off" disables the daily boundary.
func parseDailyAt(s string) (hour, min int, ok bool) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" || s == "off" {
		return 0, 0, false
	}
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}
