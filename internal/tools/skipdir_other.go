//go:build !darwin

package tools

import "os"

// walkFilter is a no-op on non-macOS platforms.
// The macOS permission dialog issue does not apply.
type walkFilter struct{}

func newWalkFilter(_ string) *walkFilter {
	return &walkFilter{}
}

func (wf *walkFilter) shouldSkip(_ string, _ os.FileInfo) bool {
	return false
}
