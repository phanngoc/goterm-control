package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testManager(t *testing.T, cfg Config) *Manager {
	t.Helper()
	if cfg.Dir == "" {
		cfg.Dir = t.TempDir()
	}
	cfg.Enabled = true
	return NewManager(cfg)
}

func TestBootstrapIdempotent(t *testing.T) {
	m := testManager(t, Config{})
	if err := m.Bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if _, err := os.Stat(m.MemoryFilePath()); err != nil {
		t.Fatalf("MEMORY.md not created: %v", err)
	}
	if _, err := os.Stat(m.NotesDir()); err != nil {
		t.Fatalf("memory/ not created: %v", err)
	}

	// Second bootstrap must not overwrite user edits.
	custom := []byte("# MEMORY.md\n- user fact\n")
	if err := os.WriteFile(m.MemoryFilePath(), custom, 0644); err != nil {
		t.Fatal(err)
	}
	if err := m.Bootstrap(); err != nil {
		t.Fatalf("second bootstrap: %v", err)
	}
	data, _ := os.ReadFile(m.MemoryFilePath())
	if string(data) != string(custom) {
		t.Errorf("bootstrap overwrote MEMORY.md: %q", data)
	}
}

func TestBuildContextIncludesFiles(t *testing.T) {
	m := testManager(t, Config{MaxFileChars: 20000, MaxTotalChars: 60000})
	if err := m.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 2, 10, 0, 0, 0, time.Local)

	os.WriteFile(m.MemoryFilePath(), []byte("- user is ngoc"), 0644)
	os.WriteFile(m.TodayNotePath(now), []byte("- today note"), 0644)
	os.WriteFile(m.TodayNotePath(now.AddDate(0, 0, -1)), []byte("- yesterday note"), 0644)

	got := m.BuildContext(now)
	for _, want := range []string{
		"## Persistent Memory",
		"memory/2026-07-02.md",
		"- user is ngoc",
		"- today note",
		"### Yesterday (memory/2026-07-01.md)",
		"- yesterday note",
		"grep -ri",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("BuildContext missing %q", want)
		}
	}
}

func TestBuildContextEmptyFiles(t *testing.T) {
	m := testManager(t, Config{})
	now := time.Date(2026, 7, 2, 10, 0, 0, 0, time.Local)
	got := m.BuildContext(now)
	for _, want := range []string{"(empty)", "(no notes yet)", "(no notes)"} {
		if !strings.Contains(got, want) {
			t.Errorf("BuildContext missing placeholder %q", want)
		}
	}
}

func TestBuildContextDisabled(t *testing.T) {
	m := NewManager(Config{Enabled: false, Dir: t.TempDir()})
	if got := m.BuildContext(time.Now()); got != "" {
		t.Errorf("disabled manager returned context: %q", got)
	}
	var nilManager *Manager
	if nilManager.Enabled() {
		t.Error("nil manager reported enabled")
	}
}

func TestBuildContextPerFileCap(t *testing.T) {
	m := testManager(t, Config{MaxFileChars: 100, MaxTotalChars: 60000})
	now := time.Now()
	os.WriteFile(m.MemoryFilePath(), []byte(strings.Repeat("x", 500)), 0644)

	got := m.BuildContext(now)
	if !strings.Contains(got, "[truncated — read MEMORY.md for the rest]") {
		t.Error("missing per-file truncation marker")
	}
	if strings.Contains(got, strings.Repeat("x", 200)) {
		t.Error("file content not capped")
	}
}

func TestBuildContextTotalCap(t *testing.T) {
	m := testManager(t, Config{MaxFileChars: 20000, MaxTotalChars: 500})
	now := time.Now()
	os.WriteFile(m.MemoryFilePath(), []byte(strings.Repeat("y", 2000)), 0644)

	got := m.BuildContext(now)
	if !strings.Contains(got, "[memory truncated") {
		t.Error("missing total truncation marker")
	}
	if len([]rune(got)) > 600 {
		t.Errorf("block not capped: %d chars", len([]rune(got)))
	}
}

func TestAppendNote(t *testing.T) {
	m := testManager(t, Config{})
	now := time.Date(2026, 7, 2, 15, 4, 0, 0, time.Local)

	if err := m.AppendNote(now, "first fact"); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := m.AppendNote(now, "second fact"); err != nil {
		t.Fatalf("append: %v", err)
	}

	data, err := os.ReadFile(m.TodayNotePath(now))
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.HasPrefix(got, "# 2026-07-02\n") {
		t.Errorf("missing date header: %q", got)
	}
	if !strings.Contains(got, "- 15:04 first fact\n") || !strings.Contains(got, "- 15:04 second fact\n") {
		t.Errorf("missing bullets: %q", got)
	}
	if strings.Count(got, "# 2026-07-02") != 1 {
		t.Errorf("header duplicated: %q", got)
	}
}

func TestFlushPrompt(t *testing.T) {
	m := testManager(t, Config{})
	now := time.Date(2026, 7, 2, 10, 0, 0, 0, time.Local)

	got := m.FlushPrompt(now)
	if !strings.Contains(got, "memory/2026-07-02.md") {
		t.Errorf("flush prompt missing today path: %q", got)
	}
	if !strings.Contains(got, NoReplySentinel) {
		t.Error("flush prompt missing sentinel")
	}

	custom := testManager(t, Config{FlushPrompt: "save to {today} please"})
	if got := custom.FlushPrompt(now); got != "save to memory/2026-07-02.md please" {
		t.Errorf("custom prompt: %q", got)
	}
}

func TestIsNoReply(t *testing.T) {
	m := testManager(t, Config{})
	if !m.IsNoReply("  NO_REPLY \n") {
		t.Error("trimmed sentinel not detected")
	}
	if m.IsNoReply("NO_REPLY but also saved 3 notes") {
		t.Error("false positive on longer reply")
	}
}

func TestStats(t *testing.T) {
	m := testManager(t, Config{})
	if err := m.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := m.AppendNote(now, "a fact"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(m.NotesDir(), "2026-01-01.md"), []byte("- old"), 0644)

	st, err := m.Stats(now)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if st.MemoryMDBytes == 0 {
		t.Error("MEMORY.md size not reported")
	}
	if st.DailyNoteCount != 2 {
		t.Errorf("DailyNoteCount = %d, want 2", st.DailyNoteCount)
	}
	if st.TodayBytes == 0 {
		t.Error("today size not reported")
	}
}

func TestShouldRotate(t *testing.T) {
	loc := time.Local
	base := time.Date(2026, 7, 2, 10, 0, 0, 0, loc) // 10:00 today

	cases := []struct {
		name         string
		lastActivity time.Time
		now          time.Time
		dailyAt      string
		idle         time.Duration
		want         bool
		wantReason   string
	}{
		{"active this morning after boundary", time.Date(2026, 7, 2, 8, 0, 0, 0, loc), base, "04:00", 0, false, ""},
		{"last active yesterday evening", time.Date(2026, 7, 1, 22, 0, 0, 0, loc), base, "04:00", 0, true, "daily"},
		{"last active before 4am today", time.Date(2026, 7, 2, 3, 0, 0, 0, loc), base, "04:00", 0, true, "daily"},
		{"now before boundary, active yesterday after prev boundary", time.Date(2026, 7, 1, 23, 0, 0, 0, loc), time.Date(2026, 7, 2, 2, 0, 0, 0, loc), "04:00", 0, false, ""},
		{"daily disabled via off", time.Date(2026, 6, 20, 10, 0, 0, 0, loc), base, "off", 0, false, ""},
		{"daily disabled via empty", time.Date(2026, 6, 20, 10, 0, 0, 0, loc), base, "", 0, false, ""},
		{"idle exceeded", base.Add(-2 * time.Hour), base, "off", time.Hour, true, "idle"},
		{"idle not exceeded", base.Add(-30 * time.Minute), base, "off", time.Hour, false, ""},
		{"idle wins over daily", time.Date(2026, 7, 1, 6, 0, 0, 0, loc), base, "04:00", time.Hour, true, "idle"},
		{"zero last activity", time.Time{}, base, "04:00", time.Hour, false, ""},
		{"invalid dailyAt ignored", time.Date(2026, 6, 20, 10, 0, 0, 0, loc), base, "4am", 0, false, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := ShouldRotate(tc.lastActivity, tc.now, tc.dailyAt, tc.idle)
			if got != tc.want || reason != tc.wantReason {
				t.Errorf("ShouldRotate() = (%v, %q), want (%v, %q)", got, reason, tc.want, tc.wantReason)
			}
		})
	}
}
