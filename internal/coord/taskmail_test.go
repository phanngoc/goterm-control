package coord

import (
	"testing"
	"time"
)

func TestTaskMailIsWhatWasFiledAgainstTheTask(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw", "bomclaw2", "bomclaw3")

	task, err := db.CreateTask(NewTask{CreatedBy: "bomclaw2", AssignedTo: "bomclaw", Title: "visualize"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SendMessage("bomclaw2", "bomclaw", task.ID, "schema chốt ở parcel-contract.md"); err != nil {
		t.Fatal(err)
	}
	// The same two agents talking about something else entirely.
	if _, err := db.SendMessage("bomclaw2", "bomclaw", "", "trưa nay deploy nhé"); err != nil {
		t.Fatal(err)
	}

	mail, err := db.TaskMail(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(mail) != 1 {
		t.Fatalf("got %d messages, want only the one filed against the task: %+v", len(mail), mail)
	}
	if mail[0].FromAgent != "bomclaw2" || mail[0].ToAgent != "bomclaw" {
		t.Errorf("lost who was talking to whom: %+v", mail[0])
	}
}

// The rooms are derived, not stored: two agents coordinating have exactly one.
func TestDMRoomsAmongPairsEveryoneOnce(t *testing.T) {
	rooms := DMRoomsAmong([]string{"bomclaw2", "bomclaw", "bomclaw3", "bomclaw", ""})
	if len(rooms) != 3 {
		t.Fatalf("three agents make three pair-rooms, got %d: %v", len(rooms), rooms)
	}
	seen := map[string]bool{}
	for _, r := range rooms {
		if seen[r] {
			t.Fatalf("the same room twice: %v", rooms)
		}
		seen[r] = true
	}
	// Derivable from either side, which is what makes storing them unnecessary.
	if !seen[DMChannelID("bomclaw", "bomclaw3")] {
		t.Errorf("missing the room the task's two agents actually share: %v", rooms)
	}
}

// The window is the point. Two agents that talk every day would otherwise
// drown one task's exchange in a month of unrelated lines.
func TestMessagesInIsBoundedByItsWindow(t *testing.T) {
	db := testDB(t)
	registerTestAgents(t, db, "bomclaw", "bomclaw3")

	m, err := db.SendMessage("bomclaw", "bomclaw3", "", "về task đang chạy")
	if err != nil {
		t.Fatal(err)
	}
	room := DMChannelID("bomclaw", "bomclaw3")

	inside, err := db.MessagesIn([]string{room}, m.CreatedAt.Add(-time.Minute), time.Time{}, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(inside) != 1 {
		t.Fatalf("a line inside the window was not returned: %+v", inside)
	}

	after, err := db.MessagesIn([]string{room}, m.CreatedAt.Add(time.Minute), time.Time{}, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Fatalf("a line from before the task started was counted as part of it: %+v", after)
	}

	before, err := db.MessagesIn([]string{room}, m.CreatedAt.Add(-time.Hour), m.CreatedAt.Add(-time.Minute), 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 0 {
		t.Fatalf("a line from after the task finished was counted as part of it: %+v", before)
	}
}

func TestMessagesInWithNoRoomsAsksNothing(t *testing.T) {
	db := testDB(t)
	got, err := db.MessagesIn(nil, time.Time{}, time.Time{}, 50)
	if err != nil {
		t.Fatalf("a task with one agent and no peer must not be an error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}
