package bot

import "testing"

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
