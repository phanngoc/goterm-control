package scheduler

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/ngocp/goterm-control/internal/coord"
)

// cronParser is the standard 5-field parser plus descriptors (@daily, @every
// 1h). Only the parser is used; the calendar lives in the database
// (schedules.next_run_at), not in a process — that is what lets two gateways
// share one set of schedules.
//
// Standard-cron trap worth knowing: when BOTH day-of-month and day-of-week are
// restricted ("0 8 1 * 1"), a run happens when EITHER matches, not both.
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// atLayouts are the forms `--at` accepts, tried in order, in the schedule's zone.
var atLayouts = []string{
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02T15:04",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
}

// NextRun computes the first firing at or after `from` for a spec. A zero
// result with a nil error means "never again" — an `at` whose time has passed.
//
// `every` counts from `from`, so an every-10m schedule that fires late does not
// try to catch up on a fixed grid; `cron` and `at` are absolute in tz.
func NextRun(kind, spec, tz string, from time.Time) (time.Time, error) {
	loc, err := LoadZone(tz)
	if err != nil {
		return time.Time{}, err
	}
	spec = strings.TrimSpace(spec)
	switch kind {
	case coord.ScheduleEvery:
		d, err := time.ParseDuration(spec)
		if err != nil {
			return time.Time{}, fmt.Errorf("every: %q is not a duration (try 10m, 1h30m): %w", spec, err)
		}
		if d < time.Minute {
			return time.Time{}, fmt.Errorf("every: %s is under the 1m minimum", d)
		}
		return from.Add(d), nil
	case coord.ScheduleCron:
		sched, err := cronParser.Parse(spec)
		if err != nil {
			return time.Time{}, fmt.Errorf("cron: %q: %w", spec, err)
		}
		return sched.Next(from.In(loc)), nil
	case coord.ScheduleAt:
		at, err := ParseAt(spec, loc)
		if err != nil {
			return time.Time{}, err
		}
		if !at.After(from) {
			return time.Time{}, nil
		}
		return at, nil
	}
	return time.Time{}, fmt.Errorf("schedule kind %q must be at, every or cron", kind)
}

// ParseAt reads a one-shot time. Forms without a zone are read in loc.
func ParseAt(spec string, loc *time.Location) (time.Time, error) {
	spec = strings.TrimSpace(spec)
	for _, layout := range atLayouts {
		if t, err := time.ParseInLocation(layout, spec, loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("at: %q is not a time (use 2026-09-07T09:00 or RFC3339)", spec)
}

// LoadZone resolves an IANA zone name. "" and "local" mean the machine's zone.
func LoadZone(tz string) (*time.Location, error) {
	switch strings.ToLower(strings.TrimSpace(tz)) {
	case "", "local":
		return time.LoadLocation(LocalZone())
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("tz: %q is not an IANA zone (try Asia/Ho_Chi_Minh): %w", tz, err)
	}
	return loc, nil
}

// LocalZone names the machine's zone the way a schedule row must store it: an
// IANA name, never "Local". $TZ wins; otherwise the /etc/localtime symlink
// (macOS points it at /var/db/timezone/zoneinfo/<Area>/<City>); else UTC.
func LocalZone() string {
	if tz := strings.TrimSpace(os.Getenv("TZ")); tz != "" {
		if _, err := time.LoadLocation(tz); err == nil {
			return tz
		}
	}
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if i := strings.Index(target, "zoneinfo/"); i >= 0 {
			name := target[i+len("zoneinfo/"):]
			if _, err := time.LoadLocation(name); err == nil {
				return name
			}
		}
	}
	return "UTC"
}

// Describe renders a spec for lists: "every 10m", "cron 0 8 * * 1-5 (Asia/Ho_Chi_Minh)".
func Describe(s *coord.Schedule) string {
	switch s.Kind {
	case coord.ScheduleEvery:
		return "every " + s.Spec
	case coord.ScheduleAt:
		return "at " + s.Spec + " (" + s.TZ + ")"
	}
	return "cron " + s.Spec + " (" + s.TZ + ")"
}
