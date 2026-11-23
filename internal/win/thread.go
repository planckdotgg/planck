//go:build windows

package win

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	avrtDLL                  = syscall.NewLazyDLL("avrt.dll")
	procAvSetMmThreadChar    = avrtDLL.NewProc("AvSetMmThreadCharacteristicsW")
	procAvRevertMmThreadChar = avrtDLL.NewProc("AvRevertMmThreadCharacteristics")

	kernel32              = syscall.NewLazyDLL("kernel32.dll")
	procGetCurrentThread  = kernel32.NewProc("GetCurrentThread")
	procSetThreadPriority = kernel32.NewProc("SetThreadPriority")
	procGetThreadPriority = kernel32.NewProc("GetThreadPriority")
)

const threadPriorityHighest = 2

// BoostThread registers the calling OS thread with the Multimedia Class
// Scheduler Service as task ("Games", "Pro Audio"), falling back to
// THREAD_PRIORITY_HIGHEST when MMCSS is unavailable. At normal priority a
// paced encode thread wakes late often enough to add 5-30 ms tails. The
// caller must hold runtime.LockOSThread until it calls revert.
func BoostThread(task string) (revert func(), err error) {
	name, err := syscall.UTF16PtrFromString(task)
	if err != nil {
		return nil, err
	}
	if procAvSetMmThreadChar.Find() == nil {
		var index uint32
		h, _, callErr := procAvSetMmThreadChar.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&index)))
		if h != 0 {
			return func() { procAvRevertMmThreadChar.Call(h) }, nil
		}
		err = fmt.Errorf("win: MMCSS task %q: %w", task, callErr)
	}
	thread, _, _ := procGetCurrentThread.Call()
	old, _, _ := procGetThreadPriority.Call(thread)
	if ok, _, callErr := procSetThreadPriority.Call(thread, threadPriorityHighest); ok == 0 {
		return nil, fmt.Errorf("win: SetThreadPriority: %w (after %v)", callErr, err)
	}
	return func() { procSetThreadPriority.Call(thread, old) }, nil
}
