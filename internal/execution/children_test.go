//go:build unix

package execution

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// alive reports whether a pid still exists. Signal 0 checks without sending.
func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func waitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !alive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pid %d survived", pid)
}

// TestKillSpawnedKillsATrackedChild is the plain case: the gateway goes away
// and the CLI it started goes with it.
func TestKillSpawnedKillsATrackedChild(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	Detach(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	untrack := Track(cmd)
	defer untrack()
	pid := cmd.Process.Pid
	go cmd.Wait()

	if n := KillSpawned(); n != 1 {
		t.Fatalf("expected to kill 1 process, killed %d", n)
	}
	waitGone(t, pid)
}

// TestKillSpawnedReachesGrandchildren is the half that Process.Kill misses: a
// CLI that spawned a helper leaves the helper holding whatever the CLI held.
func TestKillSpawnedReachesGrandchildren(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	cmd := exec.Command("sh", "-c", "sleep 30 & echo $! > "+pidFile+"; sleep 30")
	Detach(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer Track(cmd)()
	go cmd.Wait()

	var grandchild int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(pidFile); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && n > 0 {
				grandchild = n
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if grandchild == 0 {
		t.Fatal("grandchild never reported its pid")
	}

	KillSpawned()
	waitGone(t, cmd.Process.Pid)
	waitGone(t, grandchild)
}

// TestUntrackedProcessIsNotKilled: a finished turn's child must not be in the
// list, or a later shutdown would signal a pid the OS has since reused.
func TestUntrackedProcessIsNotKilled(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	Detach(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	untrack := Track(cmd)
	untrack()
	defer func() { _ = KillGroup(cmd.Process); cmd.Wait() }()

	if n := KillSpawned(); n != 0 {
		t.Fatalf("killed %d processes after untracking the only one", n)
	}
	if !alive(cmd.Process.Pid) {
		t.Fatal("an untracked process was killed anyway")
	}
}

// TestCancelKillsTheGroup: cancelling one turn must not need shutdown to clean
// up after it — and must reach the grandchildren too.
func TestCancelKillsTheGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "sleep", "30")
	Detach(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer Track(cmd)()
	pid := cmd.Process.Pid
	go cmd.Wait()

	cancel()
	waitGone(t, pid)
}
