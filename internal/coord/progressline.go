package coord

import (
	"encoding/json"
	"strings"
	"time"
)

// A progress line carries two readings of the same moment. The markdown is for
// anything that reads text — an agent catching up on a thread, the CLI, an old
// dashboard. The data, in a trailing HTML comment, is for the dashboard, which
// draws it as a live status card the way Claude Code shows a turn: a spinner,
// a clock it ticks itself, the step running now, the thinking, the plan.
//
// Markdown alone could not do that. A clock written into the text is only as
// fresh as the last redraw, and "which step is running" had to be guessed
// from a ▸ in a bullet list.

// progressMarker opens the data comment. It never appears in prose, and an
// HTML comment is invisible to anything that renders markdown as HTML.
const progressMarker = "<!--bomclaw:progress "

// Progress is the state of one running turn or task run.
type Progress struct {
	Agent string    `json:"agent,omitempty"`
	Label string    `json:"label"`          // "đang làm", "đang chạy"
	Task  string    `json:"task,omitempty"` // the task being run, when it is one
	Since time.Time `json:"since"`
	// Steps are the most recent tool calls, oldest first; the last is the one
	// running now. StepCount is how many there were in all.
	Steps     []string       `json:"steps,omitempty"`
	StepCount int            `json:"step_count"`
	Thinking  string         `json:"thinking,omitempty"` // the newest paragraph
	Todos     []ProgressTodo `json:"todos,omitempty"`
	Partial   string         `json:"partial,omitempty"` // the answer so far, already cut to a preview
}

// ProgressTodo is one line of the model's plan.
type ProgressTodo struct {
	Content string `json:"content"`
	Status  string `json:"status"` // pending | in_progress | completed
	Active  string `json:"active,omitempty"`
}

// MaxProgressSteps is how many recent steps a progress line carries.
const MaxProgressSteps = 8

// WithProgress appends p to a progress line's markdown.
func WithProgress(markdown string, p Progress) string {
	if n := len(p.Steps); n > MaxProgressSteps {
		p.Steps = p.Steps[n-MaxProgressSteps:]
	}
	b, err := json.Marshal(p)
	if err != nil {
		return markdown
	}
	// "--" cannot occur inside an HTML comment; JSON only produces it inside
	// strings, where - is the same character.
	data := strings.ReplaceAll(string(b), "--", `--`)
	return markdown + "\n\n" + progressMarker + data + "-->"
}

// ParseProgress reads the data back out of a progress line.
func ParseProgress(body string) (Progress, bool) {
	i := strings.LastIndex(body, progressMarker)
	if i < 0 {
		return Progress{}, false
	}
	data := strings.TrimSuffix(strings.TrimSpace(body[i+len(progressMarker):]), "-->")
	var p Progress
	if err := json.Unmarshal([]byte(data), &p); err != nil {
		return Progress{}, false
	}
	return p, true
}

// ProgressText is a message body without its progress data — what a reader
// that is not the dashboard should see.
func ProgressText(body string) string {
	if i := strings.LastIndex(body, progressMarker); i >= 0 {
		return strings.TrimRight(body[:i], "\n ")
	}
	return body
}
