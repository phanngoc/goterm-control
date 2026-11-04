package bot

import (
	"encoding/json"
	"regexp"
	"strings"
)

// planTracker keeps the model's own plan as it changes during a turn, for the
// "what is left" part of a progress line and for auto-continue.
//
// The CLI has two shapes for the same idea. TodoWrite sends the whole list each
// time. The newer task tools (TaskCreate / TaskUpdate, what the CLI offers in
// print mode once CLAUDE_CODE_ENABLE_TODO_TOOLS is set) send one change at a
// time and number the items themselves — "Task #3 created" is in the result,
// not the call — so an item gets its id when its result comes back.
type planTracker struct {
	items []todoItem
	ids   []string // ids[i] belongs to items[i]; "" until its result arrives
}

var taskCreatedRe = regexp.MustCompile(`#(\d+)`)

// isPlanTool reports a tool that only edits the plan. Its calls are the plan,
// not steps of the work, so a progress line shows them as the plan instead.
func isPlanTool(name string) bool {
	switch name {
	case "TodoWrite", "TaskCreate", "TaskUpdate", "TaskList", "TaskGet":
		return true
	}
	return false
}

// call applies a tool call and reports whether the plan changed.
func (p *planTracker) call(name, input string) bool {
	switch name {
	case "TodoWrite":
		todos := parseTodoWriteInput(input)
		if todos == nil {
			return false
		}
		p.items, p.ids = todos, make([]string, len(todos))
		return true
	case "TaskCreate":
		var in struct {
			Subject    string `json:"subject"`
			ActiveForm string `json:"activeForm"`
		}
		if json.Unmarshal([]byte(input), &in) != nil || strings.TrimSpace(in.Subject) == "" {
			return false
		}
		p.items = append(p.items, todoItem{Content: in.Subject, Status: "pending", ActiveForm: in.ActiveForm})
		p.ids = append(p.ids, "")
		return true
	case "TaskUpdate":
		var in struct {
			TaskID     string `json:"taskId"`
			Status     string `json:"status"`
			Subject    string `json:"subject"`
			ActiveForm string `json:"activeForm"`
		}
		if json.Unmarshal([]byte(input), &in) != nil || in.TaskID == "" {
			return false
		}
		for i, id := range p.ids {
			if id != in.TaskID {
				continue
			}
			if in.Status == "deleted" {
				p.items = append(p.items[:i], p.items[i+1:]...)
				p.ids = append(p.ids[:i], p.ids[i+1:]...)
				return true
			}
			if in.Status != "" {
				p.items[i].Status = in.Status
			}
			if in.Subject != "" {
				p.items[i].Content = in.Subject
			}
			if in.ActiveForm != "" {
				p.items[i].ActiveForm = in.ActiveForm
			}
			return true
		}
	}
	return false
}

// result gives a created task the number the CLI assigned it.
func (p *planTracker) result(name, output string) {
	if name != "TaskCreate" {
		return
	}
	m := taskCreatedRe.FindStringSubmatch(output)
	if m == nil {
		return
	}
	for i := range p.ids {
		if p.ids[i] == "" {
			p.ids[i] = m[1]
			return
		}
	}
}

func (p *planTracker) todos() []todoItem {
	return append([]todoItem(nil), p.items...)
}
