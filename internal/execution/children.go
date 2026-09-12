package execution

import (
	"log"
	"os"
	"os/exec"
	"sync"
)

// The CLI processes this gateway has spawned, and how they die.
//
// Every turn shells out to `claude` or `codex`. exec.CommandContext kills the
// child when that turn's context ends, which covers the normal case and misses
// the one that hurt: the gateway itself going away. A process that outlives its
// parent keeps whatever the parent was holding — for the Telegram backend that
// means the long poll, and the next gateway to start is answered with
// "Conflict: terminated by other getUpdates request" by a bot it cannot see.
//
// Two things are needed, not one. A registry, because nothing kept a list of
// children to kill on the way out. And a process GROUP, because Process.Kill
// reaches the child and not the child's own children — a CLI that spawned a
// helper leaves the helper holding the socket.
//
// Registration is by pid rather than by *exec.Cmd so nothing here can block on
// or race with the goroutine doing Wait.
var spawned = &children{procs: map[int]*os.Process{}}

type children struct {
	mu    sync.Mutex
	procs map[int]*os.Process
}

// Detach puts a command in its own process group and, when it has a context,
// makes cancelling that context kill the whole group rather than the leader
// alone. Call it before Start.
//
// The Cancel hook is only installed on a command that already has one:
// CommandContext sets a default that calls Process.Kill, and Start refuses a
// Cancel on a command built with plain Command. Testing for it is how this
// tells the two apart without asking the caller to say which it used.
//
// On Windows the process group is a no-op — there is nothing to put a child in
// — and KillGroup falls back to killing the process alone, as before.
func Detach(cmd *exec.Cmd) {
	detach(cmd)
	if cmd.Cancel == nil {
		return
	}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return KillGroup(cmd.Process)
	}
}

// Track records a started process and returns the function that forgets it.
// Call it right after Start, and defer the result:
//
//	Detach(cmd)
//	cmd.Start()
//	defer Track(cmd)()
func Track(cmd *exec.Cmd) func() {
	if cmd.Process == nil {
		return func() {}
	}
	pid, p := cmd.Process.Pid, cmd.Process
	spawned.mu.Lock()
	spawned.procs[pid] = p
	spawned.mu.Unlock()
	return func() {
		spawned.mu.Lock()
		delete(spawned.procs, pid)
		spawned.mu.Unlock()
	}
}

// KillSpawned kills every CLI process this gateway started, and their
// descendants, returning how many it signalled. Shutdown calls it; nothing
// else should, because a live turn's child is in here too.
func KillSpawned() int {
	spawned.mu.Lock()
	procs := make([]*os.Process, 0, len(spawned.procs))
	for pid, p := range spawned.procs {
		procs = append(procs, p)
		delete(spawned.procs, pid)
	}
	spawned.mu.Unlock()

	killed := 0
	for _, p := range procs {
		if err := KillGroup(p); err != nil {
			// Already gone is the common case and not worth a line.
			if !isGone(err) {
				log.Printf("execution: kill %d: %v", p.Pid, err)
			}
			continue
		}
		killed++
	}
	return killed
}
