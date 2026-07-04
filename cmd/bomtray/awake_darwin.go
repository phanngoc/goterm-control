//go:build darwin

package main

/*
#cgo LDFLAGS: -framework CoreFoundation -framework IOKit
#include <CoreFoundation/CoreFoundation.h>
#include <IOKit/pwr_mgt/IOPMLib.h>

static IOReturn bomtrayCreateAssertion(IOPMAssertionID *id) {
	return IOPMAssertionCreateWithName(
		kIOPMAssertionTypePreventUserIdleSystemSleep,
		kIOPMAssertionLevelOn,
		CFSTR("bomclaw: agent task in progress"),
		id);
}

static IOReturn bomtrayReleaseAssertion(IOPMAssertionID id) {
	return IOPMAssertionRelease(id);
}
*/
import "C"

import "fmt"

// awake holds/releases an IOKit power assertion to keep the Mac awake
// (kwota pattern — direct assertion, no caffeinate child process).
// PreventUserIdleSystemSleep lets the display sleep but keeps the system
// running, which is what a background agent needs.
type awake struct {
	id   C.IOPMAssertionID
	held bool
}

// Hold acquires the assertion (idempotent).
func (a *awake) Hold() error {
	if a.held {
		return nil
	}
	if ret := C.bomtrayCreateAssertion(&a.id); ret != C.kIOReturnSuccess {
		return fmt.Errorf("IOPMAssertionCreateWithName failed: 0x%x", uint32(ret))
	}
	a.held = true
	return nil
}

// Release drops the assertion (idempotent).
func (a *awake) Release() {
	if !a.held {
		return
	}
	C.bomtrayReleaseAssertion(a.id)
	a.held = false
}

// Held reports whether the assertion is currently active.
func (a *awake) Held() bool { return a.held }
