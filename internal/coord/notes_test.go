package coord

import (
	"strings"
	"testing"
)

func TestAddAndListNotes(t *testing.T) {
	db := testDB(t)

	if _, err := db.AddNote(NewNote{Author: "a1", Title: "Codex only accepts gpt-6-astra",
		Kind: KindGotcha, Body: "gpt-5-codex returns 400 on a ChatGPT account", Tags: "codex,auth"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := db.AddNote(NewNote{Author: "a2", Title: "Use WAL for the shared db", Kind: KindDecision}); err != nil {
		t.Fatalf("add: %v", err)
	}

	notes, err := db.ListNotes(NoteFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(notes) != 2 {
		t.Fatalf("got %d notes, want 2", len(notes))
	}
	if notes[0].Title != "Use WAL for the shared db" {
		t.Errorf("newest first: got %q", notes[0].Title)
	}
	if notes[0].Scope != ScopeShared {
		t.Errorf("scope defaults to shared, got %q", notes[0].Scope)
	}
}

func TestNoteTitleIsRequired(t *testing.T) {
	db := testDB(t)
	if _, err := db.AddNote(NewNote{Author: "a1", Body: "orphan body"}); err == nil {
		t.Error("a note with no title was accepted")
	}
}

func TestSupersedeKeepsBothVersions(t *testing.T) {
	db := testDB(t)
	old, _ := db.AddNote(NewNote{Author: "a1", Title: "Model is gpt-5-codex"})

	newer, err := db.AddNote(NewNote{Author: "a2", Title: "Model is gpt-6-astra", Supersedes: old.ID})
	if err != nil {
		t.Fatalf("supersede: %v", err)
	}

	live, _ := db.ListNotes(NoteFilter{})
	if len(live) != 1 || live[0].ID != newer.ID {
		t.Fatalf("current notes = %d, want only the correction", len(live))
	}

	all, _ := db.ListNotes(NoteFilter{IncludeReplaced: true})
	if len(all) != 2 {
		t.Fatalf("history lost: %d rows, want 2", len(all))
	}
	for _, n := range all {
		if n.ID == old.ID && n.SupersededBy != newer.ID {
			t.Errorf("old note does not point at its replacement: %q", n.SupersededBy)
		}
	}
}

func TestSupersedeRejectsUnknownOrAlreadyReplaced(t *testing.T) {
	db := testDB(t)
	old, _ := db.AddNote(NewNote{Author: "a1", Title: "first"})
	db.AddNote(NewNote{Author: "a1", Title: "second", Supersedes: old.ID})

	if _, err := db.AddNote(NewNote{Author: "a1", Title: "third", Supersedes: old.ID}); err == nil {
		t.Error("superseding an already-replaced note was accepted — the chain would fork")
	}
	if _, err := db.AddNote(NewNote{Author: "a1", Title: "x", Supersedes: "n_nope"}); err == nil {
		t.Error("superseding an unknown note was accepted")
	}
}

func TestSearchFindsBodyAndTitle(t *testing.T) {
	db := testDB(t)
	db.AddNote(NewNote{Author: "a1", Title: "Codex auth", Body: "refresh token reused after relogin"})
	db.AddNote(NewNote{Author: "a1", Title: "Telegram conflict", Body: "orphaned claude subprocess holds getUpdates"})

	hits, err := db.SearchNotes("refresh token", "", 0)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 || hits[0].Title != "Codex auth" {
		t.Fatalf("search matched %d notes, want the codex one", len(hits))
	}

	hits, _ = db.SearchNotes("getUpdates", "", 0)
	if len(hits) != 1 || hits[0].Title != "Telegram conflict" {
		t.Errorf("body search failed: %d hits", len(hits))
	}
}

// A raw user string can contain FTS5 operators; unquoted it either errors or
// quietly means something else than what was typed.
func TestSearchToleratesOperatorCharacters(t *testing.T) {
	db := testDB(t)
	db.AddNote(NewNote{Author: "a1", Title: "quoted", Body: `he said "hello" loudly`})

	for _, q := range []string{`"hello"`, "AND OR NOT", "foo*", "a(b)c", "-x"} {
		if _, err := db.SearchNotes(q, "", 0); err != nil {
			t.Errorf("search(%q) errored: %v", q, err)
		}
	}
}

func TestSearchSkipsSupersededNotes(t *testing.T) {
	db := testDB(t)
	old, _ := db.AddNote(NewNote{Author: "a1", Title: "old", Body: "elephant"})
	db.AddNote(NewNote{Author: "a1", Title: "new", Body: "elephant corrected", Supersedes: old.ID})

	hits, _ := db.SearchNotes("elephant", "", 0)
	if len(hits) != 1 || hits[0].Title != "new" {
		t.Errorf("search returned %d hits, want only the current note", len(hits))
	}
}

func TestPrivateScopeIsNotVisibleToOtherAgents(t *testing.T) {
	db := testDB(t)
	db.AddNote(NewNote{Author: "a1", Scope: "a1", Title: "a1 private"})
	db.AddNote(NewNote{Author: "a1", Title: "everyone"})

	mine, _ := db.ListNotes(NoteFilter{Scope: "a1"})
	if len(mine) != 2 {
		t.Errorf("a1 sees %d notes, want its own plus shared", len(mine))
	}
	theirs, _ := db.ListNotes(NoteFilter{Scope: "a2"})
	if len(theirs) != 1 || theirs[0].Title != "everyone" {
		t.Errorf("a2 sees %d notes, want only the shared one", len(theirs))
	}
}

func TestRenderNotesGroupsByKind(t *testing.T) {
	out := RenderNotes([]Note{
		{Title: "T1", Kind: KindGotcha, Author: "a1", Body: "watch out"},
		{Title: "T2", Kind: KindFact, Author: "a2"},
	})
	if !strings.Contains(out, "## fact") || !strings.Contains(out, "## gotcha") {
		t.Errorf("missing kind headings:\n%s", out)
	}
	if strings.Index(out, "## fact") > strings.Index(out, "## gotcha") {
		t.Error("kinds must be in a stable order so the file does not churn")
	}
	if !strings.Contains(out, "Do not edit by hand") {
		t.Error("rendered file must warn that it is generated")
	}
}

func TestRenderNotesHandlesEmpty(t *testing.T) {
	if out := RenderNotes(nil); !strings.Contains(out, "No notes recorded yet") {
		t.Errorf("empty render is unhelpful:\n%s", out)
	}
}
