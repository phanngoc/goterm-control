package coord

import (
	"strings"
	"testing"
	"time"
)

// The dashboard reads the data, everything else reads the text — and neither
// may see the other's half.
func TestAProgressLineCarriesItsStateBesideItsText(t *testing.T) {
	since := time.Date(2026, 11, 8, 14, 0, 0, 0, time.UTC)
	steps := []string{}
	for i := 0; i < 12; i++ {
		steps = append(steps, "Bash(step)")
	}
	steps[11] = "Bash(echo -- \"-->\")" // a comment terminator in the payload
	body := WithProgress(ProgressPrefix+"_đang làm_", Progress{
		Agent: "bomclaw", Label: "đang làm", Since: since, Steps: steps, StepCount: 12,
		Thinking: "đọc file", Todos: []ProgressTodo{{Content: "a", Status: "in_progress"}},
	})

	if strings.Count(body, "-->") != 1 || !strings.HasSuffix(body, "-->") {
		t.Fatalf("the comment is closed early, so the page would show its data:\n%s", body)
	}
	p, ok := ParseProgress(body)
	if !ok {
		t.Fatalf("no progress data in %q", body)
	}
	if !p.Since.Equal(since) || p.StepCount != 12 || len(p.Steps) != MaxProgressSteps {
		t.Errorf("parsed %+v", p)
	}
	if p.Steps[len(p.Steps)-1] != `Bash(echo -- "-->")` {
		t.Errorf("the running step came back as %q", p.Steps[len(p.Steps)-1])
	}
	if got := ProgressText(body); got != ProgressPrefix+"_đang làm_" {
		t.Errorf("text = %q", got)
	}
	if _, ok := ParseProgress("plain message"); ok {
		t.Error("found progress data in a plain message")
	}
}

// A room's main list says, under a message, that an agent is working in its
// thread right now — and what step it is on — without opening the thread.
// The preview text never shows the data comment.
func TestARoomShowsWhichThreadsHaveAnAgentAtWork(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw")
	root, _, err := db.PostMessage(NewChannelMessage{
		ChannelID: GeneralChannelID, AuthorKind: MemberUser, AuthorID: OwnerUserID, Body: "làm phim",
	})
	if err != nil {
		t.Fatal(err)
	}
	since := time.Now().Add(-90 * time.Second).UTC().Truncate(time.Second)
	if _, _, err := db.PostMessage(NewChannelMessage{
		ChannelID: GeneralChannelID, ThreadRoot: root.ID, AuthorID: "bomclaw",
		Body: WithProgress(ProgressPrefix+"_đang làm_", Progress{
			Label: "đang làm", Since: since, Steps: []string{"Read(a)", "Bash(render)"}, StepCount: 2,
			Partial: "đang viết…", Todos: []ProgressTodo{{Content: "x", Status: "pending"}},
		}),
	}); err != nil {
		t.Fatal(err)
	}

	msgs, err := db.ChannelMessages(GeneralChannelID, 10, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var got *ChannelMessage
	for i := range msgs {
		if msgs[i].ID == root.ID {
			got = &msgs[i]
		}
	}
	if got == nil || got.Working == nil {
		t.Fatalf("the room does not say anyone is working in the thread: %+v", got)
	}
	w := got.Working
	if w.Agent != "bomclaw" || !w.Since.Equal(since) || w.Steps[len(w.Steps)-1] != "Bash(render)" {
		t.Errorf("working = %+v", w)
	}
	if w.Partial != "" || w.Todos != nil {
		t.Error("the main list carried the thread's partial answer and plan")
	}
	if strings.Contains(got.LastReplyText, "bomclaw:progress") {
		t.Errorf("the preview shows the data comment: %q", got.LastReplyText)
	}
}
