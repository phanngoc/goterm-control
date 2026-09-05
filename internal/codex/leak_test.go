package codex

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ngocp/goterm-control/internal/chat"
	"github.com/ngocp/goterm-control/internal/session"
)

// fakeCodex installs a stand-in `codex` on PATH that prints the given JSONL
// lines, then writes its own pid to pidFile and lingers. A leaked child stays
// alive after SendMessage returns; a reaped one does not.
func fakeCodex(t *testing.T, jsonl string, linger time.Duration) (pidFile string) {
	t.Helper()
	dir := t.TempDir()
	pidFile = filepath.Join(dir, "pid")

	script := fmt.Sprintf(`#!/bin/sh
cat > /dev/null &
printf '%%s' $$ > %q
%s
sleep %d
`, pidFile, jsonl, int(linger.Seconds()))

	path := filepath.Join(dir, "codex")
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return pidFile
}

func alive(t *testing.T, pidFile string) bool {
	t.Helper()
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("fake codex never wrote its pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("bad pid %q: %v", raw, err)
	}
	// Signal 0 reports whether the process still exists.
	return syscall.Kill(pid, 0) == nil
}

// TestTurnFailedReapsTheChild covers a real leak: the stream-error paths used
// to return before cmd.Wait(), leaving one zombie codex process and one blocked
// stderr goroutine behind per failed turn.
func TestTurnFailedReapsTheChild(t *testing.T) {
	lines := `echo '{"type":"thread.started","thread_id":"t1"}'
echo '{"type":"turn.failed","error":{"message":"model unavailable"}}'`
	pidFile := fakeCodex(t, lines, 30*time.Second)

	c := New("test")
	c.SetWorkspace(t.TempDir())
	err := c.SendMessage(context.Background(), session.New(1), "", "hi", "", chat.StreamCallbacks{})
	if err == nil {
		t.Fatal("turn.failed must surface as an error")
	}
	if !strings.Contains(err.Error(), "model unavailable") {
		t.Errorf("error = %v, want the model's message", err)
	}
	if alive(t, pidFile) {
		t.Error("the codex subprocess outlived the failed turn — it was never reaped")
	}
}

func TestStreamErrorReapsTheChild(t *testing.T) {
	lines := `echo '{"type":"thread.started","thread_id":"t1"}'
echo '{"type":"error","message":"stream broke"}'`
	pidFile := fakeCodex(t, lines, 30*time.Second)

	c := New("test")
	c.SetWorkspace(t.TempDir())
	if err := c.SendMessage(context.Background(), session.New(1), "", "hi", "", chat.StreamCallbacks{}); err == nil {
		t.Fatal("a stream error must surface")
	}
	if alive(t, pidFile) {
		t.Error("the codex subprocess outlived the stream error")
	}
}

// TestNonZeroExitKeepsTheStatus covers the second half: the stderr tail is the
// useful message, but dropping the exit status made the failure uninspectable.
func TestNonZeroExitKeepsTheStatus(t *testing.T) {
	lines := `echo 'Failed to authenticate: token expired' >&2
exit 7`
	fakeCodex(t, lines, 0)

	c := New("test")
	c.SetWorkspace(t.TempDir())
	err := c.SendMessage(context.Background(), session.New(1), "", "hi", "", chat.StreamCallbacks{})
	if err == nil {
		t.Fatal("a non-zero exit with no turn must be an error")
	}
	if !strings.Contains(err.Error(), "token expired") {
		t.Errorf("error = %v, want the stderr tail (otherwise the user sees only 'exit status 7')", err)
	}
	var exitErr *exec.ExitError
	if !errorsAs(err, &exitErr) {
		t.Fatalf("error = %v, want the exit status still wrapped", err)
	}
	if code := exitErr.ExitCode(); code != 7 {
		t.Errorf("exit code = %d, want 7", code)
	}
}

// errorsAs keeps the import list honest about what this file needs.
func errorsAs(err error, target any) bool {
	type unwrapper interface{ Unwrap() error }
	for err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			if p, ok := target.(**exec.ExitError); ok {
				*p = e
				return true
			}
		}
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
