package gateway

import (
	"strings"
	"testing"
	"time"

	"github.com/ngocp/goterm-control/internal/chat"
	"github.com/ngocp/goterm-control/internal/coord"
)

// The progress line is the room's view of a running turn: it must show the
// newest thinking, the steps with the running one last, and the plan with
// what is done and what is left — the three things a person waiting wants.
func TestWorkingLineShowsThinkingStepsAndPlan(t *testing.T) {
	v := progressView{
		thinking: "old idea about the parser\n\nChecking whether the cache key includes the locale.",
		tools:    []string{"Read(a.go)", "Read(b.go)", "Bash(go test ./...)"},
		todos: []chat.Todo{
			{Content: "Read the code", Status: "completed"},
			{Content: "Fix the key", Status: "in_progress", ActiveForm: "Fixing the cache key"},
			{Content: "Add a test", Status: "pending"},
		},
		partial: "So far: the key",
		since:   time.Now().Add(-42 * time.Second),
	}
	got := workingLine(v)
	for _, want := range []string{
		coord.ProgressPrefix, "3 bước",
		"> _💭 Checking whether the cache key includes the locale._",
		"- ✓ `Read(b.go)`", "- ▸ `Bash(go test ./...)`",
		"**Kế hoạch** · 1/3 xong", "- ☑ ~~Read the code~~", "- ◐ **Fixing the cache key**", "- ☐ Add a test",
		"So far: the key",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("progress line lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "old idea") {
		t.Errorf("older thinking paragraphs should give way to the newest:\n%s", got)
	}
	if !strings.HasPrefix(workingLine(progressView{}), coord.ProgressPrefix) {
		t.Error("the empty line must still read as progress")
	}
}

func TestSinkReportsThinkingAndPlans(t *testing.T) {
	var last progressView
	calls := 0
	s := &replySink{preview: chat.DefaultPreview(), onProgress: func(p progressView) { last = p; calls++ }}
	s.NoteThinking(strings.Repeat("x", thinkingRedraw)) // a sentence's worth: redraw now
	if calls != 1 || !strings.Contains(last.thinking, "xxx") {
		t.Fatalf("enough new thinking did not redraw: %d %q", calls, last.thinking)
	}
	s.NoteThinking("y") // a trickle waits for the interval
	if calls != 1 {
		t.Errorf("a single delta redrew the line")
	}
	s.NoteThinking("")
	s.NoteTodos([]chat.Todo{{Content: "a", Status: "pending"}})
	if calls != 2 || len(last.todos) != 1 || !strings.HasSuffix(last.thinking, "y\n\n") {
		t.Errorf("plan did not redraw, or block end lost: %d %+v %q", calls, last.todos, last.thinking)
	}
}
