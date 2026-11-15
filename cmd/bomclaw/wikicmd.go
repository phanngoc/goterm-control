package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// bomclaw wiki — a project's documents, from the command line. The same pages
// the dashboard's Wiki panel shows and /wiki/<channel>/ hosts: rows in the
// shared database, not files.

func wikiUsage() {
	fmt.Fprintln(os.Stderr, `usage: bomclaw wiki <command> --channel ID [flags]

  list  --channel ID                                   pages, newest first
  get   --channel ID --path P                          print one page
  put   --channel ID --path P (--file F | --body B | -)  save a page (.md .html .txt .json .csv .svg .png .jpg)
  rm    --channel ID --path P                          delete a page
  mv    --channel ID --path P --to Q                   rename a page

The wiki belongs to the project and is stored in the shared database (not in
a folder): every agent and the dashboard see the same pages. Hosted at
/wiki/<channel>/<page> on the dashboard.`)
}

func runWiki(args []string) {
	if len(args) == 0 {
		wikiUsage()
		os.Exit(1)
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("wiki "+sub, flag.ExitOnError)
	dbPath := dbFlag(fs)
	channel := fs.String("channel", "", "Room id, e.g. ch_arcade (required)")
	page := fs.String("path", "", "Page path inside the wiki, e.g. design/gdd.md")
	file := fs.String("file", "", "Read the page body from this file")
	body := fs.String("body", "", "The page body")
	to := fs.String("to", "", "New path, for mv")
	fs.Parse(rest)
	if *channel == "" {
		wikiUsage()
		os.Exit(1)
	}
	db := openCoord(*dbPath)
	defer db.Close()

	fail := func(err error) {
		fmt.Fprintln(os.Stderr, "wiki:", err)
		os.Exit(1)
	}
	needPath := func() {
		if *page == "" {
			fail(fmt.Errorf("--path is required"))
		}
	}

	switch sub {
	case "list", "ls":
		pages, err := db.ListWiki(*channel)
		if err != nil {
			fail(err)
		}
		if len(pages) == 0 {
			fmt.Println("(chưa có trang nào)")
		}
		for _, p := range pages {
			fmt.Printf("%-40s %-5s %7d  %s  %s\n", p.Path, p.Format, p.Bytes, p.Mtime[:16], p.Title)
		}
	case "get", "cat":
		needPath()
		b, _, err := db.ReadWiki(*channel, *page)
		if err != nil {
			fail(err)
		}
		fmt.Print(b)
	case "put", "save":
		needPath()
		var content string
		switch {
		case *file != "":
			b, err := os.ReadFile(*file)
			if err != nil {
				fail(err)
			}
			content = string(b)
		case *body != "":
			content = *body
		case fs.NArg() == 1 && fs.Arg(0) == "-":
			b, err := io.ReadAll(os.Stdin)
			if err != nil {
				fail(err)
			}
			content = string(b)
		default:
			fail(fmt.Errorf("give the page with --file, --body or - (stdin)"))
		}
		by := os.Getenv("BOMCLAW_AGENT_ID")
		if by == "" {
			by = "cli"
		}
		if _, err := db.WriteWiki(*channel, *page, content, "", by); err != nil {
			fail(err)
		}
		fmt.Printf("saved %s · /wiki/%s/%s\n", *page, *channel, strings.TrimPrefix(filepath.ToSlash(*page), "/"))
	case "rm", "delete":
		needPath()
		if err := db.DeleteWiki(*channel, *page); err != nil {
			fail(err)
		}
		fmt.Println("deleted", *page)
	case "mv", "rename":
		needPath()
		if *to == "" {
			fail(fmt.Errorf("--to is required"))
		}
		if err := db.RenameWiki(*channel, *page, *to); err != nil {
			fail(err)
		}
		fmt.Println("renamed", *page, "→", *to)
	default:
		wikiUsage()
		os.Exit(1)
	}
}
