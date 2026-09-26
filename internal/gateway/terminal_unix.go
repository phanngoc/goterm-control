//go:build !windows

package gateway

import (
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

type ptyShell struct {
	f    *os.File
	cmd  *exec.Cmd
	once sync.Once
}

// startShell runs the user's login shell on a PTY in dir.
//
// A login shell because the dashboard runs under launchd with next to no
// environment — HOME and little else — and the profile is what puts brew,
// node and friends on PATH. LANG is set when missing so the Vietnamese in
// every project's notes does not arrive as question marks.
func startShell(dir, channel string, cols, rows int) (shell, error) {
	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = "/bin/zsh"
	}
	cmd := exec.Command(sh, "-l")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"BOMCLAW_PROJECT="+channel,
	)
	if os.Getenv("LANG") == "" {
		cmd.Env = append(cmd.Env, "LANG=en_US.UTF-8")
	}
	if cols <= 0 || cols > 1000 {
		cols = 80
	}
	if rows <= 0 || rows > 500 {
		rows = 24
	}
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return nil, err
	}
	return &ptyShell{f: f, cmd: cmd}, nil
}

func (s *ptyShell) Read(p []byte) (int, error)  { return s.f.Read(p) }
func (s *ptyShell) Write(p []byte) (int, error) { return s.f.Write(p) }

func (s *ptyShell) Resize(cols, rows int) {
	if cols > 0 && rows > 0 && cols <= 1000 && rows <= 500 {
		_ = pty.Setsize(s.f, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	}
}

// Close ends the session: HUP to the shell's whole process group — it is a
// session leader, so a `npm run dev` it started goes too — then KILL for
// anything that ignored the hangup. A closed browser tab must not leave a
// dev server holding a port.
func (s *ptyShell) Close() {
	s.once.Do(func() {
		pid := s.cmd.Process.Pid
		_ = syscall.Kill(-pid, syscall.SIGHUP)
		_ = s.f.Close()
		exited := make(chan struct{})
		go func() { _ = s.cmd.Wait(); close(exited) }()
		select {
		case <-exited:
		case <-time.After(2 * time.Second):
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			<-exited
		}
	})
}
