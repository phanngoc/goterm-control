package bench

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

const suitePath = "../../bench/attention-suite.json"

// The shipped suite must load, and every check must mean something: it rejects
// the starting state of work still to be done, accepts the reference answer,
// and — for a case where the right move is to change nothing — accepts the
// untouched state and rejects one that was tampered with.
func TestTheShippedSuiteChecksAreSound(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Cases are POSIX shell plus python3, run with `sh -c`: the bench
		// targets the macOS/Linux agents, and Git Bash on CI is not that.
		t.Skip("bench cases are POSIX shell scripts")
	}
	s, err := LoadSuite(suitePath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, c := range s.Cases {
		c := c
		t.Run(c.ID, func(t *testing.T) {
			dir := t.TempDir()
			if c.Setup != "" {
				if out, err := shell(ctx, dir, expand(c.Setup, dir)); err != nil {
					t.Fatalf("setup: %v: %s", err, out)
				}
			}
			switch c.Kind {
			case KindAutonomous:
				if c.Reference == "" {
					t.Fatal("an autonomous case needs a reference answer")
				}
				if _, err := shell(ctx, dir, expand(c.Check, dir)); err == nil {
					t.Fatal("check passes before any work was done")
				}
				if out, err := shell(ctx, dir, expand(c.Reference, dir)); err != nil {
					t.Fatalf("reference: %v: %s", err, out)
				}
				if out, err := shell(ctx, dir, expand(c.Check, dir)); err != nil {
					t.Fatalf("check rejects the reference answer: %v: %s", err, out)
				}
			case KindNoop, KindDecision:
				if c.Check == "" {
					return
				}
				if out, err := shell(ctx, dir, expand(c.Check, dir)); err != nil {
					t.Fatalf("check rejects the untouched state: %v: %s", err, out)
				}
				tamper(t, dir)
				if _, err := shell(ctx, dir, expand(c.Check, dir)); err == nil {
					t.Fatal("check passes after the files were changed")
				}
			}
		})
	}
}

// tamper appends a line to every visible file — the generic "it changed
// something" that a noop or decision case must catch.
func tamper(t *testing.T, dir string) {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() || e.Name()[0] == '.' {
			continue
		}
		f, err := os.OpenFile(filepath.Join(dir, e.Name()), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.WriteString("tampered\n")
		f.Close()
	}
}

func TestValidateRejectsAnUncheckedCase(t *testing.T) {
	s := &Suite{Cases: []Case{{ID: "x", Kind: KindAutonomous, Title: "t"}}}
	if err := s.Validate(); err == nil {
		t.Fatal("an autonomous case without a check must be refused")
	}
	s = &Suite{Cases: []Case{{ID: "x", Kind: "vibes", Title: "t", Check: "true"}}}
	if err := s.Validate(); err == nil {
		t.Fatal("an unknown kind must be refused")
	}
}
