//go:build darwin

package tools

import (
	"os"
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

// walkFilter detects network-mounted directories during filepath.Walk
// using device IDs to minimize syscalls.
type walkFilter struct {
	rootDev int32
	cache   map[int32]bool // device ID → is network mount
}

func newWalkFilter(root string) *walkFilter {
	wf := &walkFilter{cache: make(map[int32]bool)}
	var stat syscall.Stat_t
	if err := syscall.Stat(root, &stat); err == nil {
		wf.rootDev = stat.Dev
		wf.cache[stat.Dev] = false // root filesystem is safe
	}
	return wf
}

// shouldSkip returns true if the directory at path is on a network filesystem.
// It uses the device ID from info.Sys() to avoid redundant syscalls.
func (wf *walkFilter) shouldSkip(path string, info os.FileInfo) bool {
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
