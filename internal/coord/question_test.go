package coord

import (
	"path/filepath"
	"strings"
	"testing"
)

func openQuestionDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "coord.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func claimed(t *testing.T, db *DB) *Task {
	t.Helper()
	if _, err := db.CreateTask(NewTask{CreatedBy: "a2", Title: "publish notes"}); err != nil {
		t.Fatal(err)
	}
	c, err := db.ClaimTask("a1")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// The question a person is shown is the one the agent wrote, whatever order
// its note is in — the reason the column exists.
func TestABlockShowsTheQuestionNotTheNotesLastParagraph(t *testing.T) {
	db := openQuestionDB(t)
	c := claimed(t, db)
	note := "Notes are ready in NOTES.md.\n\nOptions: blog, changelog page, or email."
	if err := db.BlockTaskAsking(c.ID, "a1", c.Attempts, BlockedOnHuman, note, "Where should the notes be published?"); err != nil {
		t.Fatal(err)
	}
	got, _ := db.GetTask(c.ID)
	if q := AttentionQuestion(got); q != "Where should the notes be published?" {
		t.Fatalf("question = %q", q)
	}
	if got.Checkpoint != note {
		t.Fatalf("the note is the next run's evidence and must be kept; got %q", got.Checkpoint)
	}
}

// A block without --ask (older agents, the runtime's own blocks) still shows
// something: the last paragraph, as before.
func TestABlockWithoutAQuestionFallsBackToTheNote(t *testing.T) {
	db := openQuestionDB(t)
	c := claimed(t, db)
	if err := db.BlockTask(c.ID, "a1", c.Attempts, BlockedOnHuman, "context\n\nWhich account?"); err != nil {
		t.Fatal(err)
	}
	got, _ := db.GetTask(c.ID)
	if q := AttentionQuestion(got); q != "Which account?" {
		t.Fatalf("question = %q", q)
	}
}

// Only a person is asked anything; a block on children carries no question.
func TestABlockOnChildrenAsksNobody(t *testing.T) {
	db := openQuestionDB(t)
	c := claimed(t, db)
	if err := db.BlockTaskAsking(c.ID, "a1", c.Attempts, BlockedOnChildren, "", "ignored?"); err != nil {
		t.Fatal(err)
	}
	got, _ := db.GetTask(c.ID)
	if got.Question != "" {
		t.Fatalf("question on a children block: %q", got.Question)
	}
}

// Unblocking answers the question: the column clears, and the next run reads
// the question beside the answer so it knows what was decided.
func TestUnblockPairsTheAnswerWithItsQuestion(t *testing.T) {
	db := openQuestionDB(t)
	c := claimed(t, db)
	if err := db.BlockTaskAsking(c.ID, "a1", c.Attempts, BlockedOnHuman, "ready", "Blog or changelog?"); err != nil {
		t.Fatal(err)
	}
	if err := db.UnblockTask(c.ID, "owner", "changelog"); err != nil {
		t.Fatal(err)
	}
	got, _ := db.GetTask(c.ID)
	if got.Question != "" {
		t.Fatalf("question survived the answer: %q", got.Question)
	}
	if !strings.Contains(got.Checkpoint, "Question: Blog or changelog?\nAnswer from owner: changelog") {
		t.Fatalf("checkpoint lost the pairing:\n%s", got.Checkpoint)
	}
}
