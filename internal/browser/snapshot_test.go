package browser

import (
	"strings"
	"testing"
)

// The snapshot text tree is what the agent reads before it picks a ref, and
// both backends (managed Chrome over CDP, and the Browser Bridge extension)
// now render through this one formatter. Pin the shape.
func TestFormatSnapshot(t *testing.T) {
	got := FormatSnapshot([]SnapshotNode{
		{Ref: "n1", Depth: 0, Tag: "html"},
		{Ref: "n2", Depth: 1, Tag: "body"},
		{Ref: "n3", Depth: 2, Tag: "input", ID: "q", Type: "search", Value: "shoes"},
		{Ref: "n4", Depth: 2, Tag: "a", Href: "https://example.com", Text: "Home"},
		{Ref: "n5", Depth: 2, Tag: "div", Role: "alert", Name: "Errors"},
	})
	want := strings.Join([]string{
		`[n1] <html>`,
		`  [n2] <body>`,
		`    [n3] <input> #q type=search value="shoes"`,
		`    [n4] <a> href=https://example.com "Home"`,
		`    [n5] <div> role=alert "Errors"`,
		``,
	}, "\n")
	if got != want {
		t.Errorf("snapshot tree:\n got:\n%s\nwant:\n%s", got, want)
	}
}

// Long text is dropped rather than wrapped, so one verbose node cannot bury
// the rest of the tree.
func TestFormatSnapshotDropsLongText(t *testing.T) {
	long := strings.Repeat("x", 120)
	got := FormatSnapshot([]SnapshotNode{{Ref: "n1", Tag: "p", Text: long}})
	if strings.Contains(got, long) {
		t.Errorf("text of %d chars should not be printed inline: %s", len(long), got)
	}
	if got != "[n1] <p>\n" {
		t.Errorf("got %q", got)
	}
}

func TestFormatSnapshotJSON(t *testing.T) {
	// The wrapped shape the in-page script and the extension both return.
	wrapped, err := FormatSnapshotJSON([]byte(`{"nodes":[{"ref":"n1","depth":0,"tag":"html"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if wrapped != "[n1] <html>\n" {
		t.Errorf("wrapped: got %q", wrapped)
	}

	// A bare array is accepted too, so a simpler client need not wrap.
	bare, err := FormatSnapshotJSON([]byte(`[{"ref":"n1","depth":0,"tag":"html"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if bare != wrapped {
		t.Errorf("bare array should format the same: got %q", bare)
	}

	if _, err := FormatSnapshotJSON([]byte(`"not a snapshot"`)); err == nil {
		t.Error("expected an error for a non-node payload")
	}
}
