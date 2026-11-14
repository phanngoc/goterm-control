package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// bomclaw wiki — a room's documents, from the command line. The same folder
// the dashboard's Wiki panel shows and /wiki/<channel>/ hosts.

func wikiUsage() {
	fmt.Fprintln(os.Stderr, `usage: bomclaw wiki <command> --channel ID [flags]

  list  --channel ID                                   pages, newest first
  get   --channel ID --path P                          print one page
  put   --channel ID --path P (--file F | --body B | -)  save a page (.md .html .txt .json .csv .svg)
  rm    --channel ID --path P                          delete a page
  dir   --channel ID                                   print the wiki folder

A project's wiki is <project>/wiki/; a room without a folder keeps it in
~/goterm-shared/wiki/<channel>/. Hosted at /wiki/<channel>/<page> on the dashboard.`)
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
		if _, err := db.WriteWiki(*channel, *page, content, ""); err != nil {
			fail(err)
		}
		fmt.Printf("saved %s · /wiki/%s/%s\n", *page, *channel, strings.TrimPrefix(filepath.ToSlash(*page), "/"))
	case "rm", "delete":
		needPath()
		if err := db.DeleteWiki(*channel, *page); err != nil {
			fail(err)
		}
		fmt.Println("deleted", *page)
	case "dir", "path":
		d, err := db.WikiDir(*channel)
		if err != nil {
			fail(err)
		}
		fmt.Println(d)
	default:
		wikiUsage()
		os.Exit(1)
	}
}
