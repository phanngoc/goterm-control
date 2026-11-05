package credentials

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// A capped claude account says when it comes back:
//
//	You've hit your 5-hour limit · resets 7:20pm (Asia/Saigon)
//	You've hit your session limit · resets 3pm
//	You've hit your weekly limit · resets Nov 7, 3pm (Asia/Saigon)
//
// A fixed cooldown ignores that. On 2026-11-05 a token capped until 19:20 was
// benched for 15 minutes, picked again, failed again, and that failure reached
// the person — every quarter of an hour, on all three agents.
var resetRe = regexp.MustCompile(`(?i)resets\s+(?:(?:at|on)\s+)?(?:([a-z]{3})[a-z]*\.?\s+(\d{1,2})(?:st|nd|rd|th)?,?\s+(?:at\s+)?)?(\d{1,2})(?::(\d{2}))?\s*(am|pm)\b(?:\s*\(([^)]+)\))?`)

var months = map[string]time.Month{
	"jan": time.January, "feb": time.February, "mar": time.March, "apr": time.April,
	"may": time.May, "jun": time.June, "jul": time.July, "aug": time.August,
	"sep": time.September, "oct": time.October, "nov": time.November, "dec": time.December,
}

// maxReset bounds what a parsed reset may bench an account for. A weekly cap
// is the longest there is; anything further is a misread, and an account
// benched by a misread for a month is worse than one tried too early.
const maxReset = 8 * 24 * time.Hour

// ResetAt reads the reset time out of a limit message: the first moment
// after now that matches it, in the zone the message names (or now's zone).
func ResetAt(msg string, now time.Time) (time.Time, bool) {
	m := resetRe.FindStringSubmatch(msg)
	if m == nil {
		return time.Time{}, false
	}
	loc := now.Location()
	if tz := strings.TrimSpace(m[6]); tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	hour, _ := strconv.Atoi(m[3])
	minute := 0
	if m[4] != "" {
		minute, _ = strconv.Atoi(m[4])
	}
	if hour < 1 || hour > 12 || minute > 59 {
		return time.Time{}, false
	}
	hour %= 12
	if strings.EqualFold(m[5], "pm") {
		hour += 12
	}
	local := now.In(loc)
	var at time.Time
	if m[1] != "" {
		mon, ok := months[strings.ToLower(m[1])]
		day, _ := strconv.Atoi(m[2])
		if !ok || day < 1 || day > 31 {
			return time.Time{}, false
		}
		at = time.Date(local.Year(), mon, day, hour, minute, 0, 0, loc)
		if at.Before(local) { // "resets Jan 2" read on Dec 30
			at = at.AddDate(1, 0, 0)
		}
	} else {
		at = time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, loc)
		if !at.After(local) {
			at = at.AddDate(0, 0, 1)
		}
	}
	if at.Sub(now) > maxReset {
		return time.Time{}, false
	}
	return at, true
}

// coolFor is how long err benches an account, and why.
func (p *Pool) coolFor(err error, now time.Time) (time.Time, bool) {
	switch {
	case IsRateLimit(err):
		if at, ok := ResetAt(err.Error(), now); ok {
			// A minute past the stated reset: the provider's clock and ours
			// are not the same clock.
			return at.Add(time.Minute), true
		}
		return now.Add(p.cooldown), true
	case IsAuthFailure(err):
		return now.Add(AuthCooldown), true
	}
	return time.Time{}, false
}
