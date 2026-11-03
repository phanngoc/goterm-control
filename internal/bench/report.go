package bench

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// LoadReport reads a report written by Save.
func LoadReport(path string) (*Report, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r Report
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &r, nil
}

// Save writes the report as indented JSON.
func (r *Report) Save(path string) error {
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

// Markdown renders the scorecard, and against a baseline when one is given:
// each headline number with its change, then one row per case.
func Markdown(r *Report, base *Report) string {
	var b strings.Builder
	title := r.Suite
	if r.Label != "" {
		title += " — " + r.Label
	}
	fmt.Fprintf(&b, "## Bench: %s\n\n", title)
	if r.Agent != "" {
		fmt.Fprintf(&b, "Agent: `%s` · ", r.Agent)
	}
	fmt.Fprintf(&b, "%s · %s\n\n", r.Started.Format("2006-01-02 15:04"),
		r.Finished.Sub(r.Started).Round(1e9))

	s := r.Summary
	var bs *Summary
	if base != nil {
		bs = &base.Summary
		fmt.Fprintf(&b, "Baseline: %s", base.Suite)
		if base.Label != "" {
			fmt.Fprintf(&b, " — %s", base.Label)
		}
		fmt.Fprintf(&b, " (%s)\n\n", base.Started.Format("2006-01-02 15:04"))
	}

	b.WriteString("| Metric | Value | Better |\n|---|---|---|\n")
	row := func(name string, v, bv float64, f string, higherBetter bool) {
		cell := fmt.Sprintf(f, v)
		if bs != nil {
			d := v - bv
			mark := ""
			if d != 0 {
				if (d > 0) == higherBetter {
					mark = " ✅"
				} else {
					mark = " ❌"
				}
			}
			cell += fmt.Sprintf(" (%+"+f[1:]+"%s)", d, mark)
		}
		dir := "lower"
		if higherBetter {
			dir = "higher"
		}
		fmt.Fprintf(&b, "| %s | %s | %s |\n", name, cell, dir)
	}
	pick := func(f func(Summary) float64) float64 {
		if bs == nil {
			return 0
		}
		return f(*bs)
	}
	row("Behaved (right work, right asking)", s.BehavedRate*100, pick(func(x Summary) float64 { return x.BehavedRate * 100 }), "%.0f%%", true)
	row("Checks passed", float64(s.ChecksOK), pick(func(x Summary) float64 { return float64(x.ChecksOK) }), "%.0f", true)
	row("Unnecessary interruptions", float64(s.UnnecessaryInterruptions), pick(func(x Summary) float64 { return float64(x.UnnecessaryInterruptions) }), "%.0f", false)
	row("Missed decisions (guessed for a person)", float64(s.MissedDecisions), pick(func(x Summary) float64 { return float64(x.MissedDecisions) }), "%.0f", false)
	row("Words a person reads", float64(s.WordsToRead), pick(func(x Summary) float64 { return float64(x.WordsToRead) }), "%.0f", false)
	row("Mean runs per case", s.MeanRuns, pick(func(x Summary) float64 { return x.MeanRuns }), "%.2f", false)
	row("Median wall time (s)", s.MedianWallSeconds, pick(func(x Summary) float64 { return x.MedianWallSeconds }), "%.0f", false)
	row("Timeouts", float64(s.Timeouts), pick(func(x Summary) float64 { return float64(x.Timeouts) }), "%.0f", false)

	b.WriteString("\n| Case | Kind | Outcome | Behaved | Runs | Words | Why |\n|---|---|---|---|---|---|---|\n")
	for _, c := range r.Results {
		ok := "✅"
		if !c.Behaved {
			ok = "❌"
		}
		why := c.Why
		if c.Question != "" {
			why = strings.TrimSpace(why + " · asked: " + clip(c.Question, 120))
		}
		if c.Error != "" {
			why = "error: " + clip(c.Error, 120)
		}
		why = strings.ReplaceAll(why, "|", "/")
		why = strings.ReplaceAll(why, "\n", " ")
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %d | %d | %s |\n",
			c.ID, c.Kind, c.Outcome, ok, c.Runs, c.ResultWords+c.QuestionWords, why)
	}
	return b.String()
}
