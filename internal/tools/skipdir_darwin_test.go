//go:build darwin

package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInt8ArrayToString(t *testing.T) {
	// Simulate "nfs\0" as int8 array
	arr := [16]int8{110, 102, 115, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	got := int8ArrayToString(arr[:])
	if got != "nfs" {
		t.Errorf("expected 'nfs', got %q", got)
	}
}

func TestInt8ArrayToString_Empty(t *testing.T) {
	arr := [16]int8{}
	got := int8ArrayToString(arr[:])
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestIsNetworkMount_LocalPath(t *testing.T) {
	// /tmp should be on a local filesystem (apfs or hfs)
	if isNetworkMount("/tmp") {
		t.Error("expected /tmp to not be detected as network mount")
	}
}

func TestWalkFilter_LocalDir(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "subdir")
	os.Mkdir(sub, 0755)

	wf := newWalkFilter(dir)
	info, err := os.Stat(sub)
	if err != nil {
		t.Fatal(err)
	}

	if wf.shouldSkip(sub, info) {
		t.Error("expected local subdirectory to not be skipped")
	}
}

func TestWalkFilter_CachesDeviceID(t *testing.T) {
	dir := t.TempDir()
	sub1 := filepath.Join(dir, "a")
	sub2 := filepath.Join(dir, "b")
	os.Mkdir(sub1, 0755)
	os.Mkdir(sub2, 0755)

	wf := newWalkFilter(dir)

	info1, _ := os.Stat(sub1)
	info2, _ := os.Stat(sub2)

	// Both should be on the same device as root — fast path, no Statfs needed
	wf.shouldSkip(sub1, info1)
	wf.shouldSkip(sub2, info2)

	// Cache should have exactly 1 entry (the root device)
	if len(wf.cache) != 1 {
		t.Errorf("expected 1 cache entry (root device), got %d", len(wf.cache))
	}
}
