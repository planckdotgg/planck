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
