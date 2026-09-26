package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/ngocp/goterm-control/internal/config"
	"github.com/ngocp/goterm-control/internal/skills"
)

// `bomclaw skills` shows an agent what it can do, and shows a person what an
// agent was given.
//
// The list is the same one that goes into the prompt, read from the same place
// by the same code — so "why did it not use that skill" is answerable by
// looking, instead of by reasoning about what the gateway might have loaded.

func runSkills(args []string) {
	fs := flag.NewFlagSet("skills", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "Path to config file")
	workspace := fs.String("workspace", "", "Read this workspace instead of the config's")
	showPrompt := fs.Bool("prompt", false, "Print the block the agent actually receives")
	sub, rest := "list", args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, rest = args[0], args[1:]
	}
	fs.Parse(rest)

	if sub != "list" {
		fmt.Fprintln(os.Stderr, "Usage: bomclaw skills list [--workspace <dir>] [--prompt]")
		os.Exit(1)
	}

	ws := *workspace
	if ws == "" {
		cfg, err := config.Load(*configPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skills: %v\n"+
				"Pass --workspace <dir>, or run this from the agent's own directory.\n", err)
			os.Exit(1)
		}
		ws = cfg.Claude.Workspace
	}

	list, err := skills.Load(ws)
	if err != nil {
		fmt.Fprintf(os.Stderr, "skills: %v\n", err)
		os.Exit(1)
	}
	if *showPrompt {
		fmt.Print(skills.Index(list))
		return
	}
	if len(list) == 0 {
		fmt.Printf("no skills in %s\n", filepath.Join(ws, skills.Dir))
		fmt.Println("A skill is a folder with a SKILL.md: a name, a line saying when to use it,")
		fmt.Println("and the instructions below that.")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SKILL\tWHEN TO USE IT")
	for _, s := range list {
		fmt.Fprintf(w, "%s\t%s\n", s.Name, truncate(oneLine(s.Description), 80))
	}
	w.Flush()
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
