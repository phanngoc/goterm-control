package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/ngocp/goterm-control/internal/coord"
)

// `bomclaw artifact` is how work leaves one task and reaches another.
//
// Before it, an agent that produced a patch had two options: paste it into the
// task result, where the parent's summary cut it off, or drop it in
// ~/goterm-shared/mailbox/ and name the path in a message — which is what they
// actually did, with no index, no owner and no lifecycle.

func runArtifact(args []string) {
	if len(args) == 0 {
		artifactUsage()
		os.Exit(1)
	}
	cmd, rest := args[0], args[1:]

	switch cmd {
	case "put":
		fs := flag.NewFlagSet("artifact put", flag.ExitOnError)
		agent, dbPath := agentFlag(fs), dbFlag(fs)
		taskID := fs.String("task", "", "Task this belongs to (required)")
		kind := fs.String("kind", coord.ArtifactFile, "document | patch | file | link | result")
		title := fs.String("title", "", "What this is, in a few words (required)")
		file := fs.String("file", "", "Read content from this file (default: stdin)")
		name := fs.String("name", "", "Filename to store it under (default: from --file or --title)")
		url := fs.String("url", "", "For --kind link")
		contentType := fs.String("content-type", "", "MIME type, if it matters")
		fs.Parse(rest)

		if *taskID == "" || *title == "" {
			fmt.Fprintln(os.Stderr, "Usage: bomclaw artifact put --task <id> --title <what it is> [--kind patch] [--file p.diff]")
			os.Exit(1)
		}

		n := coord.NewArtifact{
			TaskID: *taskID, Kind: *kind, Title: *title, Filename: *name,
			ContentType: *contentType, URL: *url, CreatedBy: requireAgent(*agent),
		}
		if *kind != coord.ArtifactLink {
			content, err := readContent(*file)
			if err != nil {
				fmt.Fprintf(os.Stderr, "artifact put: %v\n", err)
				os.Exit(1)
			}
			n.Content = content
			if n.Filename == "" && *file != "" {
				n.Filename = filepath.Base(*file)
			}
		}

		db := openCoord(*dbPath)
		defer db.Close()
		a, err := db.PutArtifact(n)
		if err != nil {
			fmt.Fprintf(os.Stderr, "artifact put: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(a.ID)

	case "list":
		fs := flag.NewFlagSet("artifact list", flag.ExitOnError)
		dbPath := dbFlag(fs)
		taskID := fs.String("task", "", "List a task's artifacts")
		contextID := fs.String("context", "", "List a whole task tree's artifacts")
		tree := fs.Bool("tree", false, "With --task: the task's whole tree, not just this task")
		fs.Parse(rest)

		db := openCoord(*dbPath)
		defer db.Close()

		var (
			arts []coord.Artifact
			err  error
		)
		switch {
		case *contextID != "":
			arts, err = db.ContextArtifacts(*contextID)
		case *taskID != "" && *tree:
			var t *coord.Task
			if t, err = db.GetTask(*taskID); err == nil {
				arts, err = db.ContextArtifacts(t.ContextID)
			}
		case *taskID != "":
			arts, err = db.TaskArtifacts(*taskID)
		default:
			fmt.Fprintln(os.Stderr, "artifact list: pass --task or --context")
			os.Exit(1)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "artifact list: %v\n", err)
			os.Exit(1)
		}
		if len(arts) == 0 {
			fmt.Println("no artifacts")
			return
		}
		printArtifacts(arts)

	case "get":
		fs := flag.NewFlagSet("artifact get", flag.ExitOnError)
		dbPath := dbFlag(fs)
		out := fs.String("out", "", "Write to this file instead of stdout")
		fs.Parse(rest)
		if fs.NArg() != 1 {
			fmt.Fprintln(os.Stderr, "Usage: bomclaw artifact get <artifact-id> [--out FILE]")
			os.Exit(1)
		}

		db := openCoord(*dbPath)
		defer db.Close()
		a, err := db.GetArtifact(fs.Arg(0))
		if err != nil {
			fmt.Fprintf(os.Stderr, "artifact get: %v\n", err)
			os.Exit(1)
		}
		if a.Kind == coord.ArtifactLink {
			fmt.Println(a.URL)
			return
		}
		content, err := db.ReadArtifact(a.ID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "artifact get: %v\n", err)
			os.Exit(1)
		}
		if *out == "" {
			os.Stdout.Write(content)
			return
		}
		if err := os.WriteFile(*out, content, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "artifact get: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("%s → %s (%d bytes)\n", a.ID, *out, len(content))

	case "link":
		fs := flag.NewFlagSet("artifact link", flag.ExitOnError)
		dbPath := dbFlag(fs)
		id := fs.String("id", "", "Artifact id (required)")
		taskID := fs.String("task", "", "Task to attach it to (required)")
		role := fs.String("role", coord.RoleInput, "input | output")
		fs.Parse(rest)
		if *id == "" || *taskID == "" {
			fmt.Fprintln(os.Stderr, "Usage: bomclaw artifact link --id <artifact> --task <task> [--role input]")
			os.Exit(1)
		}

		db := openCoord(*dbPath)
		defer db.Close()
		if err := db.LinkArtifact(*id, *taskID, *role); err != nil {
			fmt.Fprintf(os.Stderr, "artifact link: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("%s is now an %s of %s\n", *id, *role, *taskID)

	default:
		artifactUsage()
		os.Exit(1)
	}
}

// readContent takes the file, or stdin when none is named — so an agent can
// pipe a diff straight in without a temp file it then has to clean up.
func readContent(file string) ([]byte, error) {
	if file != "" {
		return os.ReadFile(file)
	}
	stat, err := os.Stdin.Stat()
	if err == nil && stat.Mode()&os.ModeCharDevice != 0 {
		return nil, fmt.Errorf("nothing to store: pass --file, or pipe the content in")
	}
	return io.ReadAll(os.Stdin)
}

func printArtifacts(arts []coord.Artifact) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tROLE\tKIND\tSIZE\tTASK\tTITLE")
	for _, a := range arts {
		size := coord.HumanBytes(a.Bytes)
		if a.Kind == coord.ArtifactLink {
			size = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			a.ID, a.Role, a.Kind, size, shortID(a.TaskID), truncate(a.Title, 48))
	}
	w.Flush()
}

func artifactUsage() {
	fmt.Fprintln(os.Stderr, strings.TrimSpace(`
Usage: bomclaw artifact <command>

  put   --task <id> --title <what> [--kind document|patch|file|link|result]
        [--file PATH | stdin] [--url URL] [--name FILE]
  list  (--task <id> [--tree] | --context <id>)
  get   <artifact-id> [--out FILE]
  link  --id <artifact> --task <id> [--role input|output]

An artifact is how work crosses a task boundary. Put what you produced against
your task; a parent gathering children sees the index and reads only what it
needs. Hand one down to a child with 'task sub --input <artifact-id>'.`))
}
