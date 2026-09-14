package gateway

import (
	"testing"
	"time"

	"github.com/ngocp/goterm-control/internal/coord"
)

// The case this exists for: agent 2 opens a task for agent 1, agent 1 works it
// out with agent 3 in their own room, and none of that is filed against the
// task. Until now the board showed the work and hid the conversation that
// shaped it.
func TestSideTalkFindsTheConversationAroundATask(t *testing.T) {
	db := testCoordDB(t)
	for _, id := range []string{"bomclaw", "bomclaw2", "bomclaw3"} {
		if err := db.RegisterAgent(coord.Agent{ID: id, DisplayName: id}); err != nil {
			t.Fatal(err)
		}
	}
	task, err := db.CreateTask(coord.NewTask{
		CreatedBy: "bomclaw2", AssignedTo: "bomclaw", Title: "visualize 3D",
	})
	if err != nil {
		t.Fatal(err)
	}
	// A child handed to the third agent is what puts it among the participants.
	child, err := db.CreateTask(coord.NewTask{
		CreatedBy: "bomclaw", AssignedTo: "bomclaw3", ParentID: task.ID, Title: "dữ liệu thật",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SendMessage("bomclaw", "bomclaw3", "", "[3D viewer] chốt schema ở parcel-contract.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SendMessage("bomclaw3", "bomclaw", "", "Got it — Đà Nẵng locked in"); err != nil {
		t.Fatal(err)
	}

	deps := Deps{Coord: db}
	talk, err := taskSideTalk(deps, task, []coord.Task{*child})
	if err != nil {
		t.Fatal(err)
	}
	if len(talk) != 2 {
		t.Fatalf("got %d lines, want the two the agents exchanged: %+v", len(talk), talk)
	}
	if talk[0].FromAgent != "bomclaw" || talk[1].FromAgent != "bomclaw3" {
		t.Errorf("out of order, so it does not read as a conversation: %+v", talk)
	}
}

// A finished task stops moving, and its window closes with it. Without that a
// task from last month would keep collecting whatever its agents said since.
func TestSideTalkStopsWhenTheTaskDoes(t *testing.T) {
	db := testCoordDB(t)
	for _, id := range []string{"bomclaw", "bomclaw3"} {
		if err := db.RegisterAgent(coord.Agent{ID: id, DisplayName: id}); err != nil {
			t.Fatal(err)
		}
	}
	task, err := db.CreateTask(coord.NewTask{
		CreatedBy: "bomclaw", AssignedTo: "bomclaw3", Title: "xong từ lâu",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SendMessage("bomclaw", "bomclaw3", "", "chuyện của hôm nay"); err != nil {
		t.Fatal(err)
	}

	// Finished well before that line was written.
	task.State = coord.TaskCompleted
	task.UpdatedAt = time.Now().Add(-24 * time.Hour)
	talk, err := taskSideTalk(Deps{Coord: db}, task, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(talk) != 0 {
		t.Fatalf("a finished task kept collecting later conversation: %+v", talk)
	}
}

// A task nobody has claimed has one participant and no peer room. That must be
// an empty list, not an error and not every message on the machine.
func TestSideTalkOfAnUnclaimedTaskIsEmpty(t *testing.T) {
	db := testCoordDB(t)
	if err := db.RegisterAgent(coord.Agent{ID: "bomclaw", DisplayName: "bomclaw"}); err != nil {
		t.Fatal(err)
	}
	task, err := db.CreateTask(coord.NewTask{CreatedBy: "bomclaw", Title: "chưa ai nhận"})
	if err != nil {
		t.Fatal(err)
	}
	talk, err := taskSideTalk(Deps{Coord: db}, task, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(talk) != 0 {
		t.Fatalf("got %+v", talk)
	}
}
