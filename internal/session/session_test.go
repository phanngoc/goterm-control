package session

import "testing"

// A run hands its CLI a few facts about itself. Per session rather than
// os.Setenv, because a gateway runs several turns at once and a process-wide
// variable would give one turn's task id to another turn's CLI.
func TestSessionEnvIsPerConversation(t *testing.T) {
	a, b := New(1), New(2)
	if got := a.GetEnv(); got != nil {
		t.Fatalf("a fresh session already carries %v", got)
	}
	a.SetEnv("BOMCLAW_TASK_ID", "t_1")
	a.SetEnv("BOMCLAW_TASK_CHANNEL", "ch_bds")
	b.SetEnv("BOMCLAW_TASK_ID", "t_2")

	want := []string{"BOMCLAW_TASK_CHANNEL=ch_bds", "BOMCLAW_TASK_ID=t_1"}
	got := a.GetEnv()
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("GetEnv() = %v, want %v (sorted, KEY=VALUE)", got, want)
	}
	if other := b.GetEnv(); len(other) != 1 || other[0] != "BOMCLAW_TASK_ID=t_2" {
		t.Fatalf("one conversation saw another's environment: %v", other)
	}
	// A nil receiver is the "no session" case the clients hit on a bare call.
	var none *Session
	if got := none.GetEnv(); got != nil {
		t.Errorf("nil session returned %v", got)
	}
}
