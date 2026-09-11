package taskrunner

import (
	"strings"
	"testing"
	"time"

	"github.com/ngocp/goterm-control/internal/coord"
)

// The gap this file closes: posting recorded the mention and rang the
// doorbell, but the doorbell woke a loop that only reads the task queue, so a
// mention sat unread forever with the agent awake beside it.
func TestAMentionBecomesWork(t *testing.T) {
	db := testDB(t)
	registerAgents(t, db, "a1", "a2")
	r := newRunner(db, &stubLLM{})

	if _, _, err := db.PostMessage(coord.NewChannelMessage{
		ChannelID: coord.GeneralChannelID, AuthorID: "a1",
		Body: "@a2 you are live — say hello in this thread",
	}); err != nil {
		t.Fatal(err)
	}

	r.mentionsToTasks()

	tasks, err := db.ListTasks(coord.TaskFilter{AgentID: "a2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("%d tasks for a2, want 1 — the mention never became work", len(tasks))
	}
	got := tasks[0]
	if got.Kind != coord.KindMention {
		t.Errorf("kind = %q, want %q", got.Kind, coord.KindMention)
	}
	if got.CreatedBy != "a1" {
		t.Errorf("created_by = %q, want the agent that named it", got.CreatedBy)
	}
	// The prompt has to carry what the agent cannot look up: the words, and
	// where the answer goes.
	for _, want := range []string{"say hello in this thread", "bomclaw ch post", coord.GeneralChannelID} {
		if !strings.Contains(got.Body, want) {
			t.Errorf("prompt missing %q:\n%s", want, got.Body)
		}
	}
	// Answered once, not on every sweep.
	r.mentionsToTasks()
	again, _ := db.ListTasks(coord.TaskFilter{AgentID: "a2"})
	if len(again) != 1 {
		t.Errorf("%d tasks after a second sweep, want 1", len(again))
	}
}

func TestOnlyTheNamedAgentIsGivenTheWork(t *testing.T) {
	db := testDB(t)
	registerAgents(t, db, "a1", "a2", "a3")

	if _, _, err := db.PostMessage(coord.NewChannelMessage{
		ChannelID: coord.GeneralChannelID, AuthorID: "a1", Body: "@a3 can you take this?",
	}); err != nil {
		t.Fatal(err)
	}
	// a2 sweeps first and must not pick up a summons addressed to a3.
	newRunnerFor(db, "a2").mentionsToTasks()
	if tasks, _ := db.ListTasks(coord.TaskFilter{AgentID: "a2"}); len(tasks) != 0 {
		t.Fatalf("a2 took %d task(s) from a mention of a3", len(tasks))
	}
	newRunnerFor(db, "a3").mentionsToTasks()
	if tasks, _ := db.ListTasks(coord.TaskFilter{AgentID: "a3"}); len(tasks) != 1 {
		t.Fatalf("a3 got %d task(s), want 1", len(tasks))
	}
}

func TestAnAgentStopsTakingMentionsWhenItIsBehind(t *testing.T) {
	db := testDB(t)
	registerAgents(t, db, "a1", "a2")
	r := newRunnerFor(db, "a2")

	// Two agents answering each other by name is a loop with no natural end.
	// The cap is the end.
	for i := 0; i < MaxMentionTasks+3; i++ {
		if _, _, err := db.PostMessage(coord.NewChannelMessage{
			ChannelID: coord.GeneralChannelID, AuthorID: "a1", Body: "@a2 and another thing",
		}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 5; i++ {
		r.mentionsToTasks()
	}

	tasks, _ := db.ListTasks(coord.TaskFilter{AgentID: "a2"})
	if len(tasks) > MaxMentionTasks {
		t.Fatalf("%d open mention tasks, want at most %d", len(tasks), MaxMentionTasks)
	}
	if len(tasks) == 0 {
		t.Fatal("the cap swallowed every mention")
	}
}

func TestAStaleMentionIsClearedNotAnswered(t *testing.T) {
	db := testDB(t)
	registerAgents(t, db, "a1", "a2")
	r := newRunnerFor(db, "a2")

	m, _, err := db.PostMessage(coord.NewChannelMessage{
		ChannelID: coord.GeneralChannelID, AuthorID: "a1", Body: "@a2 are you there",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Age it past the window: an agent back from a long outage should not
	// wake into a day of stale conversation.
	old := time.Now().Add(-StaleMention - time.Hour).UTC().Format("2006-01-02T15:04:05Z")
	if _, err := db.Conn().Exec(`UPDATE channel_messages SET created_at = ? WHERE id = ?`, old, m.ID); err != nil {
		t.Fatal(err)
	}

	r.mentionsToTasks()

	if tasks, _ := db.ListTasks(coord.TaskFilter{AgentID: "a2"}); len(tasks) != 0 {
		t.Errorf("answered a stale mention (%d tasks)", len(tasks))
	}
	pending, _ := db.UnreadMentions(coord.MemberAgent, "a2", 10)
	if len(pending) != 0 {
		t.Error("stale mention left unread — it would be reconsidered on every sweep forever")
	}
}

// newRunnerFor is newRunner under a chosen identity: mention handling is
// per-agent, so most of these tests need to say who is sweeping.
func newRunnerFor(db *coord.DB, agentID string) *Runner {
	return New(db, &stubLLM{}, nil, Config{
		AgentID:  agentID,
		Model:    "m",
		Interval: 10 * time.Millisecond,
		Timeout:  5 * time.Second,
	})
}

// registerAgents puts agents in the shared database so @names resolve —
// resolveMentions deliberately ignores an @ that is not a known agent.
func registerAgents(t *testing.T, db *coord.DB, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if err := db.RegisterAgent(coord.Agent{ID: id, DisplayName: id}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
}
