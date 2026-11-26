//go:build windows

package win

import (
	"runtime"
	"testing"
)

func threadPriority() int32 {
	thread, _, _ := procGetCurrentThread.Call()
	p, _, _ := procGetThreadPriority.Call(thread)
	return int32(p)
}

func TestBoostThread(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	before := threadPriority()

	revert, err := BoostThread("Games")
	if err != nil {
		t.Fatal(err)
	}
	revert()

	// An unknown MMCSS task takes the SetThreadPriority fallback.
	revert, err = BoostThread("Planck no such task")
	if err != nil {
		t.Fatal(err)
	}
	if p := threadPriority(); p != threadPriorityHighest {
		t.Fatalf("fallback priority %d, want %d", p, threadPriorityHighest)
	}
	revert()
	if p := threadPriority(); p != before {
		t.Fatalf("priority %d after revert, want %d", p, before)
	}
}

// TestDisablePowerThrottling: the process leaves EcoQoS for good. A
// throttled background host stalled every thread 20-140 ms every few
// seconds.
func TestDisablePowerThrottling(t *testing.T) {
	if err := DisablePowerThrottling(); err != nil {
		t.Fatal(err)
	}
	control, state, err := powerThrottled()
	if err != nil {
		t.Fatal(err)
	}
	if control&powerThrottlingOptOut != powerThrottlingOptOut || state != 0 {
		t.Fatalf("throttling control %#x state %#x, want control %#x with state 0", control, state, powerThrottlingOptOut)
	}
}
