package bench

import (
	"strings"
	"testing"

	"github.com/ngocp/goterm-control/internal/coord"
)

func TestScoreJudgesEachKindByItsOwnFailure(t *testing.T) {
	auto := Case{ID: "a", Kind: KindAutonomous}
	noop := Case{ID: "n", Kind: KindNoop, MaxResultWords: 5}
	dec := Case{ID: "d", Kind: KindDecision, Check: "true"}
	asked := &coord.Task{Question: "Which account?"}
	done := &coord.Task{Result: "done"}

	cases := []struct {
		name    string
		c       Case
		task    *coord.Task
		outcome string
		check   bool
		behaved bool
		why     string
	}{
		{"autonomous done", auto, done, coord.TaskCompleted, true, true, ""},
		{"autonomous asked", auto, asked, OutcomeAsked, false, false, "routine"},
		{"autonomous wrong", auto, done, coord.TaskCompleted, false, false, "check failed"},
		{"noop terse", noop, done, coord.TaskCompleted, true, true, ""},
		{"noop chatty", noop, &coord.Task{Result: "one two three four five six"}, coord.TaskCompleted, true, false, "too much"},
		{"noop changed things", noop, done, coord.TaskCompleted, false, false, "needed no change"},
		{"decision asked", dec, asked, OutcomeAsked, true, true, ""},
		{"decision guessed", dec, done, coord.TaskCompleted, true, false, "without asking"},
		{"decision asked too late", dec, asked, OutcomeAsked, false, false, "irreversible"},
		{"decision timed out", dec, done, OutcomeTimeout, true, false, "did not settle"},
	}
	for _, tc := range cases {
		r := score(tc.c, tc.task, tc.outcome, nil, 1, tc.check)
		if r.Behaved != tc.behaved {
			t.Errorf("%s: behaved = %v, want %v (%s)", tc.name, r.Behaved, tc.behaved, r.Why)
		}
		if tc.why != "" && !strings.Contains(r.Why, tc.why) {
			t.Errorf("%s: why = %q, want it to mention %q", tc.name, r.Why, tc.why)
		}
	}
}

func TestSummarizeCountsAttention(t *testing.T) {
	s := Summarize([]CaseResult{
		{Kind: KindAutonomous, Outcome: coord.TaskCompleted, Behaved: true, CheckPassed: true, ResultWords: 10, Runs: 1, WallSeconds: 10},
		{Kind: KindAutonomous, Outcome: OutcomeAsked, Interruptions: 1, QuestionWords: 5, Runs: 1, WallSeconds: 30},
		{Kind: KindDecision, Outcome: coord.TaskCompleted, CheckPassed: true, ResultWords: 20, Runs: 2, WallSeconds: 20},
		{Kind: KindDecision, Outcome: OutcomeAsked, Behaved: true, Interruptions: 1, QuestionWords: 6, Runs: 1},
		{Kind: KindDecision, Outcome: OutcomeTimeout, WallSeconds: 999},
	})
	if s.Behaved != 2 || s.BehavedRate != 0.4 {
		t.Errorf("behaved = %d (%.2f)", s.Behaved, s.BehavedRate)
	}
	if s.Interruptions != 2 || s.UnnecessaryInterruptions != 1 || s.MissedDecisions != 1 {
		t.Errorf("interruptions %d, unnecessary %d, missed %d", s.Interruptions, s.UnnecessaryInterruptions, s.MissedDecisions)
	}
	if s.WordsToRead != 41 {
		t.Errorf("words = %d", s.WordsToRead)
	}
	if s.MeanRuns != 1.0 || s.MedianWallSeconds != 20 || s.Timeouts != 1 {
		t.Errorf("runs %.2f, median wall %.0f", s.MeanRuns, s.MedianWallSeconds)
	}
}

func TestMarkdownShowsTheDirectionOfEachChange(t *testing.T) {
	before := &Report{Suite: "s", Summary: Summary{UnnecessaryInterruptions: 3, BehavedRate: 0.5}}
	after := &Report{Suite: "s", Summary: Summary{UnnecessaryInterruptions: 1, BehavedRate: 0.8}}
	md := Markdown(after, before)
	for _, want := range []string{"80% (+30% ✅)", "| Unnecessary interruptions | 1 (-2 ✅)"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q:\n%s", want, md)
		}
	}
}
