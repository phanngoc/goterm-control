package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ngocp/goterm-control/internal/coord"
	"github.com/ngocp/goterm-control/internal/scheduler"
)

// `bomclaw heartbeat` is how an agent (or a person) edits the scratchpad the
// agent's heartbeat reads, and how they see whether the heartbeat is on and
// what it did last. Like `task` and `schedule`, it writes straight to the
// shared database; the gateway's scheduler loop does the rest.

func runHeartbeat(args []string) {
	if len(args) == 0 {
		heartbeatUsage()
		os.Exit(1)
	}
	sub, rest := args[0], args[1:]

	switch sub {
	case "scratch":
		fs := flag.NewFlagSet("heartbeat scratch", flag.ExitOnError)
		agent, dbPath := agentFlag(fs), dbFlag(fs)
		set := fs.String("set", "", "Replace the scratchpad with this text")
		appendLine := fs.String("append", "", "Add one dated line")
		clear := fs.Bool("clear", false, "Empty the scratchpad (the heartbeat then does nothing)")
		fs.Bool("show", false, "Print the scratchpad (the default when no other flag is given)")
		fs.Parse(rest)
		id := requireAgent(*agent)

		db := openCoord(*dbPath)
		defer db.Close()
		var err error
		switch {
		case *clear:
			err = db.SetScratch(id, "")
		case *set != "":
			err = db.SetScratch(id, *set)
		case *appendLine != "":
			err = db.AppendScratch(id, *appendLine, time.Now())
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "heartbeat scratch: %v\n", err)
			os.Exit(1)
		}
		scratch, err := db.Scratch(id)
		if err != nil {
			fmt.Fprintf(os.Stderr, "heartbeat scratch: %v\n", err)
			os.Exit(1)
		}
		if strings.TrimSpace(scratch) == "" {
			fmt.Println("(scratchpad is empty — the heartbeat has nothing to look at and skips)")
			return
		}
		fmt.Println(scratch)

	case "status":
		fs := flag.NewFlagSet("heartbeat status", flag.ExitOnError)
		agent, dbPath := agentFlag(fs), dbFlag(fs)
		fs.Parse(rest)
		id := requireAgent(*agent)

		db := openCoord(*dbPath)
		defer db.Close()
		s, err := db.FindSchedule(scheduler.HeartbeatName(id))
		if err != nil {
			fmt.Printf("heartbeat for %s: never configured (set heartbeat.enabled: true in config.yaml and restart)\n", id)
			return
		}
		state := "on"
		if !s.Enabled {
			state = "off"
		}
		fmt.Printf("heartbeat for %s: %s — %s\n", id, state, scheduler.Describe(s))
		if s.Enabled {
			fmt.Printf("next look: %s\n", nextText(s))
		}
		fmt.Printf("last:      %s\n", lastText(s))
		if s.ConsecutiveFailures > 0 {
			fmt.Printf("failures:  %d in a row\n", s.ConsecutiveFailures)
		}
		scratch, _ := db.Scratch(id)
		if strings.TrimSpace(scratch) == "" {
			fmt.Println("scratch:   (empty — every look is skipped for free)")
		} else {
			fmt.Printf("scratch:   %d bytes\n%s\n", len(scratch), indent(scratch))
		}
		if runs, _ := db.ScheduleRuns(s.ID, 5); len(runs) > 0 {
			fmt.Println("\nrecent looks that ran a task:")
			for _, r := range runs {
				fmt.Printf("  %s  %s  %s  %s\n", r.StartedAt.Local().Format("01-02 15:04"), r.Status, r.TaskID, firstLineOf(r.Output, 70))
			}
		}

	case "run-now", "run":
		fs := flag.NewFlagSet("heartbeat run-now", flag.ExitOnError)
		agent, dbPath := agentFlag(fs), dbFlag(fs)
		fs.Parse(rest)
		id := requireAgent(*agent)

		db := openCoord(*dbPath)
		defer db.Close()
		s, err := db.FindSchedule(scheduler.HeartbeatName(id))
		if err != nil {
			fmt.Fprintf(os.Stderr, "heartbeat run-now: %s has no heartbeat configured\n", id)
			os.Exit(1)
		}
		if err := db.FireSchedule(s.ID, time.Now()); err != nil {
			fmt.Fprintf(os.Stderr, "heartbeat run-now: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("%s will look on the next scheduler tick (within ~30s); skip rules still apply\n", s.Name)

	default:
		fmt.Fprintf(os.Stderr, "unknown heartbeat command: %s\n\n", sub)
		heartbeatUsage()
		os.Exit(1)
	}
}

func indent(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := range lines {
		lines[i] = "  " + lines[i]
	}
	return strings.Join(lines, "\n")
}

func heartbeatUsage() {
	fmt.Fprintf(os.Stderr, `Usage: bomclaw heartbeat <command> [flags]

The heartbeat is a periodic look at this agent's scratchpad — the notes it
left itself to follow up on. It runs as a task in its own session, only when
the scratchpad is non-empty, the agent is idle and the clock is inside
heartbeat.active_hours. A look that finds nothing answers %s and nobody is
told; anything else is sent to Telegram.

Commands:
  scratch [--show]        show the scratchpad
  scratch --set "..."     replace it
  scratch --append "..."  add one dated line
  scratch --clear         empty it (the heartbeat then costs nothing)
  status                  on/off, next look, last outcome, scratchpad
  run-now                 look on the next tick instead of waiting

Turn it on per agent in config.yaml:
  heartbeat: {enabled: true, every: "30m", active_hours: "08:00-23:00"}
It also needs schedules.enabled and tasks.auto_claim on the same gateway.
Cap: %d KB of scratch.
`, "NO_REPLY", coord.MaxScratch/1024)
}
