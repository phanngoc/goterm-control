package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/ngocp/goterm-control/internal/coord"
	"github.com/ngocp/goterm-control/internal/gateway"
)

// `bomclaw ch` is the agents' shared room.
//
// `bomclaw msg` still works and is still the right thing for "you, privately";
// this is for work that a third agent should be able to read, join, or pick up
// without being told about it first.

func runChannel(args []string) {
	if len(args) == 0 {
		channelUsage()
		os.Exit(1)
	}
	cmd, rest := args[0], args[1:]

	switch cmd {
	case "list":
		fs := flag.NewFlagSet("ch list", flag.ExitOnError)
		agent, dbPath := agentFlag(fs), dbFlag(fs)
		all := fs.Bool("all", false, "Every channel, not just the ones I am in")
		fs.Parse(rest)

		db := openCoord(*dbPath)
		defer db.Close()

		// --all is a view of every room and belongs to nobody, so it must not
		// demand an identity the caller may not have.
		kind, who := "", ""
		if !*all {
			kind, who = coord.MemberAgent, requireAgent(*agent)
		}
		channels, err := db.ListChannels(kind, who)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ch list: %v\n", err)
			os.Exit(1)
		}
		if len(channels) == 0 {
			fmt.Println("no channels")
			return
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "CHANNEL\tKIND\tMEMBERS\tUNREAD\t@ME\tLAST\tPURPOSE")
		for _, c := range channels {
			names := make([]string, 0, len(c.Members))
			for _, m := range c.Members {
				names = append(names, m.ID)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%s\t%s\n",
				c.ID, c.Kind, strings.Join(names, ","), c.Unread, c.Mentions,
				age(c.LastMessageAt), truncate(c.Purpose, 40))
		}
		w.Flush()

	case "read":
		fs := flag.NewFlagSet("ch read", flag.ExitOnError)
		agent, dbPath := agentFlag(fs), dbFlag(fs)
		thread := fs.String("thread", "", "Read one thread instead of the channel")
		limit := fs.Int("limit", 30, "Max messages")
		keepUnread := fs.Bool("keep-unread", false, "Do not move my read cursor")
		channelID, _ := parseLeading(fs, rest)

		db := openCoord(*dbPath)
		defer db.Close()

		if *thread != "" {
			msgs, err := db.ThreadMessages(*thread)
			if err != nil {
				fmt.Fprintf(os.Stderr, "ch read: %v\n", err)
				os.Exit(1)
			}
			printThread(msgs)
			return
		}
		if channelID == "" {
			fmt.Fprintln(os.Stderr, "Usage: bomclaw ch read <channel-id> [--thread <message-id>]")
			os.Exit(1)
		}
		msgs, err := db.ChannelMessages(channelID, *limit)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ch read: %v\n", err)
			os.Exit(1)
		}
		printChannel(channelID, msgs)
		if !*keepUnread {
			if err := db.MarkChannelRead(channelID, coord.MemberAgent, requireAgent(*agent)); err != nil {
				fmt.Fprintf(os.Stderr, "ch read: %v\n", err)
			}
		}

	case "post":
		fs := flag.NewFlagSet("ch post", flag.ExitOnError)
		agent, dbPath := agentFlag(fs), dbFlag(fs)
		thread := fs.String("thread", "", "Reply inside this thread (a root message id)")
		taskID := fs.String("task", "", "Bind this thread to a task (root messages only)")
		channelID, words := parseLeading(fs, rest)

		body := strings.Join(words, " ")
		if channelID == "" || strings.TrimSpace(body) == "" {
			fmt.Fprintln(os.Stderr, "Usage: bomclaw ch post <channel-id> [--thread <id>] [--task <id>] <message>")
			os.Exit(1)
		}

		db := openCoord(*dbPath)
		defer db.Close()
		m, wake, err := db.PostMessage(coord.NewChannelMessage{
			ChannelID: channelID, ThreadRoot: *thread, AuthorKind: coord.MemberAgent,
			AuthorID: requireAgent(*agent), Body: body, TaskID: *taskID,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "ch post: %v\n", err)
			os.Exit(1)
		}
		// Only a mention rings a doorbell. Posting into a channel nobody was
		// named in is a note on a wall, and waking three agents for every one
		// of those is how a shared room becomes a token bonfire.
		for _, who := range wake {
			gateway.NotifyAgents(db, who, "", "about a mention in "+channelID)
		}
		fmt.Println(m.ID)

	case "new":
		fs := flag.NewFlagSet("ch new", flag.ExitOnError)
		agent, dbPath := agentFlag(fs), dbFlag(fs)
		purpose := fs.String("purpose", "", "What this channel is for")
		members := fs.String("members", "", "Comma-separated agent ids to add (the owner is always in)")
		name, _ := parseLeading(fs, rest)
		if name == "" {
			fmt.Fprintln(os.Stderr, "Usage: bomclaw ch new <name> [--purpose ...] [--members a,b]")
			os.Exit(1)
		}

		db := openCoord(*dbPath)
		defer db.Close()
		me := requireAgent(*agent)
		list := []coord.Member{
			{Kind: coord.MemberAgent, ID: me},
			{Kind: coord.MemberUser, ID: coord.OwnerUserID},
		}
		for _, id := range strings.Split(*members, ",") {
			if id = strings.TrimSpace(id); id != "" && id != me {
				list = append(list, coord.Member{Kind: coord.MemberAgent, ID: id})
			}
		}
		c, err := db.CreateChannel("", name, coord.ChannelPublic, *purpose, me, list)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ch new: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(c.ID)

	case "join":
		fs := flag.NewFlagSet("ch join", flag.ExitOnError)
		agent, dbPath := agentFlag(fs), dbFlag(fs)
		who := fs.String("who", "", "Add this agent instead of me")
		channelID, _ := parseLeading(fs, rest)
		if channelID == "" {
			fmt.Fprintln(os.Stderr, "Usage: bomclaw ch join <channel-id> [--who <agent>]")
			os.Exit(1)
		}
		member := *who
		if member == "" {
			member = requireAgent(*agent)
		}

		db := openCoord(*dbPath)
		defer db.Close()
		if err := db.JoinChannel(channelID, coord.MemberAgent, member); err != nil {
			fmt.Fprintf(os.Stderr, "ch join: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("%s joined %s\n", member, channelID)

	case "mentions":
		fs := flag.NewFlagSet("ch mentions", flag.ExitOnError)
		agent, dbPath := agentFlag(fs), dbFlag(fs)
		markRead := fs.Bool("mark-read", false, "Clear them once shown")
		limit := fs.Int("limit", 30, "Max rows")
		fs.Parse(rest)

		db := openCoord(*dbPath)
		defer db.Close()
		me := requireAgent(*agent)
		msgs, err := db.UnreadMentions(coord.MemberAgent, me, *limit)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ch mentions: %v\n", err)
			os.Exit(1)
		}
		if len(msgs) == 0 {
			fmt.Println("nobody is waiting on you")
			return
		}
		ids := make([]string, 0, len(msgs))
		for _, m := range msgs {
			fmt.Printf("%s  %s  %s\n", m.CreatedAt.Local().Format("01-02 15:04"), m.ChannelID, m.AuthorID)
			if m.ThreadRoot != "" {
				fmt.Printf("  (in thread %s)\n", m.ThreadRoot)
			}
			fmt.Printf("  %s\n", strings.ReplaceAll(m.Body, "\n", "\n  "))
			fmt.Printf("  reply: bomclaw ch post %s --thread %s \"…\"\n\n", m.ChannelID, threadOf(m))
			ids = append(ids, m.ID)
		}
		if *markRead {
			n, err := db.MarkMentionsRead(coord.MemberAgent, me, ids)
			if err != nil {
				fmt.Fprintf(os.Stderr, "ch mentions: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("cleared %d\n", n)
		}

	default:
		channelUsage()
		os.Exit(1)
	}
}

// threadOf is where a reply to this message belongs: its thread if it is in
// one, else itself — replying to a top-level message starts its thread.
func threadOf(m coord.ChannelMessage) string {
	if m.ThreadRoot != "" {
		return m.ThreadRoot
	}
	return m.ID
}

func printChannel(channelID string, msgs []coord.ChannelMessage) {
	if len(msgs) == 0 {
		fmt.Printf("%s is empty\n", channelID)
		return
	}
	// Newest first out of the query; read oldest first.
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		// The id is on every line, not just threaded ones: without it there
		// is no way to start a thread from here, which made --thread
		// unreachable for the agents this command exists for.
		fmt.Printf("%s  %s  %s\n", m.CreatedAt.Local().Format("01-02 15:04"), m.AuthorID, m.ID)
		fmt.Printf("  %s\n", strings.ReplaceAll(m.Body, "\n", "\n  "))
		if m.TaskID != "" {
			fmt.Printf("  ↳ task %s\n", m.TaskID)
		}
		if m.Replies > 0 {
			fmt.Printf("  ↳ %d repl%s, last %s — bomclaw ch read %s --thread %s\n",
				m.Replies, plural(m.Replies), age(m.LastAt), channelID, m.ID)
		}
		fmt.Println()
	}
}

func printThread(msgs []coord.ChannelMessage) {
	for i, m := range msgs {
		prefix := "  "
		if i == 0 {
			prefix = ""
		}
		fmt.Printf("%s%s  %s  %s\n", prefix, m.CreatedAt.Local().Format("01-02 15:04"), m.AuthorID, m.ID)
		fmt.Printf("%s  %s\n", prefix, strings.ReplaceAll(m.Body, "\n", "\n"+prefix+"  "))
		if i == 0 && m.TaskID != "" {
			fmt.Printf("  ↳ task %s\n", m.TaskID)
		}
		fmt.Println()
	}
}

func plural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

func channelUsage() {
	fmt.Fprintln(os.Stderr, strings.TrimSpace(`
Usage: bomclaw ch <command>

  list     [--all]                       channels I am in
  read     <channel> [--thread <id>]     the main line, or one thread
  post     <channel> [--thread <id>] [--task <id>] <message>
  new      <name> [--purpose ...] [--members a,b]
  join     <channel> [--who <agent>]
  mentions [--mark-read]                 lines that named me

A channel is a place: everyone in it reads everything, so another agent can
pick up what you are doing without being told. Only an @mention wakes anybody
— write @bomclaw2 when you need an answer, and just post when you do not.`))
}
