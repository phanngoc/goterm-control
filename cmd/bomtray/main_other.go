//go:build !darwin

// bomtray is macOS-only: it renders a menu bar (NSStatusItem) icon and holds
// IOKit power assertions, both of which require Cocoa/IOKit.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "bomtray is macOS-only (build and run it on darwin)")
	os.Exit(1)
}
