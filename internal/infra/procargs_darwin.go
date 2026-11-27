package infra

import "golang.org/x/sys/unix"

// procArgsOS reads another process's exec path, argv and environment from
// the kernel (kern.procargs2), exactly as it was started: no re-splitting of
// a ps line that lost its quoting. Only works for this user's processes.
func procArgsOS(pid int) (string, []string, map[string]string, error) {
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return "", nil, nil, err
	}
	return parseProcArgs(b)
}
