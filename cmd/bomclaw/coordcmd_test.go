package main

import (
	"path/filepath"
	"testing"

	"github.com/ngocp/goterm-control/internal/coord"
)

func threadTestDB(t *testing.T) *coord.DB {
	t.Helper()
	db, err := coord.Open(filepath.Join(t.TempDir(), "coord.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.RegisterAgent(coord.Agent{ID: "bomclaw", DisplayName: "bomclaw"}); err != nil {
		t.Fatal(err)
	}
	if err := db.RegisterAgent(coord.Agent{ID: "bomclaw2", DisplayName: "bomclaw2"}); err != nil {
		t.Fatal(err)
	}
	return db
}

func rootIn(t *testing.T, db *coord.DB, channelID string) string {
	t.Helper()
	m, _, err := db.PostMessage(coord.NewChannelMessage{
		ChannelID: channelID, AuthorID: "bomclaw", Body: "việc này cần làm",
	})
	if err != nil {
		t.Fatal(err)
	}
	return m.ID
}

// Work opened from a project's thread belongs to that project.
func TestThreadChannelFilesWorkUnderItsProject(t *testing.T) {
	db := threadTestDB(t)
	p, err := db.CreateProject("bất động sản", "", "owner", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := threadChannel(db, rootIn(t, db, p.ID)); got != p.ID {
		t.Fatalf("threadChannel = %q, want the project %q", got, p.ID)
	}
}

// A DM and #general are rooms, not projects. Filing a task into one hides it
// from every filter at once: it is not unfiled, it is filed somewhere nothing
// looks — the board's project list only holds rooms with a folder. A real piece
// of work sat in a DM this way and was invisible on the board.
//
// Unfiled is honest: it shows under "việc lẻ", where it can be moved.
func TestThreadChannelRefusesRoomsThatAreNotProjects(t *testing.T) {
	db := threadTestDB(t)
	dm, err := db.EnsureDM("bomclaw", "bomclaw2")
	if err != nil {
		t.Fatal(err)
	}
	for _, room := range []string{dm.ID, coord.GeneralChannelID} {
		if got := threadChannel(db, rootIn(t, db, room)); got != "" {
			t.Errorf("a thread in %s filed work under %q; the board would never show it", room, got)
		}
	}
}

// But the note saying a task was opened still has to land somewhere, or it is
// posted into the void. Two different questions, and they were one function
// until the second one started returning "".
func TestThreadRoomAlwaysNamesSomewhereToPost(t *testing.T) {
	db := threadTestDB(t)
	dm, err := db.EnsureDM("bomclaw", "bomclaw2")
	if err != nil {
		t.Fatal(err)
	}
	if got := threadRoom(db, rootIn(t, db, dm.ID)); got != dm.ID {
		t.Errorf("threadRoom = %q, want the DM %q it was asked in", got, dm.ID)
	}
	// An unknown thread still resolves somewhere rather than nowhere.
	if got := threadRoom(db, "cm_does-not-exist"); got == "" {
		t.Error("an unresolvable thread gave nowhere to post")
	}
}
