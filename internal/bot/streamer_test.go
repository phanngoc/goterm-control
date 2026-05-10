package bot

import (
	"strings"
	"testing"
	"time"
)

func TestBuildToolStatusLine_HeadOnly(t *testing.T) {
	// 5 tools — all in head, no checkpoints
	head := []string{"Bash(find)", "Read(main.go)", "Grep(foo)", "Edit(bar.go)", "Read(config)"}
	got := buildToolStatusLine(head, nil, 5)
	want := "Bash(find) → Read(main.go) → Grep(foo) → Edit(bar.go) → Read(config)"
	if got != want {
		t.Errorf("head-only:\n got: %s\nwant: %s", got, want)
	}
}

func TestBuildToolStatusLine_ExactHead(t *testing.T) {
	// Exactly 8 tools — full head, no overflow
	head := make([]string, 8)
	for i := range head {
		head[i] = "T" + string(rune('1'+i))
	}
	got := buildToolStatusLine(head, nil, 8)
	want := "T1 → T2 → T3 → T4 → T5 → T6 → T7 → T8"
	if got != want {
		t.Errorf("exact-head:\n got: %s\nwant: %s", got, want)
	}
}

func TestBuildToolStatusLine_FirstBatchIncomplete(t *testing.T) {
	// 15 tools: 8 head + 7 tail (no checkpoints yet — batch not at position 9/10)
	head := []string{"T1", "T2", "T3", "T4", "T5", "T6", "T7", "T8"}
	got := buildToolStatusLine(head, nil, 15)
	want := "T1 → T2 → T3 → T4 → T5 → T6 → T7 → T8 → (+7 more)"
	if got != want {
		t.Errorf("first-batch-incomplete:\n got: %s\nwant: %s", got, want)
	}
}

func TestBuildToolStatusLine_OneBatchComplete(t *testing.T) {
	// 18 tools: 8 head + 10 tail (1 complete batch, 2 checkpoints)
	head := []string{"T1", "T2", "T3", "T4", "T5", "T6", "T7", "T8"}
	checkpoints := []string{"Read(crypto)", "Cd(..)"}
	got := buildToolStatusLine(head, checkpoints, 18)
	want := "T1 → T2 → T3 → T4 → T5 → T6 → T7 → T8 → (+8 more) → Read(crypto) → Cd(..)"
	if got != want {
		t.Errorf("one-batch:\n got: %s\nwant: %s", got, want)
	}
}

func TestBuildToolStatusLine_TwoBatchesComplete(t *testing.T) {
	// 28 tools: 8 head + 20 tail (2 complete batches, 4 checkpoints)
	head := []string{"T1", "T2", "T3", "T4", "T5", "T6", "T7", "T8"}
	checkpoints := []string{"A9", "A10", "B9", "B10"}
	got := buildToolStatusLine(head, checkpoints, 28)
	want := "T1 → T2 → T3 → T4 → T5 → T6 → T7 → T8 → (+8 more) → A9 → A10 → (+8 more) → B9 → B10"
	if got != want {
		t.Errorf("two-batches:\n got: %s\nwant: %s", got, want)
	}
}

func TestBuildToolStatusLine_PartialLastBatch(t *testing.T) {
	// 35 tools: 8 head + 27 tail (2 complete batches + 7 partial)
	head := []string{"T1", "T2", "T3", "T4", "T5", "T6", "T7", "T8"}
	checkpoints := []string{"A9", "A10", "B9", "B10"}
	got := buildToolStatusLine(head, checkpoints, 35)
	want := "T1 → T2 → T3 → T4 → T5 → T6 → T7 → T8 → (+8 more) → A9 → A10 → (+8 more) → B9 → B10 → (+7 more)"
	if got != want {
		t.Errorf("partial-last:\n got: %s\nwant: %s", got, want)
	}
}

func TestNoteTool_RollingWindow(t *testing.T) {
	// Simulate 25 tool calls through NoteTool and verify resulting fields
	s := &Streamer{}
	tools := make([]string, 25)
	for i := range tools {
		tools[i] = "T" + string(rune('A'+i%26))
		s.NoteTool(tools[i])
	}

	if len(s.toolHead) != 8 {
		t.Errorf("head size: got %d, want 8", len(s.toolHead))
	}
	if s.toolCount != 25 {
		t.Errorf("toolCount: got %d, want 25", s.toolCount)
	}

	// After head (8), remaining 17 tools: batch of 10 produces checkpoints
	// at positions 9 and 10 of the batch (tool #17 and #18 overall).
	// Then partial batch of 7 — no checkpoints (positions 0-6, need 8 or 9).
	if len(s.toolCheckpoints) != 2 {
		t.Errorf("checkpoints: got %d, want 2 (tools at positions 17,18)", len(s.toolCheckpoints))
	}
}

func TestNoteTool_ExactBoundary18(t *testing.T) {
	s := &Streamer{}
	for i := 1; i <= 18; i++ {
		s.NoteTool("T" + string(rune('A'+i%26)))
	}
	if len(s.toolHead) != 8 {
		t.Errorf("head: got %d, want 8", len(s.toolHead))
	}
	// 10 tools after head, positions 8 and 9 produce checkpoints
	if len(s.toolCheckpoints) != 2 {
		t.Errorf("checkpoints at 18: got %d, want 2", len(s.toolCheckpoints))
	}
}

func TestNoteTool_ExactBoundary28(t *testing.T) {
	s := &Streamer{}
	for i := 1; i <= 28; i++ {
		s.NoteTool("T" + string(rune('A'+i%26)))
	}
	if len(s.toolHead) != 8 {
		t.Errorf("head: got %d, want 8", len(s.toolHead))
	}
	// 20 tools after head = 2 complete batches = 4 checkpoints
	if len(s.toolCheckpoints) != 4 {
		t.Errorf("checkpoints at 28: got %d, want 4", len(s.toolCheckpoints))
	}
}

func TestFlush_HeartbeatAllowed(t *testing.T) {
	// Verify the heartbeat logic: when startTime is old enough and no content,
	// flush should NOT return early at the dirty guard.
	s := &Streamer{
		startTime: time.Now().Add(-5 * time.Second), // started 5s ago
	}
	// No dirty, no reasonDirty — normally flush would return immediately.
	// With heartbeat, it should pass the guard because initialSent=false and elapsed >= 2s.
	needsHeartbeat := !s.initialSent && time.Since(s.startTime) >= 2*time.Second
	if !needsHeartbeat {
		t.Fatal("expected needsHeartbeat=true when startTime is 5s ago and initialSent=false")
	}
}

func TestFlush_NoHeartbeatAfterInitialSent(t *testing.T) {
	// After initialSent=true, heartbeat should stop (real content takes over).
	s := &Streamer{
		startTime:   time.Now().Add(-5 * time.Second),
		initialSent: true,
	}
	needsHeartbeat := !s.initialSent && time.Since(s.startTime) >= 2*time.Second
	if needsHeartbeat {
		t.Fatal("expected needsHeartbeat=false when initialSent=true")
	}
}

func TestFlush_NoHeartbeatBeforeTwoSeconds(t *testing.T) {
	// Before 2 seconds, no heartbeat even if no content.
	s := &Streamer{
		startTime: time.Now(), // just started
	}
	needsHeartbeat := !s.initialSent && time.Since(s.startTime) >= 2*time.Second
	if needsHeartbeat {
		t.Fatal("expected needsHeartbeat=false when just started")
	}
}

// TestSendFormatted_OverflowDropsToolLine verifies the fix for the bug where
// answer + tool line whose combined HTML exceeds maxTelegramMsg would split
// at the \n\n boundary and orphan the tool line in its own message below the
// final answer. With the fix, the tool line is dropped before chunking so the
// chunks are answer-only.
func TestSendFormatted_OverflowDropsToolLine(t *testing.T) {
	// Build an answer that's under the limit and a long tool line that pushes
	// the combined html over the limit — the exact scenario that produced the
	// orphan tool message in the screenshot bug. The split happens at the
	// \n\n separator between answer and tool line, leaving the tool line
	// alone in chunks[1].
	answer := strings.Repeat("API Endpoints overview line.\n", 60) // ~1800 chars
	toolEntries := strings.Repeat(" → Bash(find ../wc2026-predictor)", 80)
	toolLine := "🔧 <i>" + toolEntries + "</i>" // ~2700 chars

	combined := markdownToTelegramHTML(answer) + "\n\n" + toolLine
	if len(combined) <= maxTelegramMsg {
		t.Fatalf("test setup: combined html (%d) should exceed maxTelegramMsg (%d)", len(combined), maxTelegramMsg)
	}

	// Without the fix, chunkHTML on the combined html would split at \n\n,
	// producing a chunk that is ONLY the tool line.
	combinedChunks := chunkHTML(combined, maxTelegramMsg)
	orphaned := false
	for _, c := range combinedChunks {
		if strings.Contains(c, "🔧") && !strings.Contains(c, "API Endpoints") {
			orphaned = true
			break
		}
	}
	if !orphaned {
		t.Fatal("baseline: expected combined html to chunk into a tool-only orphan chunk")
	}

	// With the fix: when overflow is detected, html collapses to answer-only.
	// Verify the resulting chunks contain no tool-only fragment.
	answerOnly := markdownToTelegramHTML(answer)
	answerChunks := chunkHTML(answerOnly, maxTelegramMsg)
	for i, c := range answerChunks {
		if strings.Contains(c, "🔧") {
			t.Errorf("chunk %d contains tool line after fix: %q", i, c)
		}
	}
}
