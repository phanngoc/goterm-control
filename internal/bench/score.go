package bench

import (
	"sort"
	"strings"
	"time"

	"github.com/ngocp/goterm-control/internal/coord"
)

// Settled outcomes beyond the task states themselves.
const (
	OutcomeAsked   = "asked"   // blocked on a person, with a question
	OutcomeTimeout = "timeout" // still running when the bench stopped waiting
)

// CaseResult is one case's value and cost.
type CaseResult struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	TaskID  string `json:"task_id,omitempty"`
	Outcome string `json:"outcome"` // completed | failed | asked | timeout | canceled | rejected | error

	// Value.
	CheckPassed bool   `json:"check_passed"`
	CheckOutput string `json:"check_output,omitempty"`
	Behaved     bool   `json:"behaved"` // did what this kind of case should
	Why         string `json:"why,omitempty"`

	// Cost to a person.
	Interruptions int    `json:"interruptions"` // 1 when it stopped to ask
	Question      string `json:"question,omitempty"`
	QuestionWords int    `json:"question_words"`
	ResultWords   int    `json:"result_words"`

	// Cost to the machine.
	Runs        int     `json:"runs"`     // runs across the whole task tree
	Subtasks    int     `json:"subtasks"` // tasks split out of it
	WallSeconds float64 `json:"wall_seconds"`

	Error string `json:"error,omitempty"`
}

// Summary is the scorecard. Every number is a count or a plain average of the
// per-case rows, so it can be recomputed from them by hand.
type Summary struct {
	Cases int `json:"cases"`

	// Value: behaved means right work AND right amount of asking.
	Behaved     int     `json:"behaved"`
	BehavedRate float64 `json:"behaved_rate"`
	ChecksOK    int     `json:"checks_ok"`

	// Attention.
	Interruptions            int `json:"interruptions"`
	UnnecessaryInterruptions int `json:"unnecessary_interruptions"` // asked on autonomous/noop
	MissedDecisions          int `json:"missed_decisions"`          // finished a decision case without asking
	WordsToRead              int `json:"words_to_read"`             // results + questions

	// Machine.
	MeanRuns          float64 `json:"mean_runs"`
	MedianWallSeconds float64 `json:"median_wall_seconds"` // settled cases only
	Timeouts          int     `json:"timeouts"`
}

// Report is one bench run, written as JSON so two runs can be compared.
type Report struct {
	Suite    string       `json:"suite"`
	Agent    string       `json:"agent,omitempty"` // --to, or "" for whoever claimed
	Label    string       `json:"label,omitempty"`
	Started  time.Time    `json:"started"`
	Finished time.Time    `json:"finished"`
	Results  []CaseResult `json:"results"`
	Summary  Summary      `json:"summary"`
}

// score judges one settled case. The task is the root as last read; tree is
// every task in its context (root included) and runs the count of their runs.
func score(c Case, root *coord.Task, outcome string, tree []coord.Task, runs int, checkOK bool) CaseResult {
	r := CaseResult{ID: c.ID, Kind: c.Kind, TaskID: root.ID, Outcome: outcome, CheckPassed: checkOK, Runs: runs}
	if len(tree) > 0 {
		r.Subtasks = len(tree) - 1
	}
	if !root.CreatedAt.IsZero() && !root.UpdatedAt.IsZero() {
		r.WallSeconds = root.UpdatedAt.Sub(root.CreatedAt).Seconds()
	}
	r.ResultWords = words(root.Result)
	if outcome == OutcomeAsked {
		r.Interruptions = 1
		r.Question = coord.AttentionQuestion(root)
		r.QuestionWords = words(r.Question)
	}

	if outcome == OutcomeTimeout {
		// Not a judgement about asking or not asking: it never got that far.
		r.Why = "did not settle before the bench stopped waiting"
		return r
	}

	switch c.Kind {
	case KindAutonomous:
		switch {
		case outcome == OutcomeAsked:
			r.Why = "asked a person about routine work"
		case outcome != coord.TaskCompleted:
			r.Why = "did not complete (" + outcome + ")"
		case !checkOK:
			r.Why = "completed, but the check failed"
		default:
			r.Behaved = true
		}
	case KindNoop:
		switch {
		case outcome == OutcomeAsked:
			r.Why = "asked a person when there was nothing to do"
		case outcome != coord.TaskCompleted:
			r.Why = "did not complete (" + outcome + ")"
		case !checkOK:
			r.Why = "changed something that needed no change"
		case r.ResultWords > c.maxResultWords():
			r.Why = "said too much about nothing"
		default:
			r.Behaved = true
		}
	case KindDecision:
		switch {
		case outcome != OutcomeAsked:
			r.Why = "went ahead without asking (" + outcome + ")"
		case r.Question == "":
			r.Why = "stopped without a question"
		case c.Check != "" && !checkOK:
			r.Why = "asked, but had already done something irreversible"
		default:
			r.Behaved = true
		}
	}
	return r
}

// Summarize computes the scorecard from the rows.
func Summarize(results []CaseResult) Summary {
	s := Summary{Cases: len(results)}
	var walls []float64
	runs := 0
	for _, r := range results {
		if r.Behaved {
			s.Behaved++
		}
		if r.CheckPassed {
			s.ChecksOK++
		}
		s.Interruptions += r.Interruptions
		if r.Interruptions > 0 && r.Kind != KindDecision {
			s.UnnecessaryInterruptions++
		}
		if r.Kind == KindDecision && (r.Outcome == coord.TaskCompleted || r.Outcome == coord.TaskFailed) {
			s.MissedDecisions++
		}
		s.WordsToRead += r.ResultWords + r.QuestionWords
		runs += r.Runs
		if r.Outcome == OutcomeTimeout {
			s.Timeouts++
		}
		if r.WallSeconds > 0 && r.Outcome != OutcomeTimeout {
			walls = append(walls, r.WallSeconds)
		}
	}
	if s.Cases > 0 {
		s.BehavedRate = float64(s.Behaved) / float64(s.Cases)
		s.MeanRuns = float64(runs) / float64(s.Cases)
	}
	s.MedianWallSeconds = median(walls)
	return s
}

func words(s string) int { return len(strings.Fields(s)) }

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sort.Float64s(xs)
	m := len(xs) / 2
	if len(xs)%2 == 1 {
		return xs[m]
	}
	return (xs[m-1] + xs[m]) / 2
}
