//go:build !windows

package infra

import "syscall"

func diskUsage(path string) (total, free int64) {
	var st syscall.Statfs_t
	if syscall.Statfs(path, &st) != nil {
		return 0, 0
	}
	return int64(st.Blocks) * int64(st.Bsize), int64(st.Bavail) * int64(st.Bsize)
}

func killTERM(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) }

func killKILL(pid int) error { return syscall.Kill(pid, syscall.SIGKILL) }

// alive: the pid exists (signal 0 checks without sending anything).
func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }
