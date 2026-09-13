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
	"github.com/ngocp/goterm-control/internal/gateway"
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

// agentFlag registers the identity flag. The gateway exports BOMCLAW_AGENT_ID
// into every CLI subprocess it spawns, so an agent's own shell commands are
// attributed correctly.
//
// There is deliberately NO fallback default. A wrong-but-plausible id is worse
// than no id: it silently files one agent's tasks, notes and messages under
// another agent's name, and nothing about the output looks wrong.
func agentFlag(fs *flag.FlagSet) *string {
	return fs.String("agent", os.Getenv("BOMCLAW_AGENT_ID"),
		"This agent's id (default $BOMCLAW_AGENT_ID)")
}

// requireAgent exits with a clear message when identity is unknown, rather than
// writing rows under a guessed name.
func requireAgent(id string) string {
	if strings.TrimSpace(id) == "" {
		fmt.Fprintln(os.Stderr,
			"error: this agent's id is unknown.\n"+
				"Pass --agent <id>, or set BOMCLAW_AGENT_ID (the gateway exports it automatically).")
		os.Exit(1)
	}
	return id
}

// parseLeading lets flags follow a positional argument. Go's flag package
// stops at the first non-flag token, so `ch post ch_general --thread X "hi"`
// silently ignored --thread and then failed on a missing identity — while
// being exactly how anyone, agent or person, would type it.
//
// It peels off one leading positional, re-parses what is left for flags, and
// returns the remainder. A body that itself starts with "-" would still be
// taken for a flag; quote it, as you would anywhere else.
func parseLeading(fs *flag.FlagSet, args []string) (first string, rest []string) {
	fs.Parse(args)
	if fs.NArg() == 0 {
		return "", nil
	}
	first = fs.Arg(0)
	fs.Parse(fs.Args()[1:])
	return first, fs.Args()
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
		thread := fs.String("thread", "", "Root message id of the conversation this work came out of")
		fs.Parse(rest)

		db := openCoord(*dbPath)
		defer db.Close()

		me := requireAgent(*agent)
		task, err := db.CreateTask(coord.NewTask{
			CreatedBy: me, AssignedTo: *to, Title: *title, Body: *body,
			Priority: *priority, Depth: *depth, ContextID: *context,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "task new: %v\n", err)
			os.Exit(1)
		}
		// Work that came out of a conversation stays attached to it, in both
		// directions: the thread knows which task it produced, and the task
		// knows where to report back. Without this the agent does as it is
		// told — open a task for the big thing — and the context stays behind.
		if *thread != "" {
			if err := db.BindThreadToTask(*thread, task.ID); err != nil {
				fmt.Fprintf(os.Stderr, "task new: created %s but could not bind it to the thread: %v\n", task.ID, err)
				os.Exit(1)
			}
			if _, _, err := db.PostMessage(coord.NewChannelMessage{
				ChannelID: threadChannel(db, *thread), ThreadRoot: *thread, AuthorID: me,
				Body: fmt.Sprintf("Đã mở task %s cho việc này: %s", task.ID, *title),
			}); err != nil {
				fmt.Fprintf(os.Stderr, "task new: bound, but could not say so in the thread: %v\n", err)
			}
		}
		// Ring the agent that can take it. Failure is fine — its poll finds
		// the task anyway; this only removes the wait.
		gateway.NotifyTaskCreated(db, task)
		fmt.Println(task.ID)

	case "sub":
		// A child of a task this agent holds: inherits context, one level deeper.
		fs := flag.NewFlagSet("task sub", flag.ExitOnError)
		agent, dbPath := agentFlag(fs), dbFlag(fs)
		parent := fs.String("parent", "", "The task you are splitting (required)")
		title := fs.String("title", "", "Short summary of the piece (required)")
		body := fs.String("body", "", "Full description — enough that whoever claims it needs nothing else (required)")
		acceptance := fs.String("acceptance", "", "How the claimer knows it is done")
		inputs := fs.String("input", "", "Comma-separated artifact ids to hand down")
		to := fs.String("to", "", "Assign to a specific agent (default: any agent may claim)")
		priority := fs.Int("priority", 0, "Higher is claimed first")
		fs.Parse(rest)
		if *parent == "" {
			fmt.Fprintln(os.Stderr, "task sub: --parent is required")
			os.Exit(1)
		}
		var inputIDs []string
		for _, id := range strings.Split(*inputs, ",") {
			if id = strings.TrimSpace(id); id != "" {
				inputIDs = append(inputIDs, id)
			}
		}

		db := openCoord(*dbPath)
		defer db.Close()
		child, err := db.CreateSubTask(*parent, requireAgent(*agent), coord.NewTask{
			AssignedTo: *to, Title: *title, Body: *body, Priority: *priority,
			Acceptance: *acceptance, Inputs: inputIDs,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "task sub: %v\n", err)
			os.Exit(1)
		}
		gateway.NotifyTaskCreated(db, child)
		fmt.Println(child.ID)
		fmt.Fprintf(os.Stderr, "(when every child is done, `bomclaw task block --id %s --on children` wakes you with their results)\n", *parent)

	case "claim":
		fs := flag.NewFlagSet("task claim", flag.ExitOnError)
		agent, dbPath := agentFlag(fs), dbFlag(fs)
		asJSON := fs.Bool("json", false, "Print the whole task as JSON")
		fs.Parse(rest)

		db := openCoord(*dbPath)
		defer db.Close()

		task, err := db.ClaimTask(requireAgent(*agent))
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
		if err := db.FinishTask(*id, requireAgent(*agent), state, *result, *attempts); err != nil {
			fmt.Fprintf(os.Stderr, "task %s: %v\n", sub, err)
			os.Exit(1)
		}
		fmt.Printf("%s → %s\n", *id, state)

	case "cancel":
		// A person stopping work. Takes every unfinished child with it: a
		// canceled parent never comes back for their results.
		fs := flag.NewFlagSet("task cancel", flag.ExitOnError)
		agent, dbPath := agentFlag(fs), dbFlag(fs)
		id := fs.String("id", "", "Task id (required)")
		fs.Parse(rest)

		db := openCoord(*dbPath)
		defer db.Close()
		children, err := db.CancelTaskTree(*id, requireAgent(*agent))
		if err != nil {
			fmt.Fprintf(os.Stderr, "task cancel: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("%s canceled", *id)
		if len(children) > 0 {
			fmt.Printf(" — and %d unfinished child task(s): %s", len(children), strings.Join(children, ", "))
		}
		fmt.Println()

	case "progress":
		// The agent's own "here is how far I got". Fed into the next run's
		// prompt, so a run that hits its time cap loses nothing it wrote down.
		fs := flag.NewFlagSet("task progress", flag.ExitOnError)
		agent, dbPath := agentFlag(fs), dbFlag(fs)
		id := fs.String("id", "", "Task id (required)")
		note := fs.String("note", "", "Where the work stands and what is left (required)")
		attempts := fs.Int("attempts", 0, "The attempts value from claim (default: read from the task)")
		fs.Parse(rest)
		if *note == "" && fs.NArg() > 0 {
			*note = strings.Join(fs.Args(), " ")
		}

		db := openCoord(*dbPath)
		defer db.Close()
		if *attempts == 0 {
			*attempts = currentAttempts(db, *id, "task progress")
		}
		if err := db.SetCheckpoint(*id, requireAgent(*agent), *attempts, *note); err != nil {
			fmt.Fprintf(os.Stderr, "task progress: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("%s checkpoint recorded\n", *id)

	case "block":
		// Park the task until a person answers or its children finish. The
		// run ends right after; the system calls you back when it is unblocked.
		fs := flag.NewFlagSet("task block", flag.ExitOnError)
		agent, dbPath := agentFlag(fs), dbFlag(fs)
		id := fs.String("id", "", "Task id (required)")
		on := fs.String("on", coord.BlockedOnHuman, "What it waits for: human | children")
		note := fs.String("note", "", "What you need (required for --on human)")
		attempts := fs.Int("attempts", 0, "The attempts value from claim (default: read from the task)")
		fs.Parse(rest)

		db := openCoord(*dbPath)
		defer db.Close()
		if *attempts == 0 {
			*attempts = currentAttempts(db, *id, "task block")
		}
		if err := db.BlockTask(*id, requireAgent(*agent), *attempts, *on, *note); err != nil {
			fmt.Fprintf(os.Stderr, "task block: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("%s → blocked on %s\n", *id, *on)

	case "unblock", "answer":
		// A person (or the last child) freeing a blocked task. --note is the
		// answer; it lands in the checkpoint so the next run reads it.
		fs := flag.NewFlagSet("task "+sub, flag.ExitOnError)
		agent, dbPath := agentFlag(fs), dbFlag(fs)
		id := fs.String("id", "", "Task id (required)")
		note := fs.String("note", "", "The answer, or why it is free now")
		fs.Parse(rest)
		if *note == "" && fs.NArg() > 0 {
			*note = strings.Join(fs.Args(), " ")
		}

		db := openCoord(*dbPath)
		defer db.Close()
		by := *agent
		if strings.TrimSpace(by) == "" {
			by = "human"
		}
		// The answer is merged into the checkpoint by UnblockTask itself, in the
		// same guarded statement — so it cannot land on a task that turns out
		// not to be blocked, and the admin API gets the same behaviour for free.
		if err := db.UnblockTask(*id, by, *note); err != nil {
			fmt.Fprintf(os.Stderr, "task %s: %v\n", sub, err)
			os.Exit(1)
		}
		gateway.NotifyAgents(db, "", "", "about unblocked "+*id)
		fmt.Printf("%s → submitted\n", *id)

	case "resume":
		// Reopen a task the system gave up on (exhausted attempts or
		// continuations). A person's call; grants more of whichever ran out.
		fs := flag.NewFlagSet("task resume", flag.ExitOnError)
		agent, dbPath := agentFlag(fs), dbFlag(fs)
		id := fs.String("id", "", "Task id (required)")
		more := fs.Int("more", 5, "How many more attempts/continuations to allow")
		fs.Parse(rest)

		db := openCoord(*dbPath)
		defer db.Close()
		by := *agent
		if strings.TrimSpace(by) == "" {
			by = "human"
		}
		if err := db.ResumeTask(*id, by, *more); err != nil {
			fmt.Fprintf(os.Stderr, "task resume: %v\n", err)
			os.Exit(1)
		}
		gateway.NotifyAgents(db, "", "", "about resumed "+*id)
		fmt.Printf("%s → submitted (+%d)\n", *id, *more)

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
		runs, _ := db.TaskRuns(*id)
		state := task.State
		if task.FailReason != "" {
			state += " (" + task.FailReason + ")"
		} else if task.BlockedOn != "" {
			state += " on " + task.BlockedOn
		}
		fmt.Printf("%s  [%s]  %s\n", task.ID, state, task.Title)
		fmt.Printf("from %s → %s   attempts %d/%d   continuations %d/%d   depth %d   kind %s\n",
			task.CreatedBy, orAny(task.ClaimedBy, task.AssignedTo), task.Attempts, task.MaxAttempts,
			task.Continuations, task.MaxContinuations, task.Depth, task.Kind)
		if ref := coord.ParseSessionRef(task.SessionRef); ref.SessionID != "" {
			fmt.Printf("session: %s/%s%s (next run resumes it on %s)\n",
				ref.Provider, shortID(ref.SessionID), accountSuffix(ref.Account), orAny(task.AssignedTo, "any agent"))
		}
		if task.Body != "" {
			fmt.Printf("\n%s\n", task.Body)
		}
		if task.Checkpoint != "" {
			fmt.Printf("\ncheckpoint:\n%s\n", task.Checkpoint)
		}
		if task.Acceptance != "" {
			fmt.Printf("\naccepted if:\n%s\n", task.Acceptance)
		}
		if task.Result != "" {
			fmt.Printf("\nresult:\n%s\n", task.Result)
		}
		if arts, err := db.TaskArtifacts(task.ID); err == nil && len(arts) > 0 {
			fmt.Println("\nartifacts:")
			printArtifacts(arts)
		}
		if len(runs) > 0 {
			fmt.Println("\nruns:")
			for i, r := range runs {
				dur := "…"
				if !r.EndedAt.IsZero() {
					dur = r.EndedAt.Sub(r.StartedAt).Round(time.Second).String()
				}
				line := fmt.Sprintf("  %d. %s  %-10s %-8s %s", i+1, r.StartedAt.Local().Format("15:04:05"), r.Liveness, dur, r.AgentID)
				if r.Note != "" {
					line += "  — " + truncate(r.Note, 70)
				}
				fmt.Println(line)
			}
		}
		if children, _ := db.Children(*id); len(children) > 0 {
			fmt.Printf("\nchildren (%d):\n", len(children))
			for _, c := range children {
				line := fmt.Sprintf("  ↳ %s  [%s]  %s", c.ID, c.State, c.Title)
				if c.ClaimedBy != "" {
					line += "  by " + c.ClaimedBy
				}
				if c.FailReason != "" {
					line += "  (" + c.FailReason + ")"
				}
				fmt.Println(line)
			}
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

// currentAttempts reads the fencing token off the row when the caller did not
// pass one — correct for the common single-claim case, same as `task done`.
func currentAttempts(db *coord.DB, id, cmd string) int {
	t, err := db.GetTask(id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", cmd, err)
		os.Exit(1)
	}
	return t.Attempts
}

func shortID(s string) string {
	if len(s) > 12 {
		return s[:12] + "…"
	}
	return s
}

func accountSuffix(a string) string {
	if a == "" {
		return ""
	}
	return " (account " + a + ")"
}

func taskUsage() {
	fmt.Fprintln(os.Stderr, `Usage: bomclaw task <command>

  new    --title T [--body B] [--to agent] [--priority N]   create work
  sub    --parent ID --title T --body B [--acceptance A]    split a piece off a task you hold (max 8 open)
         [--to agent] [--input a_id,a_id]                   the body must stand alone: another agent may claim it
  claim  [--json]                                           take the next claimable task
  done   --id ID [--result R] [--attempts N]                finish it
  fail   --id ID [--result R] [--attempts N]                give up on it
  cancel --id ID                                            (person) stop it, and every unfinished child with it
  progress --id ID --note "..."                             record how far you got (fed to the next run)
  block  --id ID --on human|children [--note "..."]         park it until answered / children finish
  answer --id ID --note "..."                               (person) unblock with an answer
  resume --id ID [--more N]                                 (person) reopen a task the system gave up on
  list   [--state S] [--mine] [--limit N]                   see the queue
  show   --id ID                                            one task: runs, checkpoint, history

Every command accepts --agent (default $BOMCLAW_AGENT_ID) and --db.`)
}

func runNote(args []string) {
	if len(args) == 0 {
		noteUsage()
		os.Exit(1)
	}
	sub, rest := args[0], args[1:]

	switch sub {
	case "add":
		fs := flag.NewFlagSet("note add", flag.ExitOnError)
		agent, dbPath := agentFlag(fs), dbFlag(fs)
		notesFile := fs.String("file", coord.DefaultNotesFile(), "Markdown file to regenerate")
		title := fs.String("title", "", "One line summary (required)")
		body := fs.String("body", "", "The detail")
		kind := fs.String("kind", coord.KindFact, "fact | decision | result | gotcha")
		tags := fs.String("tags", "", "Comma separated")
		private := fs.Bool("private", false, "Visible only to this agent")
		supersedes := fs.String("supersedes", "", "Id of the note this corrects")
		fs.Parse(rest)

		db := openCoord(*dbPath)
		defer db.Close()

		scope := ""
		if *private {
			scope = *agent
		}
		note, err := db.AddNote(coord.NewNote{
			Author: requireAgent(*agent), Scope: scope, Kind: *kind, Title: *title,
			Body: *body, Tags: *tags, Supersedes: *supersedes,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "note add: %v\n", err)
			os.Exit(1)
		}
		if err := db.WriteNotesFile(*notesFile); err != nil {
			// The note is safely stored; only the readable copy failed.
			fmt.Fprintf(os.Stderr, "note add: saved, but could not rewrite %s: %v\n", *notesFile, err)
		}
		fmt.Println(note.ID)

	case "search":
		fs := flag.NewFlagSet("note search", flag.ExitOnError)
		agent, dbPath := agentFlag(fs), dbFlag(fs)
		limit := fs.Int("limit", 20, "Max rows")
		fs.Parse(rest)

		db := openCoord(*dbPath)
		defer db.Close()

		notes, err := db.SearchNotes(strings.Join(fs.Args(), " "), *agent, *limit)
		if err != nil {
			fmt.Fprintf(os.Stderr, "note search: %v\n", err)
			os.Exit(1)
		}
		printNotes(notes)

	case "list":
		fs := flag.NewFlagSet("note list", flag.ExitOnError)
		agent, dbPath := agentFlag(fs), dbFlag(fs)
		kind := fs.String("kind", "", "Filter by kind")
		all := fs.Bool("all", false, "Include superseded notes")
		limit := fs.Int("limit", 50, "Max rows")
		fs.Parse(rest)

		db := openCoord(*dbPath)
		defer db.Close()

		notes, err := db.ListNotes(coord.NoteFilter{
			Scope: *agent, Kind: *kind, IncludeReplaced: *all, Limit: *limit,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "note list: %v\n", err)
			os.Exit(1)
		}
		printNotes(notes)

	case "render":
		fs := flag.NewFlagSet("note render", flag.ExitOnError)
		dbPath := dbFlag(fs)
		notesFile := fs.String("file", coord.DefaultNotesFile(), "Markdown file to write")
		fs.Parse(rest)

		db := openCoord(*dbPath)
		defer db.Close()

		if err := db.WriteNotesFile(*notesFile); err != nil {
			fmt.Fprintf(os.Stderr, "note render: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(*notesFile)

	default:
		noteUsage()
		os.Exit(1)
	}
}

func printNotes(notes []coord.Note) {
	if len(notes) == 0 {
		fmt.Println("no notes")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tKIND\tSCOPE\tAUTHOR\tAGE\tTITLE")
	for _, n := range notes {
		title := n.Title
		if n.SupersededBy != "" {
			title = "(replaced) " + title
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			n.ID, n.Kind, n.Scope, n.Author, age(n.CreatedAt), truncate(title, 60))
	}
	w.Flush()
}

func noteUsage() {
	fmt.Fprintln(os.Stderr, `Usage: bomclaw note <command>

  add    --title T [--body B] [--kind fact|decision|result|gotcha]
         [--tags a,b] [--private] [--supersedes ID]     record what you learned
  search <words>                                        full-text search
  list   [--kind K] [--all]                             browse
  render [--file PATH]                                  regenerate the markdown file

Notes are append-only: correct one with --supersedes, never by editing.
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
		msgs, err = db.Inbox(requireAgent(*agent), *unread, *limit)
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
		n, err := db.MarkRead(requireAgent(*agent), ids)
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

	m, err := db.SendMessage(requireAgent(*agent), *to, *taskID, body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "msg: %v\n", err)
		os.Exit(1)
	}
	// A message about a task is read by the task's next run; ring the
	// recipient so that run starts now if the task is waiting (§5.4 comment).
	if *taskID != "" {
		gateway.NotifyAgents(db, *to, "", "about a message on "+*taskID)
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

// threadChannel is the room a thread lives in. The caller already named the
// thread, and making it also name the channel would be asking for a fact the
// database holds.
func threadChannel(db *coord.DB, rootID string) string {
	channelID, err := db.MessageChannel(rootID)
	if err != nil || channelID == "" {
		return coord.GeneralChannelID
	}
	return channelID
}
