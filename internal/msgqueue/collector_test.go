package msgqueue

import (
	"strings"
	"testing"
)

func TestCollectorFreeAgent(t *testing.T) {
	c := NewCollector()

	// Agent is free — should return true
	if !c.TryRun(1, "hello") {
		t.Fatal("expected TryRun to return true when agent is free")
	}

	// Done with no followup
	followup, ok := c.Done(1)
	if ok {
		t.Fatalf("expected no followup, got: %s", followup)
	}
}

func TestCollectorBusyCollects(t *testing.T) {
	c := NewCollector()

	// First message starts execution
	if !c.TryRun(1, "first") {
		t.Fatal("first TryRun should succeed")
	}

	// While busy, new messages are collected
	if c.TryRun(1, "second") {
		t.Fatal("second TryRun should return false (busy)")
	}
	if c.TryRun(1, "third") {
		t.Fatal("third TryRun should return false (busy)")
	}

	// Done returns collected as followup
	followup, ok := c.Done(1)
	if !ok {
		t.Fatal("expected followup from Done")
	}
	if !strings.Contains(followup, "second") || !strings.Contains(followup, "third") {
		t.Fatalf("followup should contain collected messages, got: %s", followup)
	}
}

func TestCollectorDrainLoop(t *testing.T) {
	c := NewCollector()

	// Start execution
	c.TryRun(1, "first")

	// Collect one message
	c.TryRun(1, "second")

	// Done returns followup and stays busy
	followup, ok := c.Done(1)
	if !ok || followup == "" {
		t.Fatal("expected followup")
	}

	// No more collected — Done releases busy
	followup2, ok2 := c.Done(1)
	if ok2 {
		t.Fatalf("expected no more followup, got: %s", followup2)
	}

	// Agent should be free now
	if !c.TryRun(1, "new message") {
		t.Fatal("agent should be free after drain completes")
	}
}

func TestCollectorCancel(t *testing.T) {
	c := NewCollector()

	c.TryRun(1, "first")
	c.TryRun(1, "queued")

	c.Cancel(1)

	followup, ok := c.Done(1)
	if ok {
		t.Fatalf("expected no followup after cancel, got: %s", followup)
	}
}

func TestCollectorIsolatesChats(t *testing.T) {
	c := NewCollector()

	// Chat 1 is busy
	c.TryRun(1, "chat1")
	// Chat 2 should be free
	if !c.TryRun(2, "chat2") {
		t.Fatal("chat 2 should be free while chat 1 is busy")
	}
}

func TestFormatFollowupSingle(t *testing.T) {
	result := formatFollowup([]string{"just one"})
	if result != "just one" {
		t.Fatalf("single message should pass through, got: %s", result)
	}
}

func TestFormatFollowupMultiple(t *testing.T) {
	result := formatFollowup([]string{"first", "second"})
	if !strings.Contains(result, "Queued messages") {
		t.Fatal("multi-message should have queued header")
	}
	if !strings.Contains(result, "Message 1:") || !strings.Contains(result, "Message 2:") {
		t.Fatal("multi-message should number each message")
	}
}
