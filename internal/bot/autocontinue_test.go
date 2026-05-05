package bot

import (
	"strings"
	"testing"
)

func TestParseTodoWriteInput(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		want     int
		firstStt string
	}{
		{
			name:     "valid two items",
			input:    `{"todos":[{"content":"a","status":"pending","activeForm":"doing a"},{"content":"b","status":"completed","activeForm":"doing b"}]}`,
			want:     2,
			firstStt: "pending",
		},
		{
			name:  "empty todos array",
			input: `{"todos":[]}`,
			want:  0,
		},
		{
			name:  "missing todos field",
			input: `{}`,
			want:  0,
		},
		{
			name:  "invalid json",
			input: `not json`,
			want:  0,
		},
		{
			name:  "wrong type for todos",
			input: `{"todos":"oops"}`,
			want:  0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseTodoWriteInput(tc.input)
			if len(got) != tc.want {
				t.Fatalf("got %d todos, want %d", len(got), tc.want)
			}
			if tc.want > 0 && got[0].Status != tc.firstStt {
				t.Errorf("first status = %q, want %q", got[0].Status, tc.firstStt)
			}
		})
	}
}

func TestPendingTodos(t *testing.T) {
	todos := []todoItem{
		{Content: "a", Status: "completed"},
		{Content: "b", Status: "in_progress"},
		{Content: "c", Status: "pending"},
		{Content: "d", Status: "completed"},
		{Content: "e", Status: "pending"},
	}
	got := pendingTodos(todos)
	want := []string{"b", "c", "e"}
	if len(got) != len(want) {
		t.Fatalf("got %d items, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestPendingTodos_AllDone(t *testing.T) {
	todos := []todoItem{
		{Content: "a", Status: "completed"},
		{Content: "b", Status: "completed"},
	}
	if got := pendingTodos(todos); len(got) != 0 {
		t.Errorf("expected no pending, got %v", got)
	}
}

func TestShouldAutoContinue(t *testing.T) {
	cases := []struct {
		name    string
		text    string
		pending []string
		want    bool
	}{
		{
			name:    "no pending todos",
			text:    "Tôi đã hoàn thành các bước.",
			pending: nil,
			want:    false,
		},
		{
			name:    "pending plus statement",
			text:    "Đã verify file. Tiếp tục xử lý.",
			pending: []string{"task a"},
			want:    true,
		},
		{
			name:    "pending plus trailing question mark",
			text:    "Bạn có muốn tôi tiếp tục không?",
			pending: []string{"task a"},
			want:    false,
		},
		{
			name:    "pending plus fullwidth question mark",
			text:    "Bạn muốn tôi làm gì？",
			pending: []string{"task a"},
			want:    false,
		},
		{
			name:    "english shall i marker",
			text:    "I've identified two options. Shall I proceed with option A or option B?",
			pending: []string{"task a"},
			want:    false,
		},
		{
			name:    "let me know variant",
			text:    "There are two paths forward. Let me know which you prefer.",
			pending: []string{"task a"},
			want:    false,
		},
		{
			name:    "vietnamese asking marker without question mark",
			text:    "Bạn cần tôi viết test cho cả hai trường hợp",
			pending: []string{"task a"},
			want:    false,
		},
		{
			name:    "empty text but pending",
			text:    "",
			pending: []string{"task a"},
			want:    true,
		},
		{
			name:    "asking phrase outside tail window does not block",
			text:    "Earlier I asked: shall I refactor this? You said yes. " + strings.Repeat("Now I'm running tests. ", 30) + "All green.",
			pending: []string{"task a"},
			want:    true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldAutoContinue(tc.text, tc.pending)
			if got != tc.want {
				t.Errorf("shouldAutoContinue(%q, %v) = %v, want %v", tc.text, tc.pending, got, tc.want)
			}
		})
	}
}
