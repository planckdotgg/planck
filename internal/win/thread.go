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

	procGetCurrentProcess     = kernel32.NewProc("GetCurrentProcess")
	procSetProcessInformation = kernel32.NewProc("SetProcessInformation")
	procGetProcessInformation = kernel32.NewProc("GetProcessInformation")
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

// PROCESS_POWER_THROTTLING_STATE (processthreadsapi.h).
type powerThrottlingState struct {
	Version, ControlMask, StateMask uint32
}

const (
	processPowerThrottling               = 4
	processPowerThrottlingCurrentVersion = 1
	processPowerThrottlingExecutionSpeed = 0x1
	processPowerThrottlingIgnoreTimerRes = 0x4
	powerThrottlingOptOut                = processPowerThrottlingExecutionSpeed | processPowerThrottlingIgnoreTimerRes
)

// DisablePowerThrottling opts the process out of EcoQoS: Windows 11 runs a
// background process, which the host is whenever a game is in front, on
// efficiency cores with coalesced timers, and stalls every thread in it for
// 20-140 ms every few seconds, MMCSS or not.
func DisablePowerThrottling() error {
	proc, _, _ := procGetCurrentProcess.Call()
	st := powerThrottlingState{Version: processPowerThrottlingCurrentVersion, ControlMask: powerThrottlingOptOut}
	if ok, _, err := procSetProcessInformation.Call(proc, processPowerThrottling, uintptr(unsafe.Pointer(&st)), unsafe.Sizeof(st)); ok == 0 {
		return fmt.Errorf("win: SetProcessInformation(ProcessPowerThrottling): %w", err)
	}
	return nil
}

// powerThrottled reports the process's throttling control and state masks.
func powerThrottled() (control, state uint32, err error) {
	proc, _, _ := procGetCurrentProcess.Call()
	st := powerThrottlingState{Version: processPowerThrottlingCurrentVersion}
	if ok, _, err := procGetProcessInformation.Call(proc, processPowerThrottling, uintptr(unsafe.Pointer(&st)), unsafe.Sizeof(st)); ok == 0 {
		return 0, 0, err
	}
	return st.ControlMask, st.StateMask, nil
}
