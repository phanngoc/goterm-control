//go:build darwin

package tools

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// networkFSTypes lists macOS filesystem types that indicate network or
// virtual mounts. Traversing these triggers permission dialogs.
var networkFSTypes = map[string]bool{
	"nfs":      true, // NFS (standard, OrbStack)
	"smbfs":    true, // SMB/CIFS
	"afpfs":    true, // Apple Filing Protocol
	"webdavfs": true, // WebDAV
	"virtiofs": true, // VirtIO FS (OrbStack, Lima)
	"fuse":     true, // Generic FUSE (sshfs, etc.)
	"osxfuse":  true, // macFUSE v3
	"macfuse":  true, // macFUSE v4
}

// tccProtectedNames are home subdirectories guarded by macOS TCC — first
// access from a background service pops a permission dialog ("bomclaw wants
// to access your Downloads folder"). Walks that merely pass through (e.g.
// a search rooted at $HOME) skip them; explicitly rooting a search inside
// one is treated as user intent and allowed.
var tccProtectedNames = []string{"Music", "Pictures", "Movies", "Downloads"}

// tccProtectedDirs returns the absolute protected paths to skip for a walk
// rooted at root. A protected dir containing root is excluded — the caller
// asked for it explicitly.
func tccProtectedDirs(root string) map[string]bool {
	home := os.Getenv("HOME")
	if home == "" {
		return nil
	}
	out := make(map[string]bool, len(tccProtectedNames))
	for _, name := range tccProtectedNames {
		dir := filepath.Join(home, name)
		if root == dir || strings.HasPrefix(root, dir+string(filepath.Separator)) {
			continue
		}
		out[dir] = true
	}
	return out
}

// grepExcludeDirs returns grep --exclude-dir flags for content searches
// rooted at $HOME, so grep -r does not descend into TCC-protected folders.
// Searches rooted elsewhere get no excludes (a project dir named "Music"
// must stay searchable).
func grepExcludeDirs(root string) []string {
	if root == "" || root != os.Getenv("HOME") {
		return nil
	}
	args := make([]string, 0, len(tccProtectedNames))
	for _, name := range tccProtectedNames {
		args = append(args, "--exclude-dir="+name)
	}
	return args
}

// walkFilter detects network-mounted and TCC-protected directories during
// filepath.Walk using device IDs to minimize syscalls.
type walkFilter struct {
	rootDev   int32
	cache     map[int32]bool  // device ID → is network mount
	protected map[string]bool // absolute paths of TCC dirs to skip
}

func newWalkFilter(root string) *walkFilter {
	wf := &walkFilter{
		cache:     make(map[int32]bool),
		protected: tccProtectedDirs(root),
	}
	var stat syscall.Stat_t
	if err := syscall.Stat(root, &stat); err == nil {
		wf.rootDev = stat.Dev
		wf.cache[stat.Dev] = false // root filesystem is safe
	}
	return wf
}

// shouldSkip returns true if the directory at path is TCC-protected or on a
// network filesystem. It uses the device ID from info.Sys() to avoid
// redundant syscalls.
func (wf *walkFilter) shouldSkip(path string, info os.FileInfo) bool {
	if wf.protected[path] {
		return true
	}
	sys := info.Sys()
	if sys == nil {
		return false
	}
	st, ok := sys.(*syscall.Stat_t)
	if !ok {
		return false
	}

	// Same device as walk root → same filesystem → safe
	if st.Dev == wf.rootDev {
		return false
	}

	// Check cache for this device ID
	if result, ok := wf.cache[st.Dev]; ok {
		return result
	}

	// New device encountered — Statfs to check filesystem type
	result := isNetworkMount(path)
	wf.cache[st.Dev] = result
	return result
}

// isNetworkMount uses syscall.Statfs to check whether path is on a
// network or virtual filesystem.
func isNetworkMount(path string) bool {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return true // can't stat → skip to be safe
	}
	fstype := int8ArrayToString(stat.Fstypename[:])
	return networkFSTypes[fstype]
}

// int8ArrayToString converts a C-style null-terminated int8 array to a Go string.
func int8ArrayToString(arr []int8) string {
	buf := make([]byte, 0, len(arr))
	for _, b := range arr {
		if b == 0 {
			break
		}
		buf = append(buf, byte(b))
	}
	return string(buf)
}
