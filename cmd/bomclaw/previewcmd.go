package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ngocp/goterm-control/internal/coord"
	"github.com/ngocp/goterm-control/internal/gateway"
)

// `bomclaw preview` is how an agent sets up a project's Preview button and
// proves it works, without a browser: init writes bomclaw.json (guessed from
// the project's files, or from flags), check runs it exactly as the dashboard
// will and fetches the page through the base path.

func runPreview(args []string) {
	if len(args) == 0 {
		previewUsage()
		os.Exit(1)
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "show":
		fs := flag.NewFlagSet("preview show", flag.ExitOnError)
		dbPath := dbFlag(fs)
		id, _ := parseLeading(fs, rest)
		c := previewProject(*dbPath, id)
		spec, err := gateway.ReadPreviewSpec(c.Workspace)
		switch {
		case err != nil:
			fmt.Printf("%s: %v\n", gateway.PreviewManifest, err)
		case spec == nil:
			fmt.Printf("%s declares no preview (%s/%s)\n", c.ID, c.Workspace, gateway.PreviewManifest)
		default:
			b, _ := json.MarshalIndent(spec, "", "  ")
			fmt.Printf("%s/%s preview:\n%s\n", c.Workspace, gateway.PreviewManifest, b)
		}
		if guess, why := gateway.DetectPreview(c.Workspace); guess != nil {
			b, _ := json.Marshal(guess)
			fmt.Printf("detected: %s\n  %s\n", b, why)
		} else {
			fmt.Printf("detected: nothing (%s)\n", why)
		}
		fmt.Printf("served at: %s\n", gateway.PreviewBasePath(c.ID))

	case "init":
		fs := flag.NewFlagSet("preview init", flag.ExitOnError)
		dbPath := dbFlag(fs)
		command := fs.String("command", "", "Dev server command; must listen on $HOST:$PORT")
		setup := fs.String("setup", "", "Runs before the command on every start (npm install…)")
		cwd := fs.String("cwd", "", "Folder both run in, relative to the project")
		base := fs.String("base", "", "keep | strip")
		static := fs.String("static", "", "Serve this folder as files instead of running anything")
		force := fs.Bool("force", false, "Replace a preview that is already declared")
		check := fs.Bool("check", true, "Run `preview check` after writing")
		wait := fs.Duration("timeout", 3*time.Minute, "How long check waits for the server")
		id, _ := parseLeading(fs, rest)
		c := previewProject(*dbPath, id)

		if old, err := gateway.ReadPreviewSpec(c.Workspace); err == nil && old != nil && !*force {
			fmt.Fprintf(os.Stderr, "preview init: %s already declares a preview; edit it, or pass --force to replace it\n", c.ID)
			os.Exit(1)
		}
		var spec *gateway.PreviewSpec
		if *command != "" || *static != "" {
			spec = &gateway.PreviewSpec{Command: *command, Setup: *setup, Cwd: *cwd, Base: *base, Static: *static}
		} else {
			guess, why := gateway.DetectPreview(c.Workspace)
			if guess == nil {
				fmt.Fprintf(os.Stderr, "preview init: %s\n  pass --command \"…$HOST…$PORT…\" or --static <folder> (recipes: the preview skill)\n", why)
				os.Exit(1)
			}
			fmt.Printf("detected: %s\n", why)
			spec = guess
		}
		if err := gateway.WritePreviewSpec(c.Workspace, spec); err != nil {
			fmt.Fprintf(os.Stderr, "preview init: %v\n", err)
			os.Exit(1)
		}
		b, _ := json.Marshal(spec)
		fmt.Printf("wrote %s/%s: %s\n", c.Workspace, gateway.PreviewManifest, b)
		if *check {
			os.Exit(runPreviewCheck(c, spec, *wait))
		}

	case "check":
		fs := flag.NewFlagSet("preview check", flag.ExitOnError)
		dbPath := dbFlag(fs)
		wait := fs.Duration("timeout", 3*time.Minute, "How long to wait for the server to listen")
		id, _ := parseLeading(fs, rest)
		c := previewProject(*dbPath, id)
		spec, err := gateway.ReadPreviewSpec(c.Workspace)
		if err != nil {
			fmt.Printf("FAIL %v\n", err)
			os.Exit(1)
		}
		if spec == nil {
			fmt.Printf("FAIL %s declares no preview: bomclaw preview init %s\n", c.ID, c.ID)
			os.Exit(1)
		}
		os.Exit(runPreviewCheck(c, spec, *wait))

	default:
		previewUsage()
		os.Exit(1)
	}
}

func runPreviewCheck(c *coord.Channel, spec *gateway.PreviewSpec, wait time.Duration) int {
	fmt.Printf("checking %s (%s)…\n", c.ID, map[bool]string{true: "static", false: "server"}[spec.Command == ""])
	res := gateway.CheckPreview(context.Background(), c.Workspace, c.ID, spec, wait)
	for _, n := range res.Notes {
		fmt.Printf("  %s\n", n)
	}
	if res.URL != "" {
		if res.Status != 0 {
			fmt.Printf("  GET %s → %d\n", res.URL, res.Status)
		} else {
			fmt.Printf("  page: %s\n", res.URL)
		}
	}
	if res.OK {
		fmt.Printf("PASS the Preview button will show %s\n", gateway.PreviewBasePath(c.ID))
		return 0
	}
	for _, p := range res.Problems {
		fmt.Printf("FAIL %s\n", p)
	}
	if res.Logs != "" {
		fmt.Printf("--- server output (last %d bytes) ---\n%s\n", len(res.Logs), strings.TrimRight(res.Logs, "\n"))
	}
	return 1
}

// previewProject resolves a channel id, or a bare name, to a project.
func previewProject(dbPath, id string) *coord.Channel {
	if id == "" {
		id = os.Getenv("BOMCLAW_PROJECT")
	}
	if id == "" {
		previewUsage()
		os.Exit(1)
	}
	db := openCoord(dbPath)
	defer db.Close()
	c, err := db.GetChannel(id)
	if err != nil && !strings.HasPrefix(id, "ch_") {
		c, err = db.GetChannel("ch_" + id)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "preview: no channel %s (bomclaw ch list)\n", id)
		os.Exit(1)
	}
	if c.Workspace == "" {
		fmt.Fprintf(os.Stderr, "preview: %s is a room with no project folder, so there is nothing to preview\n", c.ID)
		os.Exit(1)
	}
	return c
}

func previewUsage() {
	fmt.Fprintln(os.Stderr, `Usage: bomclaw preview <command> <channel>

Make a project's Preview button work (the dashboard reads bomclaw.json).

  show  <channel>   What bomclaw.json declares, and what the files suggest
  init  <channel>   Write bomclaw.json: guessed from the files (Vite, Next,
                    wrangler, a folder with index.html), or from flags:
                      --command "npm run dev -- --host $HOST --port $PORT --base $BASE_PATH"
                      --setup "npm install"  --cwd web  --base keep|strip
                      --static board
                    then runs check (--check=false to skip, --force to replace)
  check <channel>   Run the preview exactly as the dashboard will ($PORT,
                    $HOST, $BASE_PATH), fetch the page through its base path,
                    flag assets that would 404, stop it. Exit 1 on FAIL.

<channel> is ch_<name> or <name>; defaults to $BOMCLAW_PROJECT.`)
}
