package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ngocp/goterm-control/internal/coord"
	"github.com/ngocp/goterm-control/internal/scheduler"
)

// `bomclaw schedule` writes straight to the shared database, like `task`. The
// gateway's scheduler loop picks changes up on its next tick (30s by default),
// so "add" and "run-now" say when to expect the firing rather than firing
// themselves — a CLI process has no business running a model.

func runSchedule(args []string) {
	if len(args) == 0 {
		scheduleUsage()
		os.Exit(1)
	}
	sub, rest := args[0], args[1:]

	switch sub {
	case "add":
		fs := flag.NewFlagSet("schedule add", flag.ExitOnError)
		agent, dbPath := agentFlag(fs), dbFlag(fs)
		name := fs.String("name", "", "Unique name (required)")
		cronSpec := fs.String("cron", "", "5-field cron line, e.g. \"0 8 * * 1-5\" (or @daily)")
		every := fs.String("every", "", "Interval, e.g. 10m, 1h30m (minimum 1m)")
		at := fs.String("at", "", "One-shot time, e.g. 2026-09-07T09:00 (read in --tz)")
		tz := fs.String("tz", "", "IANA zone for --cron/--at (default: this machine's, "+scheduler.LocalZone()+")")
		agentTask := fs.String("agent-task", "", "Fire as a task for an agent: the task title")
		body := fs.String("body", "", "Task body for --agent-task")
		to := fs.String("to", "", "Assign the task to this agent (default: --owner, else any agent)")
		quiet := fs.Bool("quiet", false, "Do not deliver the task's result to Telegram")
		command := fs.String("command", "", "Fire as a shell command inside the gateway (no model)")
		cwd := fs.String("cwd", "", "Working directory for --command")
		timeoutS := fs.Int("timeout", 0, "Seconds a --command may run (default: gateway's schedules.command_timeout_seconds)")
		owner := fs.String("owner", "", "Only this gateway fires it (default: whichever gateway sees it first)")
		skipMissed := fs.Bool("skip-missed", false, "After downtime, re-arm instead of running the missed firing once")
		fs.Parse(rest)

		kind, spec, err := pickSpec(*cronSpec, *every, *at)
		if err != nil {
			fmt.Fprintf(os.Stderr, "schedule add: %v\n", err)
			os.Exit(1)
		}
		if *tz == "" {
			*tz = scheduler.LocalZone()
		}
		if (*agentTask == "") == (*command == "") {
			fmt.Fprintln(os.Stderr, "schedule add: give exactly one of --agent-task or --command")
			os.Exit(1)
		}
		now := time.Now()
		next, err := scheduler.NextRun(kind, spec, *tz, now)
		if err != nil {
			fmt.Fprintf(os.Stderr, "schedule add: %v\n", err)
			os.Exit(1)
		}
		if next.IsZero() {
			fmt.Fprintf(os.Stderr, "schedule add: %s is already in the past\n", *at)
			os.Exit(1)
		}
		n := coord.NewSchedule{
			Name: *name, CreatedBy: orAny(*agent, "cli"), OwnerAgent: *owner,
			Kind: kind, Spec: spec, TZ: *tz, SkipMissed: *skipMissed, NextRunAt: next,
		}
		if *agentTask != "" {
			n.PayloadKind = coord.PayloadAgent
			n.Payload = coord.AgentPayload{Title: *agentTask, Body: *body, To: *to, Quiet: *quiet}
		} else {
			n.PayloadKind = coord.PayloadCommand
			n.Payload = coord.CommandPayload{Cmd: *command, Cwd: *cwd, TimeoutS: *timeoutS}
		}

		db := openCoord(*dbPath)
		defer db.Close()
		s, err := db.CreateSchedule(n)
		if err != nil {
			fmt.Fprintf(os.Stderr, "schedule add: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("%s  %s\nfirst run %s (%s)\n", s.ID, s.Name, s.NextRunAt.Local().Format("2006-01-02 15:04:05 MST"), untilText(s.NextRunAt))
		fmt.Println("(a gateway with schedules.enabled=true fires it; check with `bomclaw schedule list`)")

	case "list":
		fs := flag.NewFlagSet("schedule list", flag.ExitOnError)
		dbPath := dbFlag(fs)
		fs.Parse(rest)

		db := openCoord(*dbPath)
		defer db.Close()
		all, err := db.ListSchedules()
		if err != nil {
			fmt.Fprintf(os.Stderr, "schedule list: %v\n", err)
			os.Exit(1)
		}
		if len(all) == 0 {
			fmt.Println("no schedules — add one with `bomclaw schedule add --name X --cron \"0 8 * * 1-5\" --agent-task \"...\"`")
			return
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tWHEN\tNEXT\tLAST\tPAYLOAD\tOWNER")
		for i := range all {
			s := &all[i]
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
				s.Name, scheduler.Describe(s), nextText(s), lastText(s), payloadText(s), orAny(s.OwnerAgent, "any"))
		}
		w.Flush()

	case "show":
		fs := flag.NewFlagSet("schedule show", flag.ExitOnError)
		dbPath := dbFlag(fs)
		limit := fs.Int("runs", 10, "How many recent runs to show")
		fs.Parse(rest)
		s, db := findSchedule(fs, *dbPath, "schedule show")
		defer db.Close()

		fmt.Printf("%s  %s\n", s.ID, s.Name)
		fmt.Printf("when:     %s\n", scheduler.Describe(s))
		fmt.Printf("next:     %s\n", nextText(s))
		fmt.Printf("last:     %s\n", lastText(s))
		fmt.Printf("payload:  %s\n", payloadText(s))
		fmt.Printf("owner:    %s   created by %s   skip_missed %v   system %v\n",
			orAny(s.OwnerAgent, "any gateway"), s.CreatedBy, s.SkipMissed, s.System)
		if s.ConsecutiveFailures > 0 {
			fmt.Printf("failures: %d in a row (turns itself off at %d)\n", s.ConsecutiveFailures, coord.DisableAfterFailures)
		}
		runs, _ := db.ScheduleRuns(s.ID, *limit)
		if len(runs) == 0 {
			fmt.Println("\nno runs yet")
			return
		}
		fmt.Println("\nruns:")
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		for _, r := range runs {
			dur := ""
			if !r.EndedAt.IsZero() {
				dur = r.EndedAt.Sub(r.StartedAt).Round(time.Second).String()
			}
			detail := r.TaskID
			if detail == "" {
				detail = fmt.Sprintf("exit %d", r.ExitCode)
			}
			fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\n", r.StartedAt.Local().Format("01-02 15:04:05"), r.Status, dur, detail, firstLineOf(r.Output, 80))
		}
		w.Flush()

	case "enable", "disable":
		fs := flag.NewFlagSet("schedule "+sub, flag.ExitOnError)
		dbPath := dbFlag(fs)
		fs.Parse(rest)
		s, db := findSchedule(fs, *dbPath, "schedule "+sub)
		defer db.Close()
		if sub == "disable" {
			if err := db.SetScheduleEnabled(s.ID, false, time.Time{}); err != nil {
				fmt.Fprintf(os.Stderr, "schedule disable: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("%s disabled\n", s.Name)
			return
		}
		next, err := scheduler.NextRun(s.Kind, s.Spec, s.TZ, time.Now())
		if err != nil {
			fmt.Fprintf(os.Stderr, "schedule enable: %v\n", err)
			os.Exit(1)
		}
		if next.IsZero() {
			fmt.Fprintf(os.Stderr, "schedule enable: %s is a one-shot whose time (%s) has passed; remove it and add a new one\n", s.Name, s.Spec)
			os.Exit(1)
		}
		if err := db.SetScheduleEnabled(s.ID, true, next); err != nil {
			fmt.Fprintf(os.Stderr, "schedule enable: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("%s enabled — next run %s (%s)\n", s.Name, next.Local().Format("2006-01-02 15:04:05 MST"), untilText(next))

	case "remove", "rm":
		fs := flag.NewFlagSet("schedule remove", flag.ExitOnError)
		dbPath := dbFlag(fs)
		fs.Parse(rest)
		s, db := findSchedule(fs, *dbPath, "schedule remove")
		defer db.Close()
		if err := db.DeleteSchedule(s.ID); err != nil {
			if errors.Is(err, coord.ErrSystemSchedule) {
				fmt.Fprintf(os.Stderr, "schedule remove: %s is owned by the gateway (heartbeat); turn it off in config.yaml instead\n", s.Name)
			} else {
				fmt.Fprintf(os.Stderr, "schedule remove: %v\n", err)
			}
			os.Exit(1)
		}
		fmt.Printf("%s removed\n", s.Name)

	case "run-now", "run":
		fs := flag.NewFlagSet("schedule run-now", flag.ExitOnError)
		dbPath := dbFlag(fs)
		fs.Parse(rest)
		s, db := findSchedule(fs, *dbPath, "schedule run-now")
		defer db.Close()
		if err := db.FireSchedule(s.ID, time.Now()); err != nil {
			fmt.Fprintf(os.Stderr, "schedule run-now: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("%s will fire on the next scheduler tick (within ~30s); its regular cadence resumes from then\n", s.Name)

	default:
		fmt.Fprintf(os.Stderr, "unknown schedule command: %s\n\n", sub)
		scheduleUsage()
		os.Exit(1)
	}
}

// findSchedule reads the positional id-or-name after the flags and loads it.
func findSchedule(fs *flag.FlagSet, dbPath, what string) (*coord.Schedule, *coord.DB) {
	if fs.NArg() != 1 {
		fmt.Fprintf(os.Stderr, "%s: give the schedule's name or id\n", what)
		os.Exit(1)
	}
	db := openCoord(dbPath)
	s, err := db.FindSchedule(fs.Arg(0))
	if err != nil {
		db.Close()
		fmt.Fprintf(os.Stderr, "%s: %v\n", what, err)
		os.Exit(1)
	}
	return s, db
}

// pickSpec turns the three mutually exclusive time flags into kind+spec.
func pickSpec(cronSpec, every, at string) (kind, spec string, err error) {
	set := 0
	for _, v := range []string{cronSpec, every, at} {
		if v != "" {
			set++
		}
	}
	if set != 1 {
		return "", "", fmt.Errorf("give exactly one of --cron, --every or --at")
	}
	switch {
	case cronSpec != "":
		return coord.ScheduleCron, cronSpec, nil
	case every != "":
		return coord.ScheduleEvery, every, nil
	}
	return coord.ScheduleAt, at, nil
}

func nextText(s *coord.Schedule) string {
	switch {
	case !s.Enabled && s.Done():
		return "done"
	case !s.Enabled:
		return "disabled"
	case s.Done():
		return "never"
	}
	return fmt.Sprintf("%s (%s)", s.NextRunAt.Local().Format("01-02 15:04"), untilText(s.NextRunAt))
}

func lastText(s *coord.Schedule) string {
	if s.LastRunAt.IsZero() {
		return "-"
	}
	status := s.LastStatus
	if s.ConsecutiveFailures > 1 {
		status += fmt.Sprintf(" ×%d", s.ConsecutiveFailures)
	}
	return fmt.Sprintf("%s %s ago", status, age(s.LastRunAt))
}

func payloadText(s *coord.Schedule) string {
	switch s.PayloadKind {
	case coord.PayloadAgent:
		var p coord.AgentPayload
		_ = json.Unmarshal(s.Payload, &p)
		out := "task: " + p.Title
		if p.To != "" {
			out += " → " + p.To
		}
		return out
	case coord.PayloadCommand:
		var p coord.CommandPayload
		_ = json.Unmarshal(s.Payload, &p)
		return "sh: " + firstLineOf(p.Cmd, 60)
	}
	return s.PayloadKind
}

// untilText says how far away a time is: "in 4h12m", "3m overdue".
func untilText(t time.Time) string {
	d := time.Until(t).Round(time.Minute)
	if d < 0 {
		return (-d).String() + " overdue"
	}
	if d < time.Minute {
		return "in under a minute"
	}
	return "in " + d.String()
}

func firstLineOf(s string, max int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i] + " …"
	}
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}

func scheduleUsage() {
	fmt.Fprintln(os.Stderr, `Usage: bomclaw schedule <command> [flags]

Timed work, fired by whichever gateway has schedules.enabled=true. A schedule
never runs a model itself: --agent-task creates an ordinary task at the
appointed time (any agent claims it; the result is sent to Telegram);
--command runs a shell command inside the gateway.

Commands:
  add       --name X (--cron "0 8 * * 1-5" | --every 10m | --at 2026-09-07T09:00) [--tz Asia/Ho_Chi_Minh]
            (--agent-task "title" [--body ...] [--to agent] [--quiet] | --command "df -h" [--cwd dir] [--timeout 60])
            [--owner agent] [--skip-missed]
  list                    every schedule with its next and last run
  show <name|id>          details and recent runs
  enable <name|id>        turn on (next run computed from now; failure streak reset)
  disable <name|id>       turn off; keeps the row and history
  remove <name|id>        delete it and its runs
  run-now <name|id>       fire on the next tick, then resume the cadence

After consecutive failures a schedule retries sooner (30s, 1m, 5m, 15m, 1h),
tells you on Telegram at the 2nd failure, and turns itself off at the 10th.
Missed while every gateway was down: runs once on startup (or re-arms with
--skip-missed). Cron: when both day-of-month and day-of-week are set, standard
cron fires when EITHER matches.`)
}
