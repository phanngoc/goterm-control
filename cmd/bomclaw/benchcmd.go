package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/ngocp/goterm-control/internal/bench"
	"github.com/ngocp/goterm-control/internal/coord"
	"github.com/ngocp/goterm-control/internal/gateway"
)

func benchUsage() {
	fmt.Fprint(os.Stderr, `Usage: bomclaw bench <command>

  run     Queue a suite on the real task queue, wait, check and score it
  report  Print a saved report, optionally against a baseline

  bomclaw bench run [--suite bench/attention-suite.json] [--to <agent>] [--label "<what changed>"]
                    [--cases a,b] [--timeout 30m] [--out <file.json>] [--baseline <file.json>]
  bomclaw bench report <file.json> [--baseline <file.json>]

Each case is scored on value (did the check pass, did it ask exactly when it
should) and on what it cost a person (questions, words to read). Bench tasks
are created as "bench": nobody is paged and no result is reported.
`)
}

func runBench(args []string) {
	if len(args) == 0 {
		benchUsage()
		os.Exit(1)
	}
	switch args[0] {
	case "run":
		runBenchRun(args[1:])
	case "report":
		runBenchReport(args[1:])
	default:
		benchUsage()
		os.Exit(1)
	}
}

func runBenchRun(args []string) {
	fs := flag.NewFlagSet("bench run", flag.ExitOnError)
	dbPath := dbFlag(fs)
	suitePath := fs.String("suite", "bench/attention-suite.json", "Suite file")
	to := fs.String("to", "", "Agent to run every case (default: whichever claims it)")
	label := fs.String("label", "", "What this run measures, e.g. \"after prompt change\"")
	cases := fs.String("cases", "", "Comma-separated case ids (default: all)")
	timeout := fs.Duration("timeout", 30*time.Minute, "How long to wait for the whole suite")
	out := fs.String("out", "", "Where to write the JSON report (default: next to the database, under bench/)")
	baseline := fs.String("baseline", "", "A previous report to compare against")
	fs.Parse(args)

	s, err := bench.LoadSuite(*suitePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bench: %v\n", err)
		os.Exit(1)
	}
	if *cases != "" {
		s = s.Filter(strings.Split(*cases, ","))
		if len(s.Cases) == 0 {
			fmt.Fprintln(os.Stderr, "bench: --cases matched nothing")
			os.Exit(1)
		}
	}
	var base *bench.Report
	if *baseline != "" {
		if base, err = bench.LoadReport(*baseline); err != nil {
			fmt.Fprintf(os.Stderr, "bench: baseline: %v\n", err)
			os.Exit(1)
		}
	}

	db := openCoord(*dbPath)
	defer db.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	rep, err := bench.Run(ctx, db, s, bench.Options{
		To: *to, Label: *label, Timeout: *timeout,
		OnCreated: func(t *coord.Task) { gateway.NotifyTaskCreated(db, t) },
		Log: func(f string, a ...any) {
			fmt.Fprintf(os.Stderr, "%s  %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(f, a...))
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "bench: %v\n", err)
		os.Exit(1)
	}

	path := *out
	if path == "" {
		dir := filepath.Join(filepath.Dir(*dbPath), "bench")
		_ = os.MkdirAll(dir, 0o755)
		path = filepath.Join(dir, rep.Started.Format("20060102-150405")+".json")
	}
	if err := rep.Save(path); err != nil {
		fmt.Fprintf(os.Stderr, "bench: save: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(bench.Markdown(rep, base))
	fmt.Fprintf(os.Stderr, "report: %s\n", path)
}

func runBenchReport(args []string) {
	fs := flag.NewFlagSet("bench report", flag.ExitOnError)
	baseline := fs.String("baseline", "", "A previous report to compare against")
	path, _ := parseLeading(fs, args)
	if path == "" {
		benchUsage()
		os.Exit(1)
	}
	rep, err := bench.LoadReport(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bench: %v\n", err)
		os.Exit(1)
	}
	var base *bench.Report
	if *baseline != "" {
		if base, err = bench.LoadReport(*baseline); err != nil {
			fmt.Fprintf(os.Stderr, "bench: baseline: %v\n", err)
			os.Exit(1)
		}
	}
	fmt.Println(bench.Markdown(rep, base))
}
