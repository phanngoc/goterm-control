// Package bench measures the agents against the one principle the task layer
// is built on: take as little human attention as possible, and give as much
// value as possible.
//
// A suite is a set of small, checkable tasks, each with an expected behaviour.
// The bench queues them on the real task queue — the same rows, the same
// runner, the same prompts a person's work goes through — waits for each to
// settle, runs its check, and writes down two things for every case: what it
// was worth (did the check pass, did it behave as it should) and what it cost a
// person (did it stop to ask, how much did they have to read).
//
// Three kinds of case, because "efficient" is three different failures:
//
//   - autonomous: routine work. It should finish without asking anybody. A
//     question here is an unnecessary interruption.
//   - noop: there is nothing to do (already done, does not reproduce). It
//     should say so in a sentence and change nothing. A long report or an
//     invented change here is noise.
//   - decision: the work turns on something only a person can decide, or is
//     irreversible, or speaks for them outside this machine. It should stop
//     and ask one question. Going ahead here is a guess made in their name.
package bench

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Creator is the identity bench tasks are created under. It is not an agent,
// so the reporter never delivers their outcomes and the stuck listener never
// pages anybody about them: the bench must not spend the attention it measures.
const Creator = "bench"

// Case kinds.
const (
	KindAutonomous = "autonomous"
	KindNoop       = "noop"
	KindDecision   = "decision"
)

// Suite is a named list of cases.
type Suite struct {
	Name  string `json:"name"`
	Cases []Case `json:"cases"`
}

// Case is one task with a way to check it.
//
// Body, Setup and Check may use {{dir}}: the case's own scratch directory,
// created fresh for every run. Setup and Check run with `sh -c` inside it, with
// $BENCH_DIR set to the same path. Check exiting 0 means the work is right; for
// a decision case it should verify that nothing irreversible happened.
type Case struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	Title          string `json:"title"`
	Body           string `json:"body,omitempty"`
	Acceptance     string `json:"acceptance,omitempty"`
	Setup          string `json:"setup,omitempty"`
	Check          string `json:"check,omitempty"`
	MaxResultWords int    `json:"max_result_words,omitempty"` // noop: longer than this is noise; default 60
	// Reference is a known-good solution, run only by the suite's own test:
	// it proves the check accepts right work, as the untouched setup proves it
	// rejects none. A benchmark whose checks were never run against an answer
	// measures its own bugs.
	Reference string `json:"reference,omitempty"`
}

// LoadSuite reads and validates a suite file.
func LoadSuite(path string) (*Suite, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Suite
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := s.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &s, nil
}

// Validate rejects a suite that could not produce a meaningful score.
func (s *Suite) Validate() error {
	if len(s.Cases) == 0 {
		return fmt.Errorf("suite has no cases")
	}
	seen := map[string]bool{}
	for i, c := range s.Cases {
		if strings.TrimSpace(c.ID) == "" {
			return fmt.Errorf("case %d: id is required", i)
		}
		if seen[c.ID] {
			return fmt.Errorf("case %s: duplicate id", c.ID)
		}
		seen[c.ID] = true
		switch c.Kind {
		case KindAutonomous, KindNoop, KindDecision:
		default:
			return fmt.Errorf("case %s: kind must be autonomous, noop or decision", c.ID)
		}
		if strings.TrimSpace(c.Title) == "" {
			return fmt.Errorf("case %s: title is required", c.ID)
		}
		if c.Kind != KindDecision && strings.TrimSpace(c.Check) == "" {
			// Without a check, "completed" is the agent's word for it, which is
			// exactly what a benchmark is not allowed to take.
			return fmt.Errorf("case %s: a %s case needs a check", c.ID, c.Kind)
		}
	}
	return nil
}

// Filter keeps the cases whose id is listed; an empty list keeps all.
func (s *Suite) Filter(ids []string) *Suite {
	if len(ids) == 0 {
		return s
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[strings.TrimSpace(id)] = true
	}
	out := &Suite{Name: s.Name}
	for _, c := range s.Cases {
		if want[c.ID] {
			out.Cases = append(out.Cases, c)
		}
	}
	return out
}

func (c Case) maxResultWords() int {
	if c.MaxResultWords > 0 {
		return c.MaxResultWords
	}
	return 60
}

func expand(s, dir string) string { return strings.ReplaceAll(s, "{{dir}}", dir) }
