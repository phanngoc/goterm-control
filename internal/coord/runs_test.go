package coord

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// buildTrace writes a small turn → llm → 2 tools tree and returns its trace id.
func buildTrace(t *testing.T, db *DB, agent string, base time.Time) string {
	t.Helper()

	rootID := uuid.NewString()
	rootOrder := DottedOrderSegment(base, rootID)
	root := &Run{
		ID: rootID, TraceID: rootID, DottedOrder: rootOrder,
		AgentID: agent, Name: "turn", RunType: RunTypeChain, StartedAt: base,
	}
	if err := db.InsertRun(root); err != nil {
		t.Fatalf("insert root: %v", err)
	}

	llmID := uuid.NewString()
	llmStart := base.Add(10 * time.Millisecond)
	llmOrder := rootOrder + "." + DottedOrderSegment(llmStart, llmID)
	if err := db.InsertRun(&Run{
		ID: llmID, TraceID: rootID, ParentRunID: rootID, DottedOrder: llmOrder,
		AgentID: agent, Name: "codex", RunType: RunTypeLLM, StartedAt: llmStart,
	}); err != nil {
		t.Fatalf("insert llm: %v", err)
	}

	for i, name := range []string{"Bash", "Edit"} {
		toolID := uuid.NewString()
		toolStart := llmStart.Add(time.Duration(i+1) * 20 * time.Millisecond)
		if err := db.InsertRun(&Run{
			ID: toolID, TraceID: rootID, ParentRunID: llmID,
			DottedOrder: llmOrder + "." + DottedOrderSegment(toolStart, toolID),
			AgentID:     agent, Name: name, RunType: RunTypeTool, StartedAt: toolStart,
		}); err != nil {
			t.Fatalf("insert tool: %v", err)
		}
		db.EndRun(toolID, toolStart.Add(5*time.Millisecond), "ok", "", 0, 0)
	}

	db.EndRun(llmID, llmStart.Add(80*time.Millisecond), "reply", "", 1200, 50)
	db.EndRun(rootID, base.Add(100*time.Millisecond), "reply", "", 0, 0)
	return rootID
}

func TestTraceTreeComesBackNested(t *testing.T) {
	db := testDB(t)
	traceID := buildTrace(t, db, "a1", time.Now())

	runs, err := db.GetTrace(traceID)
	if err != nil {
		t.Fatalf("get trace: %v", err)
	}
	if len(runs) != 4 {
		t.Fatalf("got %d runs, want 4 (turn, llm, 2 tools)", len(runs))
	}

	// dotted_order alone must produce parent-before-child, in start order.
	wantNames := []string{"turn", "codex", "Bash", "Edit"}
	wantDepth := []int{0, 1, 2, 2}
	for i, r := range runs {
		if r.Name != wantNames[i] {
			t.Errorf("run %d = %q, want %q", i, r.Name, wantNames[i])
		}
		if r.Depth != wantDepth[i] {
			t.Errorf("run %q depth = %d, want %d", r.Name, r.Depth, wantDepth[i])
		}
	}
	if !strings.HasPrefix(runs[2].DottedOrder, runs[1].DottedOrder+".") {
		t.Error("a tool's dotted_order must extend its llm parent's")
	}
}

func TestEndRunComputesDuration(t *testing.T) {
	db := testDB(t)
	traceID := buildTrace(t, db, "a1", time.Now())

	runs, _ := db.GetTrace(traceID)
	root := runs[0]
	if root.Status != StatusSuccess {
		t.Errorf("status = %q, want success", root.Status)
	}
	if root.DurationMS < 90 || root.DurationMS > 110 {
		t.Errorf("duration = %dms, want ~100ms", root.DurationMS)
	}
}

func TestFailedRunIsRecordedAsError(t *testing.T) {
	db := testDB(t)
	id := uuid.NewString()
	start := time.Now()
	db.InsertRun(&Run{ID: id, TraceID: id, DottedOrder: DottedOrderSegment(start, id),
		AgentID: "a1", Name: "turn", RunType: RunTypeChain, StartedAt: start})

	if err := db.EndRun(id, start.Add(time.Second), "", "codex error: 400", 0, 0); err != nil {
		t.Fatalf("end run: %v", err)
	}
	runs, _ := db.GetTrace(id)
	if runs[0].Status != StatusError {
		t.Errorf("status = %q, want error", runs[0].Status)
	}
	if runs[0].Error != "codex error: 400" {
		t.Errorf("error = %q", runs[0].Error)
	}
}

func TestListTracesRollsUpTheTree(t *testing.T) {
	db := testDB(t)
	base := time.Now()
	buildTrace(t, db, "a1", base.Add(-time.Minute))
	buildTrace(t, db, "a2", base)

	all, err := db.ListTraces(TraceFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("got %d traces, want 2 roots (children must not be listed)", len(all))
	}
	if all[0].AgentID != "a2" {
		t.Errorf("newest trace first: got %q", all[0].AgentID)
	}
	if all[0].SpanCount != 4 || all[0].ToolCount != 2 {
		t.Errorf("rollup: spans=%d tools=%d, want 4 and 2", all[0].SpanCount, all[0].ToolCount)
	}
	if all[0].TotalTokens != 1250 {
		t.Errorf("total tokens = %d, want 1250", all[0].TotalTokens)
	}

	only, _ := db.ListTraces(TraceFilter{AgentID: "a1"})
	if len(only) != 1 || only[0].AgentID != "a1" {
		t.Errorf("agent filter returned %d rows", len(only))
	}
}

func TestPurgeRunsBefore(t *testing.T) {
	db := testDB(t)
	buildTrace(t, db, "a1", time.Now().Add(-48*time.Hour))
	buildTrace(t, db, "a1", time.Now())

	n, err := db.PurgeRunsBefore(time.Now().Add(-24 * time.Hour))
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if n != 4 {
		t.Errorf("purged %d rows, want 4", n)
	}
	left, _ := db.ListTraces(TraceFilter{})
	if len(left) != 1 {
		t.Errorf("%d traces left, want 1", len(left))
	}
}

// --- the multi-process question the design gates everything else on ---------

// TestTwoProcessesShareTheDatabase re-executes this test binary as a second OS
// process writing the same file. Two goroutines would not prove anything: the
// risk is cross-process locking, where a connection without a real busy timeout
// returns SQLITE_BUSY instead of waiting.
func TestTwoProcessesShareTheDatabase(t *testing.T) {
	if os.Getenv("COORD_WRITER_DB") != "" {
		runWriterChild(t)
		return
	}

	path := t.TempDir() + "/shared.db"
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	const perWriter = 60
	child := exec.Command(os.Args[0], "-test.run=TestTwoProcessesShareTheDatabase", "-test.v")
	child.Env = append(os.Environ(),
		"COORD_WRITER_DB="+path,
		"COORD_WRITER_ID=child",
		fmt.Sprintf("COORD_WRITER_N=%d", perWriter),
	)
	out, err := child.StdoutPipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	child.Stderr = child.Stdout
	if err := child.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}

	// Parent writes at the same time as the child.
	parentErr := writeTasks(db, "parent", perWriter)

	buf := make([]byte, 4096)
	n, _ := out.Read(buf)
	childOut := string(buf[:n])
	childErr := child.Wait()

	if parentErr != nil {
		t.Fatalf("parent writes failed while the child was writing: %v", parentErr)
	}
	if childErr != nil {
		t.Fatalf("child process failed: %v\n%s", childErr, childOut)
	}

	var total int
	if err := db.Conn().QueryRow(`SELECT count(*) FROM tasks`).Scan(&total); err != nil {
		t.Fatalf("count: %v", err)
	}
	if want := perWriter * 2; total != want {
		t.Errorf("%d tasks written by two processes, want %d", total, want)
	}
}

func runWriterChild(t *testing.T) {
	db, err := Open(os.Getenv("COORD_WRITER_DB"))
	if err != nil {
		t.Fatalf("child open: %v", err)
	}
	defer db.Close()

	var n int
	fmt.Sscanf(os.Getenv("COORD_WRITER_N"), "%d", &n)
	if err := writeTasks(db, os.Getenv("COORD_WRITER_ID"), n); err != nil {
		t.Fatalf("child writes: %v", err)
	}
}

func writeTasks(db *DB, who string, n int) error {
	for i := 0; i < n; i++ {
		if _, err := db.CreateTask(NewTask{
			CreatedBy: who,
			Title:     fmt.Sprintf("%s-%d", who, i),
		}); err != nil {
			return fmt.Errorf("%s write %d: %w", who, i, err)
		}
	}
	return nil
}

// TestDottedOrderSegmentHasNoSeparator guards a subtle break: "." separates
// segments, so a timestamp layout containing one silently corrupts depth
// calculation and prefix matching for the whole tree.
func TestDottedOrderSegmentHasNoSeparator(t *testing.T) {
	seg := DottedOrderSegment(time.Date(2026, 9, 5, 10, 30, 45, 123456789, time.UTC), "abc")
	if strings.Contains(seg, ".") {
		t.Errorf("segment %q contains the path separator", seg)
	}
	if !strings.HasSuffix(seg, "Zabc") {
		t.Errorf("segment %q must end in Z<run id>", seg)
	}
}
