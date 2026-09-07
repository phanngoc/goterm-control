//go:build windows

package main

import (
	"log"
	"runtime"
	"sync"
	"syscall"
)

// SetThreadExecutionState flags. ES_SYSTEM_REQUIRED without
// ES_DISPLAY_REQUIRED is the counterpart of the macOS side's
// PreventUserIdleSystemSleep: the machine stays up, the display may still
// sleep, which is what a background agent needs.
const (
	esSystemRequired = 0x00000001
	esContinuous     = 0x80000000
)

var procSetThreadExecutionState = syscall.NewLazyDLL("kernel32.dll").
	NewProc("SetThreadExecutionState")

// awake holds and releases a sleep-prevention request.
//
// ES_CONTINUOUS is per-*thread* state that lives only as long as the thread
// that set it. Go moves goroutines between OS threads freely, so a naive call
// would be dropped the moment the runtime reused that thread elsewhere. All
// calls therefore go to one goroutine pinned with LockOSThread for the life of
// the process.
//
// Caveat worth knowing: on a machine using Modern Standby (S0 low-power idle),
// ES_SYSTEM_REQUIRED does not hold off the transition the way it does on
// classic S3 sleep. There is no user-space fix for that; the request is still
// the right thing to make.
type awake struct {
	start sync.Once
	cmds  chan bool // true = hold, false = release

	mu   sync.Mutex
	held bool
}

func (a *awake) ensure() {
	a.start.Do(func() {
		a.cmds = make(chan bool, 1)
		ready := make(chan struct{})
		go func() {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			close(ready)
			for hold := range a.cmds {
				flags := uintptr(esContinuous)
				if hold {
					flags |= esSystemRequired
				}
				// A zero return is the documented failure signal; the previous
				// state is returned otherwise.
				if r, _, err := procSetThreadExecutionState.Call(flags); r == 0 {
					log.Printf("bomtray: SetThreadExecutionState: %v", err)
				}
			}
		}()
		<-ready
	})
}

// Hold requests that the system stay awake (idempotent).
func (a *awake) Hold() error {
	a.ensure()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.held {
		return nil
	}
	a.cmds <- true
	a.held = true
	return nil
}

// Release drops the request (idempotent).
func (a *awake) Release() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.held {
		return
	}
	a.ensure()
	a.cmds <- false
	a.held = false
}

// Held reports whether the request is currently active.
func (a *awake) Held() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.held
}
