package bot

import "testing"

// The task tools number their items in the result, not the call; an update
// names that number. Exactly what the CLI sent in a real run (2.1.318).
func TestPlanFollowsTheTaskTools(t *testing.T) {
	var p planTracker
	if !p.call("TaskCreate", `{"subject":"Say hi","description":"Task A","activeForm":"Saying hi"}`) {
		t.Fatal("create not applied")
	}
	p.result("TaskCreate", "Task #1 created successfully: Say hi")
	p.call("TaskCreate", `{"subject":"Say bye","description":"Task B","activeForm":"Saying bye"}`)
	p.result("TaskCreate", "Task #2 created successfully: Say bye")
	p.call("TaskUpdate", `{"taskId":"1","status":"in_progress"}`)
	got := p.todos()
	if len(got) != 2 || got[0].Status != "in_progress" || got[1].Status != "pending" || got[0].ActiveForm != "Saying hi" {
		t.Fatalf("plan = %+v", got)
	}
	p.call("TaskUpdate", `{"taskId":"1","status":"completed"}`)
	if p.todos()[0].Status != "completed" {
		t.Errorf("completion lost: %+v", p.todos())
	}
	if p.call("TaskUpdate", `{"taskId":"9","status":"completed"}`) {
		t.Error("an update for a task this turn never saw changed the plan")
	}
	p.call("TaskUpdate", `{"taskId":"2","status":"deleted"}`)
	if len(p.todos()) != 1 {
		t.Errorf("delete not applied: %+v", p.todos())
	}
}

func TestPlanTakesTodoWriteWhole(t *testing.T) {
	var p planTracker
	p.call("TodoWrite", `{"todos":[{"content":"a","status":"completed"},{"content":"b","status":"pending"}]}`)
	if got := p.todos(); len(got) != 2 || got[1].Content != "b" {
		t.Fatalf("plan = %+v", got)
	}
	if !isPlanTool("TaskUpdate") || isPlanTool("Bash") {
		t.Error("plan tool classification")
	}
}
