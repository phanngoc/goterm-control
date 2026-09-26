package taskrunner

import (
	"os"
	"path/filepath"
	"testing"
)

// Keep the suite's feet inside its own sandbox.
//
// A coord.DB opened on a temp file still resolves its artifact and run roots
// from the environment, and their defaults are real directories under the
// user's home. A run of this package therefore created a folder in
// ~/goterm-shared/runs for every task it invented — 25 of them in one pass,
// on the machine of whoever ran the tests.
//
// Fixed here rather than in each helper because forgetting it is silent: the
// tests pass either way, and the only evidence is litter in somebody's home
// directory. ~/goterm-shared/artifacts had collected 66 such folders before
// anybody noticed.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "taskrunner-roots-")
	if err != nil {
		panic(err)
	}
	os.Setenv("BOMCLAW_RUNS_DIR", filepath.Join(dir, "runs"))
	os.Setenv("BOMCLAW_ARTIFACTS_DIR", filepath.Join(dir, "artifacts"))
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
