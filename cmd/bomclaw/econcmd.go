package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ngocp/goterm-control/internal/coord"
)

// `bomclaw econ` is the agents' ledger: what each one burns, what it gets
// done, and how a third party grades it. `bomclaw econ judge` names that third
// party.

func runEcon(args []string) {
	if len(args) > 0 && args[0] == "judge" {
		runEconJudge(args[1:])
		return
	}
	fs := flag.NewFlagSet("econ", flag.ExitOnError)
	dbPath := dbFlag(fs)
	days := fs.Int("days", 7, "Window, in days back from now")
	asJSON := fs.Bool("json", false, "Print the report as JSON")
	fs.Parse(args)

	db := openCoord(*dbPath)
	defer db.Close()
	e, err := db.EconomicsSince(time.Now().AddDate(0, 0, -*days))
	if err != nil {
		fmt.Fprintf(os.Stderr, "econ: %v\n", err)
		os.Exit(1)
	}
	if *asJSON {
		b, _ := json.MarshalIndent(e, "", "  ")
		fmt.Println(string(b))
		return
	}
	printEconomics(e, *days)
}

func printEconomics(e *coord.Economics, days int) {
	judge := e.Judge
	if judge == "" {
		judge = "any peer (set one: bomclaw econ judge <agent>)"
	}
	fmt.Printf("Agent economics · last %d days · judge: %s\n\n", days, judge)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(w, "agent\tturns\ttokens\tcache hit\tcost $\ttask $\tdone\tfail\trej\tstuck\tcancel\topen\tcompletion\t$/done\twasted $\tlost tries\tverified\tpass\tscore\tjudged\t")
	for _, a := range e.Agents {
		hit := "-"
		if t := a.InputTokens + a.CacheRead + a.CacheWrite; t > 0 {
			hit = fmt.Sprintf("%.0f%%", 100*float64(a.CacheRead)/float64(t))
		}
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%.2f\t%.2f\t%d\t%d\t%d\t%d\t%d\t%d\t%s\t%s\t%.2f\t%d\t%d\t%s\t%s\t%d\t\n",
			a.AgentID, a.Turns, humanTokens(a.Tokens()), hit, a.CostUSD, a.TaskCostUSD,
			a.Completed, a.Failed, a.Rejected, a.Stuck, a.Canceled, a.Open,
			pct(a.CompletionRate()), money(a.CostPerCompleted()), a.WastedUSD, a.FailedAttempts,
			a.Verified, ratio(a.Passed, a.Verified), score(a.MeanScore, a.Scored), a.Judged)
	}
	w.Flush()

	if len(e.Projects) > 0 {
		fmt.Println("\nBy project")
		w = tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', tabwriter.AlignRight)
		fmt.Fprintln(w, "project\ttasks\tdone\tcost $\tscore\t")
		for _, p := range e.Projects {
			fmt.Fprintf(w, "%s\t%d\t%d\t%.2f\t%s\t\n", p.ChannelID, p.Tasks, p.Completed, p.CostUSD, score(p.MeanScore, 1))
		}
		w.Flush()
	}
	fmt.Println("\ncost $ is the CLI's own figure at API list price, whatever the plan pays.\n" +
		"completion = done / (done + fail + rejected + stuck); a cancel is the owner's call, not a miss.\n" +
		"$/done = what finished tasks cost in full (every attempt, any agent) / how many; wasted $ = cost of failed/rejected ones.\n" +
		"lost tries = attempts that died on a task another agent ended up carrying.\n" +
		"pass and score are the judge's reading of finished goals against their criteria.")
}

func runEconJudge(args []string) {
	fs := flag.NewFlagSet("econ judge", flag.ExitOnError)
	dbPath := dbFlag(fs)
	clear := fs.Bool("clear", false, "Go back to any peer")
	id, _ := parseLeading(fs, args)
	db := openCoord(*dbPath)
	defer db.Close()
	if id == "" && !*clear {
		if j := db.Judge(); j != "" {
			fmt.Println(j)
		} else {
			fmt.Println("(none: any peer that did not do the work)")
		}
		return
	}
	if !*clear {
		agents, err := db.ListAgents()
		if err != nil {
			fmt.Fprintf(os.Stderr, "econ judge: %v\n", err)
			os.Exit(1)
		}
		known := false
		var ids []string
		for _, a := range agents {
			ids = append(ids, a.ID)
			known = known || a.ID == id
		}
		if !known {
			fmt.Fprintf(os.Stderr, "econ judge: no agent %q (known: %s)\n", id, strings.Join(ids, ", "))
			os.Exit(1)
		}
	} else {
		id = ""
	}
	if err := db.SetJudge(id); err != nil {
		fmt.Fprintf(os.Stderr, "econ judge: %v\n", err)
		os.Exit(1)
	}
	if id == "" {
		fmt.Println("judge cleared: any peer that did not do the work grades it")
	} else {
		fmt.Printf("judge: %s grades every finished goal it did not do itself\n", id)
	}
}

func humanTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.0fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

func pct(f float64) string {
	if f < 0 {
		return "-"
	}
	return fmt.Sprintf("%.0f%%", 100*f)
}

func money(f float64) string {
	if f < 0 {
		return "-"
	}
	return fmt.Sprintf("%.2f", f)
}

func ratio(a, b int) string {
	if b == 0 {
		return "-"
	}
	return fmt.Sprintf("%d/%d", a, b)
}

func score(mean float64, n int) string {
	if n == 0 || mean == 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f", mean)
}
