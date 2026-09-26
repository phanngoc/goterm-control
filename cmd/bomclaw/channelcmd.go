package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

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
		msgs, err := db.ChannelMessages(channelID, *limit, time.Time{})
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
		plain := fs.Bool("no-workspace", false, "A room with no project folder behind it")
		projectsDir := fs.String("projects-dir", "", "Where project folders live (default ~/goterm-projects)")
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
		// A new room is a project by default: a folder, a brief, somewhere for
		// the work to land. --no-workspace opts out for a room that is only a
		// room, the way #general is.
		var (
			c   *coord.Channel
			err error
		)
		if *plain {
			c, err = db.CreateChannel("", name, coord.ChannelPublic, *purpose, me, list)
		} else {
			c, err = db.CreateProject(name, *purpose, me, *projectsDir, list)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "ch new: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(c.ID)
		if c.Workspace != "" {
			fmt.Printf("workspace: %s\n", c.Workspace)
			fmt.Printf("brief:     %s\n", filepath.Join(c.Workspace, coord.AgentsFile))
		}

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

	case "bind":
		fs := flag.NewFlagSet("ch bind", flag.ExitOnError)
		agent, dbPath := agentFlag(fs), dbFlag(fs)
		kind := fs.String("kind", coord.GatewayTelegram, "telegram | webhook")
		// A private chat's id is the user's id, and the gateway exports the
		// first trusted user as exactly that. Typing it by hand is a chance to
		// get it wrong for no benefit — there is only one owner.
		chat := fs.Int64("chat", ownerChatID(), "Telegram chat id (default $BOMCLAW_OWNER_CHAT_ID)")
		target := fs.String("target", "", "Where it speaks: a chat id, or a webhook URL")
		secret := fs.String("secret", "", "Bearer token sent with a webhook")
		label := fs.String("label", "", "What to call this destination on screen")
		mode := fs.String("mode", "", "all | mentions | off (default depends on the kind)")
		pause := fs.Bool("pause", false, "Stop the traffic, keep the destination")
		resume := fs.Bool("resume", false, "Start it again, from now")
		off := fs.Bool("off", false, "Remove a destination entirely")
		channelID, _ := parseLeading(fs, rest)

		db := openCoord(*dbPath)
		defer db.Close()

		if channelID == "" {
			listGateways(db, "")
			return
		}
		switch {
		case *pause:
			*mode = coord.ForwardOff
		case *resume && *mode == "":
			*mode = coord.ForwardMentions
		}
		// --chat is the old spelling, kept because the muscle memory and the
		// env default both live on it.
		if *target == "" && *kind == coord.GatewayTelegram && *chat != 0 {
			*target = strconv.FormatInt(*chat, 10)
		}

		if *off {
			g := pickGateway(db, channelID, *kind, *target, "remove")
			if err := db.RemoveChannelGateway(g.ID); err != nil {
				fmt.Fprintf(os.Stderr, "ch bind: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("%s ✗ %s %s — no longer carries this room\n", channelID, g.Kind, g.Target)
			return
		}
		// Changing the mode of a destination that already exists is an edit,
		// not a re-bind: it must not need the target spelled out again.
		if *mode != "" && *target == "" {
			g := pickGateway(db, channelID, *kind, "", "change")
			if _, err := db.UpdateChannelGateway(g.ID, *mode, *label, "", *secret); err != nil {
				fmt.Fprintf(os.Stderr, "ch bind: %v\n", err)
				os.Exit(1)
			}
			listGateways(db, channelID)
			return
		}
		if *target == "" {
			fmt.Fprintln(os.Stderr,
				"error: no destination.\n"+
					"For Telegram pass --chat <id>, or run this from a shell the gateway spawned "+
					"(it exports BOMCLAW_OWNER_CHAT_ID).\n"+
					"For a webhook pass --target https://…")
			os.Exit(1)
		}
		// The gateway names the process that carries it: each agent here has
		// its own bot, and a chat id alone does not say which one speaks into
		// it.
		g, err := db.AddChannelGateway(coord.ChannelGateway{
			ChannelID: channelID, Kind: *kind, AgentID: requireAgent(*agent),
			Target: *target, Secret: *secret, Mode: *mode, Label: *label,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "ch bind: %v\n", err)
			os.Exit(1)
		}
		switch g.Mode {
		case coord.ForwardAll:
			fmt.Printf("%s → %s %s via %s: every line\n", channelID, g.Kind, g.Target, g.AgentID)
		case coord.ForwardOff:
			fmt.Printf("%s → %s %s via %s: paused (destination kept)\n", channelID, g.Kind, g.Target, g.AgentID)
		default:
			fmt.Printf("%s → %s %s via %s: lines that name the owner, and replies in threads they are in\n",
				channelID, g.Kind, g.Target, g.AgentID)
		}
		listGateways(db, channelID)
		fmt.Println("Only what is said from now on travels — the room's history stays here.")

	case "archive":
		fs := flag.NewFlagSet("ch archive", flag.ExitOnError)
		dbPath := dbFlag(fs)
		channelID, _ := parseLeading(fs, rest)
		if channelID == "" {
			fmt.Fprintln(os.Stderr, "Usage: bomclaw ch archive <channel-id>")
			os.Exit(1)
		}
		db := openCoord(*dbPath)
		defer db.Close()
		if err := db.ArchiveChannel(channelID); err != nil {
			fmt.Fprintf(os.Stderr, "ch archive: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("%s is out of the list. Nothing said in it was deleted.\n", channelID)

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

// listGateways prints where a room speaks, or where every room does when
// channelID is empty. Printed after a change too: the whole point of the
// change is that a room can have several, and a count is only visible if it is
// shown.
func listGateways(db *coord.DB, channelID string) {
	gws, err := db.ChannelGateways(channelID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ch bind: %v\n", err)
		os.Exit(1)
	}
	if len(gws) == 0 {
		if channelID == "" {
			fmt.Println("no room speaks anywhere outside the dashboard")
		} else {
			fmt.Printf("%s speaks nowhere outside the dashboard\n", channelID)
		}
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "CHANNEL\tKIND\tVIA\tTARGET\tMODE\tLABEL\tSINCE")
	for _, g := range gws {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			g.ChannelID, g.Kind, g.AgentID, g.Target, g.Mode, g.Label, age(g.Since))
	}
	w.Flush()
}

// pickGateway resolves which destination a command means.
//
// A room used to have at most one, so --off needed nothing but the room. Now
// the flags narrow it, and when they do not narrow it to one the command
// prints the list and stops — guessing which of someone's destinations to
// remove is not a guess worth making.
func pickGateway(db *coord.DB, channelID, kind, target, verb string) coord.ChannelGateway {
	gws, err := db.ChannelGateways(channelID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ch bind: %v\n", err)
		os.Exit(1)
	}
	var match []coord.ChannelGateway
	for _, g := range gws {
		if target != "" && g.Target != target {
			continue
		}
		if target == "" && g.Kind != kind {
			continue
		}
		match = append(match, g)
	}
	// Exactly one destination and nothing to disambiguate: that is what was
	// meant, whatever the flags said.
	if len(match) == 0 && len(gws) == 1 {
		match = gws
	}
	switch len(match) {
	case 1:
		return match[0]
	case 0:
		fmt.Fprintf(os.Stderr, "error: %s has no such destination to %s.\n", channelID, verb)
		listGateways(db, channelID)
		os.Exit(1)
	default:
		fmt.Fprintf(os.Stderr, "error: %s has %d destinations — say which one with --target.\n",
			channelID, len(match))
		listGateways(db, channelID)
		os.Exit(1)
	}
	return coord.ChannelGateway{}
}

// ownerChatID is the Telegram conversation the gateway said belongs to the
// owner. Zero when this shell was not spawned by a gateway, which the command
// reports rather than guessing a number.
func ownerChatID() int64 {
	id, err := strconv.ParseInt(strings.TrimSpace(os.Getenv("BOMCLAW_OWNER_CHAT_ID")), 10, 64)
	if err != nil {
		return 0
	}
	return id
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
  bind     [<channel>] [--kind telegram|webhook] [--target X]
           [--mode all|mentions|off] [--pause|--resume] [--off]
                                                        where the room speaks
  archive  <channel>                     hide it; nothing said in it is lost
  mentions [--mark-read]                 lines that named me

A channel is a place: everyone in it reads everything, so another agent can
pick up what you are doing without being told. Only an @mention wakes anybody
— write @bomclaw2 when you need an answer, and just post when you do not.`))
}
