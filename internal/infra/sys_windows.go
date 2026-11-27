//go:build windows

package infra

import "os"

func diskUsage(string) (int64, int64) { return 0, 0 }

func killTERM(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

func killKILL(pid int) error { return killTERM(pid) }

func alive(pid int) bool {
	p, err := os.FindProcess(pid)
	return err == nil && p != nil
}
