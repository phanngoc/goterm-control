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
)

// The task/inbox commands are the surface an agent uses to hand work to its
// peer. They talk straight to the shared coordination database rather than
// through a gateway: an agent already has shell access, and a direct write
// keeps working when the peer's gateway is down.

// openCoord resolves the shared database from flags, falling back to the same
// default path the gateways use.
func openCoord(dbPath string) *coord.DB {
	db, err := coord.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "coord: %v\n", err)
		os.Exit(1)
	}
	return db
}

// agentFlag registers the identity flag. An agent running in its own workspace
// gets this from BOMCLAW_AGENT_ID, set by whoever launched it.
func agentFlag(fs *flag.FlagSet) *string {
	def := os.Getenv("BOMCLAW_AGENT_ID")
	if def == "" {
		def = "bomclaw"
	}
	return fs.String("agent", def, "This agent's id (default $BOMCLAW_AGENT_ID)")
}

func dbFlag(fs *flag.FlagSet) *string {
	return fs.String("db", coord.DefaultPath(), "Shared coordination database")
}

func runTask(args []string) {
	if len(args) == 0 {
		taskUsage()
		os.Exit(1)
	}
	sub, rest := args[0], args[1:]

	switch sub {
	case "new":
		fs := flag.NewFlagSet("task new", flag.ExitOnError)
		agent, dbPath := agentFlag(fs), dbFlag(fs)
		title := fs.String("title", "", "Short summary of the work (required)")
		body := fs.String("body", "", "Full description")
		to := fs.String("to", "", "Assign to a specific agent (default: any agent may claim)")
		priority := fs.Int("priority", 0, "Higher is claimed first")
		depth := fs.Int("depth", 0, "Chain depth when an agent spawns follow-up work")
		context := fs.String("context", "", "Existing context id to attach this task to")
		fs.Parse(rest)

		db := openCoord(*dbPath)
		defer db.Close()

		task, err := db.CreateTask(coord.NewTask{
			CreatedBy: *agent, AssignedTo: *to, Title: *title, Body: *body,
			Priority: *priority, Depth: *depth, ContextID: *context,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "task new: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(task.ID)

	case "claim":
		fs := flag.NewFlagSet("task claim", flag.ExitOnError)
		agent, dbPath := agentFlag(fs), dbFlag(fs)
		asJSON := fs.Bool("json", false, "Print the whole task as JSON")
		fs.Parse(rest)

		db := openCoord(*dbPath)
		defer db.Close()

		task, err := db.ClaimTask(*agent)
		if errors.Is(err, coord.ErrNoTask) {
			fmt.Fprintln(os.Stderr, "no claimable task")
			os.Exit(2) // distinct from a real failure so scripts can branch
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "task claim: %v\n", err)
			os.Exit(1)
		}
		if *asJSON {
			b, _ := json.MarshalIndent(task, "", "  ")
			fmt.Println(string(b))
			return
		}
		fmt.Printf("%s\nattempts: %d (pass this to `task done --attempts`)\n\n%s\n\n%s\n",
			task.ID, task.Attempts, task.Title, task.Body)

	case "done", "fail":
		fs := flag.NewFlagSet("task "+sub, flag.ExitOnError)
		agent, dbPath := agentFlag(fs), dbFlag(fs)
		id := fs.String("id", "", "Task id (required)")
		result := fs.String("result", "", "What came of it")
		attempts := fs.Int("attempts", 0, "The attempts value from claim — guards against a lost lease")
		fs.Parse(rest)

		db := openCoord(*dbPath)
		defer db.Close()

		state := coord.TaskCompleted
		if sub == "fail" {
			state = coord.TaskFailed
		}
		// Without an explicit fencing token, fall back to whatever the row
		// says now: still correct for the common single-claim case.
		if *attempts == 0 {
			t, err := db.GetTask(*id)
			if err != nil {
				fmt.Fprintf(os.Stderr, "task %s: %v\n", sub, err)
				os.Exit(1)
			}
			*attempts = t.Attempts
		}
		if err := db.FinishTask(*id, *agent, state, *result, *attempts); err != nil {
			fmt.Fprintf(os.Stderr, "task %s: %v\n", sub, err)
			os.Exit(1)
		}
		fmt.Printf("%s → %s\n", *id, state)

	case "list":
		fs := flag.NewFlagSet("task list", flag.ExitOnError)
		agent, dbPath := agentFlag(fs), dbFlag(fs)
		state := fs.String("state", "", "Filter by state")
		mine := fs.Bool("mine", false, "Only tasks involving this agent")
		limit := fs.Int("limit", 30, "Max rows")
		fs.Parse(rest)

		db := openCoord(*dbPath)
		defer db.Close()

		f := coord.TaskFilter{State: *state, Limit: *limit}
		if *mine {
			f.AgentID = *agent
		}
		tasks, err := db.ListTasks(f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "task list: %v\n", err)
			os.Exit(1)
		}
		if len(tasks) == 0 {
			fmt.Println("no tasks")
			return
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tSTATE\tFROM→TO\tAGE\tTITLE")
		for _, t := range tasks {
			to := t.AssignedTo
			if t.ClaimedBy != "" {
				to = t.ClaimedBy
			}
			if to == "" {
				to = "any"
			}
			fmt.Fprintf(w, "%s\t%s\t%s→%s\t%s\t%s\n",
				t.ID, t.State, t.CreatedBy, to, age(t.CreatedAt), truncate(t.Title, 50))
		}
		w.Flush()

	case "show":
		fs := flag.NewFlagSet("task show", flag.ExitOnError)
		dbPath := dbFlag(fs)
		id := fs.String("id", "", "Task id (required)")
		fs.Parse(rest)

		db := openCoord(*dbPath)
		defer db.Close()

		task, err := db.GetTask(*id)
		if err != nil {
			fmt.Fprintf(os.Stderr, "task show: %v\n", err)
			os.Exit(1)
		}
		events, _ := db.TaskEvents(*id)
		fmt.Printf("%s  [%s]  %s\n", task.ID, task.State, task.Title)
		fmt.Printf("from %s → %s   attempts %d/%d   depth %d\n",
			task.CreatedBy, orAny(task.ClaimedBy, task.AssignedTo), task.Attempts, task.MaxAttempts, task.Depth)
		if task.Body != "" {
			fmt.Printf("\n%s\n", task.Body)
		}
		if task.Result != "" {
			fmt.Printf("\nresult:\n%s\n", task.Result)
		}
		if len(events) > 0 {
			fmt.Println("\nhistory:")
			for _, e := range events {
				fmt.Printf("  %s  %-10s %s %s\n",
					e.CreatedAt.Local().Format("15:04:05"), e.ToState, e.AgentID, e.Note)
			}
		}

	default:
		taskUsage()
		os.Exit(1)
	}
}

func taskUsage() {
	fmt.Fprintln(os.Stderr, `Usage: bomclaw task <command>

  new    --title T [--body B] [--to agent] [--priority N]   create work
  claim  [--json]                                           take the next claimable task
  done   --id ID [--result R] [--attempts N]                finish it
  fail   --id ID [--result R] [--attempts N]                give up on it
  list   [--state S] [--mine] [--limit N]                   see the queue
  show   --id ID                                            one task with its history

Every command accepts --agent (default $BOMCLAW_AGENT_ID) and --db.`)
}

func runInbox(args []string) {
	fs := flag.NewFlagSet("inbox", flag.ExitOnError)
	agent, dbPath := agentFlag(fs), dbFlag(fs)
	unread := fs.Bool("unread", false, "Only messages not yet marked read")
	markRead := fs.Bool("mark-read", false, "Mark everything shown as read")
	all := fs.Bool("all", false, "Show the whole cross-agent conversation, not just this agent's inbox")
	limit := fs.Int("limit", 30, "Max rows")
	fs.Parse(args)

	db := openCoord(*dbPath)
	defer db.Close()

	var (
		msgs []coord.Message
		err  error
	)
	if *all {
		msgs, err = db.RecentMessages(*limit)
	} else {
		msgs, err = db.Inbox(*agent, *unread, *limit)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "inbox: %v\n", err)
		os.Exit(1)
	}
	if len(msgs) == 0 {
		fmt.Println("no messages")
		return
	}

	ids := make([]string, 0, len(msgs))
	for _, m := range msgs {
		flag := " "
		if m.ReadAt.IsZero() {
			flag = "•"
		}
		fmt.Printf("%s %s  %s → %s", flag, m.CreatedAt.Local().Format("01-02 15:04"), m.FromAgent, m.ToAgent)
		if m.TaskID != "" {
			fmt.Printf("  [%s]", m.TaskID)
		}
		fmt.Printf("\n  %s\n", strings.ReplaceAll(m.Body, "\n", "\n  "))
		ids = append(ids, m.ID)
	}

	if *markRead {
		n, err := db.MarkRead(ids)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mark read: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("\nmarked %d read\n", n)
	}
}

func runMsg(args []string) {
	fs := flag.NewFlagSet("msg", flag.ExitOnError)
	agent, dbPath := agentFlag(fs), dbFlag(fs)
	to := fs.String("to", "", "Recipient agent id (required)")
	taskID := fs.String("task", "", "Attach to a task")
	fs.Parse(args)

	body := strings.Join(fs.Args(), " ")
	if *to == "" || body == "" {
		fmt.Fprintln(os.Stderr, "Usage: bomclaw msg --to <agent> [--task <id>] <message>")
		os.Exit(1)
	}

	db := openCoord(*dbPath)
	defer db.Close()

	m, err := db.SendMessage(*agent, *to, *taskID, body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "msg: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(m.ID)
}

func runAgents(args []string) {
	fs := flag.NewFlagSet("agents", flag.ExitOnError)
	dbPath := dbFlag(fs)
	fs.Parse(args)

	db := openCoord(*dbPath)
	defer db.Close()

	agents, err := db.ListAgents()
	if err != nil {
		fmt.Fprintf(os.Stderr, "agents: %v\n", err)
		os.Exit(1)
	}
	if len(agents) == 0 {
		fmt.Println("no agents registered yet")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTATUS\tPROVIDER\tMODEL\tLAST SEEN\tADDRESS")
	for _, a := range agents {
		status := "offline"
		if a.Online {
			status = "online"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			a.ID, status, a.Provider, a.Model, age(a.LastSeenAt), a.WSAddr)
	}
	w.Flush()
}

func age(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func orAny(primary, fallback string) string {
	if primary != "" {
		return primary
	}
	if fallback != "" {
		return fallback
	}
	return "any"
}
